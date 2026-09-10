package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/veloriba/mygrok/internal/protocol"
	"github.com/veloriba/mygrok/internal/version"
)

// bufferedConn is an io.ReadWriteCloser that reads from a shared *bufio.Reader
// (used by the handshake) while writing to and closing the underlying conn.
type bufferedConn struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

func (bc *bufferedConn) Read(p []byte) (int, error)  { return bc.r.Read(p) }
func (bc *bufferedConn) Write(p []byte) (int, error) { return bc.w.Write(p) }
func (bc *bufferedConn) Close() error                { return bc.c.Close() }

// slogWriter bridges a *log.Logger (used by yamux and httputil.ReverseProxy)
// into slog, mapping the "[ERR]"/"[WARN]" prefixes yamux emits to levels so the
// noise is filterable and attributable instead of dumping raw lines to stderr.
type slogWriter struct {
	logger *slog.Logger
}

func (w slogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line == "" {
		return len(p), nil
	}
	lvl := slog.LevelDebug
	switch {
	case strings.Contains(line, "[ERR]"):
		lvl = slog.LevelError
	case strings.Contains(line, "[WARN]"):
		lvl = slog.LevelWarn
	}
	w.logger.Log(context.Background(), lvl, line)
	return len(p), nil
}

// tunnel is a single registered client connection. For HTTP it only carries the
// yamux session; for TCP/UDP it also owns a public listener on an assigned port.
type tunnel struct {
	protocol  string
	session   *yamux.Session
	transport *http.Transport
	tcpLn     net.Listener
	udpLn     *net.UDPConn
	port      int

	id        string    // short connection id, for log correlation
	subdomain string    // registered subdomain
	remote    string    // client remote address
	since     time.Time // when the tunnel was established
	served    atomic.Uint64
}

func (t *tunnel) close() {
	if t.tcpLn != nil {
		t.tcpLn.Close()
	}
	if t.udpLn != nil {
		t.udpLn.Close()
	}
	if t.transport != nil {
		t.transport.CloseIdleConnections()
	}
	if t.session != nil {
		t.session.Close()
	}
}

type TunnelServer struct {
	Token       string
	Domain      string
	ControlAddr string
	HTTPAddr    string

	// AdminAddr, when non-empty, is a separate (loopback by default) listener
	// that serves /stats and /healthz. It is intentionally NOT the public
	// HTTPAddr so the metrics surface is never reachable through the tunnel
	// front-end. Set to "" to disable.
	AdminAddr  string
	AdminToken string // optional; grants non-loopback admin access via header

	Logger *slog.Logger

	mu        sync.RWMutex
	tunnels   map[string]*tunnel
	listeners []net.Listener
	portBase  int
	portCount int

	startTime time.Time
	connSeq   atomic.Uint64
	inflight  atomic.Int64
}

func NewTunnelServer(token, domain, controlAddr, httpAddr string, portBase, portCount int) *TunnelServer {
	if portBase <= 0 {
		portBase = 20000
	}
	if portCount <= 0 {
		portCount = 100
	}
	return &TunnelServer{
		Token:       token,
		Domain:      domain,
		ControlAddr: controlAddr,
		HTTPAddr:    httpAddr,
		tunnels:     make(map[string]*tunnel),
		portBase:    portBase,
		portCount:   portCount,
		Logger:      slog.Default(),
		startTime:   time.Now(),
	}
}

func (s *TunnelServer) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *TunnelServer) yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.LogOutput = nil // yamux rejects both Logger and LogOutput being set
	cfg.Logger = log.New(slogWriter{logger: s.log().With("component", "yamux")}, "", 0)
	return cfg
}

func (s *TunnelServer) Start() error {
	errc := make(chan error, 2)
	go func() {
		errc <- s.startHTTP()
	}()
	go func() {
		errc <- s.startControl()
	}()
	// The admin/stats listener is auxiliary: a bind failure must NOT take down
	// the tunnel server (which would crash-loop and drop every tunnel).
	if s.AdminAddr != "" {
		go func() {
			if err := s.startAdmin(); err != nil {
				s.log().Error("admin listener unavailable (tunnels unaffected)", "addr", s.AdminAddr, "err", err)
			}
		}()
	}
	return <-errc
}

func (s *TunnelServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.listeners {
		ln.Close()
	}
	for _, t := range s.tunnels {
		t.close()
	}
}

// ControlListenAddr, HTTPListenAddr, and AdminListenAddr return the resolved
// listener addresses (after ephemeral :0 ports are bound). They lock because the
// addresses are rewritten by the per-listener goroutines started in Start.
func (s *TunnelServer) ControlListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ControlAddr
}

func (s *TunnelServer) HTTPListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.HTTPAddr
}

func (s *TunnelServer) AdminListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.AdminAddr
}

func (s *TunnelServer) startControl() error {
	ln, err := net.Listen("tcp", s.ControlAddr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.ControlAddr = ln.Addr().String()
	s.mu.Unlock()

	s.log().Info("control server listening", "addr", s.ControlAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleControl(conn)
	}
}

// portListener is the common surface of a bound tcp/udp listener.
type portListener interface {
	Close() error
}

// allocListener binds a public port for a tcp/udp tunnel. If requested > 0 it
// uses that exact port; otherwise it scans the configured range for a free one.
func (s *TunnelServer) allocListener(proto string, requested int) (int, portListener, error) {
	tryPort := func(p int) (portListener, error) {
		if proto == protocol.ProtocolTCP {
			return net.Listen("tcp", fmt.Sprintf(":%d", p))
		}
		udpAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", p))
		return net.ListenUDP("udp", udpAddr)
	}
	if requested > 0 {
		pl, err := tryPort(requested)
		if err != nil {
			return 0, nil, fmt.Errorf("port %d unavailable: %w", requested, err)
		}
		return requested, pl, nil
	}
	for p := s.portBase; p < s.portBase+s.portCount; p++ {
		if pl, err := tryPort(p); err == nil {
			return p, pl, nil
		}
	}
	return 0, nil, fmt.Errorf("no free port in range %d-%d", s.portBase, s.portBase+s.portCount-1)
}

func (s *TunnelServer) handleControl(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	cid := fmt.Sprintf("c%d", s.connSeq.Add(1))
	lg := s.log().With("conn", cid, "remote", remote)
	lg.Debug("control connection opened")

	// A single bufio.Reader is shared between the handshake and the yamux
	// session so no frame is lost if it arrives in the same TCP segment as
	// the handshake (see the matching note in the client).
	r := bufio.NewReader(conn)

	line, err := r.ReadString('\n')
	if err != nil {
		lg.Debug("handshake read failed", "err", err)
		conn.Close()
		return
	}
	var req protocol.HandshakeRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		lg.Warn("handshake decode failed", "err", err)
		conn.Close()
		return
	}

	if req.Token != s.Token {
		lg.Warn("unauthorized connection attempt")
		json.NewEncoder(conn).Encode(protocol.HandshakeResponse{Status: "error", Message: "unauthorized"})
		conn.Close()
		return
	}

	if req.Protocol == "" {
		req.Protocol = protocol.ProtocolHTTP
	}

	subdomain := req.Subdomain
	if subdomain == "" {
		subdomain = s.generateRandomSubdomain()
	}
	lg = lg.With("subdomain", subdomain, "proto", req.Protocol)

	// Close any existing tunnel for this subdomain so a reconnect can take over.
	s.mu.Lock()
	if old, exists := s.tunnels[subdomain]; exists {
		lg.Warn("subdomain takeover, closing old session", "old_conn", old.id, "old_age", time.Since(old.since).Round(time.Second).String())
		delete(s.tunnels, subdomain)
		go old.close()
	}
	s.mu.Unlock()

	session, err := yamux.Client(&bufferedConn{r: r, w: conn, c: conn}, s.yamuxConfig())
	if err != nil {
		lg.Warn("yamux session init failed", "err", err)
		conn.Close()
		return
	}

	t := &tunnel{
		protocol:  req.Protocol,
		session:   session,
		id:        cid,
		subdomain: subdomain,
		remote:    remote,
		since:     time.Now(),
	}

	// Every tunnel gets a transport, regardless of protocol: an HTTP request
	// arriving on the proxy listener is routed by hostname and may target a
	// tunnel declared as "tcp" (e.g. a vllm server fronted as
	// https://<sub>.domain/v1). Restricting the transport to HTTP-protocol
	// tunnels made ServeHTTP proxy over a typed-nil *http.Transport and panic.
	t.transport = newYamuxTransport(session)

	if req.Protocol == protocol.ProtocolTCP || req.Protocol == protocol.ProtocolUDP {
		port, pl, err := s.allocListener(req.Protocol, req.Port)
		if err != nil {
			lg.Warn("port allocation failed", "err", err)
			session.Close()
			json.NewEncoder(conn).Encode(protocol.HandshakeResponse{Status: "error", Message: err.Error()})
			return
		}
		t.port = port
		if req.Protocol == protocol.ProtocolTCP {
			t.tcpLn = pl.(net.Listener)
		} else {
			t.udpLn = pl.(*net.UDPConn)
		}
	}

	s.mu.Lock()
	s.tunnels[subdomain] = t
	s.mu.Unlock()

	resp := protocol.HandshakeResponse{Status: "ok", Subdomain: subdomain}
	if req.Protocol == protocol.ProtocolHTTP {
		resp.URL = fmt.Sprintf("http://%s.%s", subdomain, s.Domain)
		lg.Info("tunnel established", "url", resp.URL)
	} else {
		resp.Port = t.port
		lg.Info("tunnel established", "port", t.port)
	}
	json.NewEncoder(conn).Encode(resp)

	if req.Protocol == protocol.ProtocolTCP {
		go s.bridgeTCP(t)
	} else if req.Protocol == protocol.ProtocolUDP {
		go s.serveUDP(t)
	}

	// Block until the session ends (client disconnect or keepalive timeout),
	// then unregister and tear the tunnel down deterministically.
	for !session.IsClosed() {
		time.Sleep(1 * time.Second)
	}

	s.mu.Lock()
	if cur := s.tunnels[subdomain]; cur == t {
		delete(s.tunnels, subdomain)
	}
	s.mu.Unlock()
	t.close()

	lg.Info("tunnel closed", "served", t.served.Load(), "duration", time.Since(t.since).Round(time.Second).String())
}

// bridgeTCP accepts inbound connections on the public port and pipes each one
// through a yamux stream to the client.
func (s *TunnelServer) bridgeTCP(t *tunnel) {
	lg := s.log().With("conn", t.id, "subdomain", t.subdomain, "proto", "tcp")
	for {
		conn, err := t.tcpLn.Accept()
		if err != nil {
			return
		}
		t.served.Add(1)
		go func(c net.Conn) {
			defer c.Close()
			stream, err := t.session.Open()
			if err != nil {
				lg.Debug("open stream failed", "err", err)
				return
			}
			defer stream.Close()
			errc := make(chan struct{}, 2)
			go func() { io.Copy(stream, c); errc <- struct{}{} }()
			go func() { io.Copy(c, stream); errc <- struct{}{} }()
			<-errc
		}(conn)
	}
}

// serveUDP bridges the public UDP port to a single yamux stream. Each packet is
// framed with the external peer address so responses route back correctly.
func (s *TunnelServer) serveUDP(t *tunnel) {
	stream, err := t.session.Open()
	if err != nil {
		return
	}
	defer stream.Close()

	// external -> stream
	go func() {
		buf := make([]byte, 65536)
		for {
			n, addr, err := t.udpLn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			t.served.Add(1)
			if err := protocol.WriteUDPPacket(stream, []byte(addr.String()), buf[:n]); err != nil {
				return
			}
		}
	}()

	// stream -> external
	for {
		peer, payload, err := protocol.ReadUDPPacket(stream)
		if err != nil {
			return
		}
		addr, err := net.ResolveUDPAddr("udp", string(peer))
		if err != nil {
			continue
		}
		t.udpLn.WriteToUDP(payload, addr)
	}
}

func (s *TunnelServer) startHTTP() error {
	ln, err := net.Listen("tcp", s.HTTPAddr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.HTTPAddr = ln.Addr().String()
	s.mu.Unlock()

	s.log().Info("HTTP proxy listening", "addr", s.HTTPAddr)

	// ReadHeaderTimeout/IdleTimeout reclaim connections without imposing a
	// WriteTimeout, which would break long-lived SSE/streaming responses.
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return srv.Serve(ln)
}

func (s *TunnelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	domainLen := len(s.Domain)
	subdomain := ""
	if len(host) > domainLen+1 && host[len(host)-domainLen:] == s.Domain {
		subdomain = host[:len(host)-domainLen-1]
	}

	if subdomain == "" {
		http.Error(w, "Tunnel Not Found", http.StatusNotFound)
		return
	}

	s.mu.RLock()
	t, ok := s.tunnels[subdomain]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("Tunnel %s not online", subdomain), http.StatusNotFound)
		return
	}

	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	t.served.Add(1)

	lg := s.log().With("subdomain", subdomain)

	// Support WebSockets (hijacking)
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleWebSocket(w, r, t.session, lg)
		return
	}

	// Defensive: never hand ReverseProxy a typed-nil *http.Transport (which
	// would panic). All tunnels set a transport at registration, but guard the
	// fallback so a nil transport can never crash the proxy.
	transport := t.transport
	if transport == nil {
		transport = newYamuxTransport(t.session)
	}

	// Use ReverseProxy with a per-tunnel shared Transport whose Dial opens (and,
	// via DisableKeepAlives, closes) one Yamux stream per request. This is the
	// fix for the stream leak that eventually wedged the proxy accept loop.
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		},
		Transport: transport,
		ErrorLog:  log.New(slogWriter{logger: lg.With("component", "proxy")}, "", 0),
	}

	proxy.ServeHTTP(w, r)
}

func (s *TunnelServer) handleWebSocket(w http.ResponseWriter, r *http.Request, session *yamux.Session, lg *slog.Logger) {
	// Open stream to client
	stream, err := session.Open()
	if err != nil {
		lg.Warn("could not open tunnel stream", "err", err)
		http.Error(w, "Could not open tunnel stream", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Forward initial HTTP request to client
	if err := r.Write(stream); err != nil {
		lg.Warn("failed to write websocket request to tunnel", "err", err)
		return
	}

	// Hijack the connection to the browser (Nginx)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Webserver doesn't support hijacking", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		lg.Warn("failed to hijack connection", "err", err)
		http.Error(w, "Failed to hijack connection", http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Pipe the raw TCP connection between the browser and the tunnel stream
	errc := make(chan error, 2)
	cp := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		errc <- err
	}

	// Note: We use bufrw.Reader because Hijack returns any data already buffered by the server
	go cp(stream, bufrw)
	go cp(conn, stream)

	<-errc
}

// newYamuxTransport builds a reusable Transport for an HTTP tunnel. A single
// stream is opened per request and closed when the response completes
// (DisableKeepAlives), so streams never accumulate on the session.
func newYamuxTransport(session *yamux.Session) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return openStream(ctx, session)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// openStream opens a yamux stream while honoring context cancellation, so a
// hung/cancelled request cannot pin a stream indefinitely.
func openStream(ctx context.Context, session *yamux.Session) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		stream net.Conn
		err    error
	}
	ch := make(chan result)
	go func() {
		st, err := session.Open()
		if err != nil {
			select {
			case ch <- result{nil, err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- result{st, nil}:
		case <-ctx.Done():
			st.Close() // request went away before we handed it off
		}
	}()
	select {
	case res := <-ch:
		return res.stream, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *TunnelServer) generateRandomSubdomain() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// --- admin / stats surface (loopback by default, optional token) ---

type statsTunnel struct {
	Subdomain string    `json:"subdomain"`
	Protocol  string    `json:"protocol"`
	Port      int       `json:"port,omitempty"`
	Conn      string    `json:"conn"`
	Remote    string    `json:"remote"`
	Since     time.Time `json:"since"`
	Served    uint64    `json:"served"`
}

type runtimeStats struct {
	Goroutines  int     `json:"goroutines"`
	NumCPU      int     `json:"num_cpu"`
	HeapAllocMB float64 `json:"heap_alloc_mb"`
	SysMB       float64 `json:"sys_mb"`
	NumGC       uint32  `json:"num_gc"`
}

type statsResponse struct {
	Now         time.Time     `json:"now"`
	Version     string        `json:"version"`
	Uptime      string        `json:"uptime"`
	TunnelCount int           `json:"tunnels"`
	Inflight    int64         `json:"inflight_requests"`
	Tunnels     []statsTunnel `json:"tunnel_list"`
	Runtime     runtimeStats  `json:"runtime"`
	Addrs       statsAddrs    `json:"addrs"`
}

type statsAddrs struct {
	Control string `json:"control"`
	HTTP    string `json:"http"`
	Admin   string `json:"admin"`
}

func (s *TunnelServer) startAdmin() error {
	ln, err := net.Listen("tcp", s.AdminAddr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.AdminAddr = ln.Addr().String()
	s.mu.Unlock()

	s.log().Info("admin listener listening", "addr", s.AdminAddr, "token_auth", s.AdminToken != "")

	mux := http.NewServeMux()
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/_mygrok/stats", s.handleStats)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Handler:           s.adminAuth(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return srv.Serve(ln)
}

// adminAuth permits loopback callers unconditionally, and any caller that
// presents a valid admin token (so the endpoint can be safely forwarded if the
// operator chooses to bind it beyond loopback).
func (s *TunnelServer) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		if s.AdminToken != "" {
			tok := r.Header.Get("X-Mygrok-Admin-Token")
			if tok == "" {
				tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.AdminToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func (s *TunnelServer) handleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := make([]statsTunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		list = append(list, statsTunnel{
			Subdomain: t.subdomain,
			Protocol:  t.protocol,
			Port:      t.port,
			Conn:      t.id,
			Remote:    t.remote,
			Since:     t.since,
			Served:    t.served.Load(),
		})
	}
	control, httpAddr, adminAddr := s.ControlAddr, s.HTTPAddr, s.AdminAddr
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Subdomain < list[j].Subdomain })

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	out := statsResponse{
		Now:         time.Now(),
		Version:     version.Version,
		Uptime:      time.Since(s.startTime).Round(time.Second).String(),
		TunnelCount: len(list),
		Inflight:    s.inflight.Load(),
		Tunnels:     list,
		Runtime: runtimeStats{
			Goroutines:  runtime.NumGoroutine(),
			NumCPU:      runtime.NumCPU(),
			HeapAllocMB: float64(ms.HeapAlloc) / (1 << 20),
			SysMB:       float64(ms.Sys) / (1 << 20),
			NumGC:       ms.NumGC,
		},
		Addrs: statsAddrs{Control: control, HTTP: httpAddr, Admin: adminAddr},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
