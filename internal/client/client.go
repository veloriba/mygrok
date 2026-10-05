package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/veloriba/mygrok/internal/protocol"
)

type RequestInfo struct {
	Timestamp time.Time
	Method    string
	Path      string
	Status    int
	Duration  time.Duration
}

// Config holds the parameters for a TunnelClient.
type Config struct {
	ServerAddr    string
	Token         string
	Subdomain     string
	LocalAddr     string // http: "http://host:port"; tcp/udp: "host:port"
	Protocol      string // protocol.ProtocolHTTP (default), ProtocolTCP, ProtocolUDP
	RequestedPort int    // tcp/udp: requested public port, 0 = auto-assign
	NoTUI         bool
	Name          string       // display name for logs and UI; falls back to subdomain, then localAddr
	Logger        *slog.Logger // per-tunnel logger; defaults to slog.Default()
	// HTTP-only options:
	FlushInterval   time.Duration // -1 = flush after every write
	Insecure        bool          // skip TLS verification for the local target
	SetHeaders      []string      // "Key: Value" headers injected into proxied requests
	UpstreamTimeout time.Duration // 0 = no timeout
}

type TunnelClient struct {
	ServerAddr    string
	Token         string
	Subdomain     string
	LocalAddr     string
	Protocol      string
	RequestedPort int
	NoTUI         bool
	Name          string
	log           *slog.Logger

	FlushInterval   time.Duration
	Insecure        bool
	SetHeaders      []string
	UpstreamTimeout time.Duration

	mu         sync.Mutex
	requests   []RequestInfo
	publicURL  string
	publicPort int
	errorMsg   string
	startTime  time.Time

	totalRequests uint64
	bytesSent     uint64
	bytesReceived uint64
}

func NewTunnelClient(cfg Config) *TunnelClient {
	if cfg.Protocol == "" {
		cfg.Protocol = protocol.ProtocolHTTP
	}
	localAddr := cfg.LocalAddr
	if cfg.Protocol == protocol.ProtocolHTTP && !strings.HasPrefix(localAddr, "http") {
		localAddr = "http://" + localAddr
	}
	name := cfg.Name
	if name == "" {
		name = cfg.Subdomain
	}
	if name == "" {
		name = localAddr
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &TunnelClient{
		ServerAddr:      cfg.ServerAddr,
		Token:           cfg.Token,
		Subdomain:       cfg.Subdomain,
		LocalAddr:       localAddr,
		Protocol:        cfg.Protocol,
		RequestedPort:   cfg.RequestedPort,
		NoTUI:           cfg.NoTUI,
		Name:            name,
		log:             logger,
		FlushInterval:   cfg.FlushInterval,
		Insecure:        cfg.Insecure,
		SetHeaders:      cfg.SetHeaders,
		UpstreamTimeout: cfg.UpstreamTimeout,
		startTime:       time.Now(),
	}
}

// PublicPort returns the public port assigned to this tunnel (tcp/udp), or 0
// until the handshake completes.
func (c *TunnelClient) PublicPort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publicPort
}

// ClientSnapshot is an immutable point-in-time view of a tunnel's state,
// consumed by the shared TUI dashboard.
type ClientSnapshot struct {
	Name          string
	Protocol      string
	Status        string // "connecting" | "online" | "error"
	PublicURL     string
	PublicPort    int
	ErrorMsg      string
	LocalAddr     string
	StartTime     time.Time
	TotalRequests uint64
	BytesSent     uint64
	BytesReceived uint64
	Requests      []RequestInfo
}

// Snapshot returns the tunnel's current state under the client lock.
func (c *TunnelClient) Snapshot() ClientSnapshot {
	c.mu.Lock()
	snap := ClientSnapshot{
		Name:          c.Name,
		Protocol:      c.Protocol,
		PublicURL:     c.publicURL,
		PublicPort:    c.publicPort,
		ErrorMsg:      c.errorMsg,
		LocalAddr:     c.LocalAddr,
		StartTime:     c.startTime,
		TotalRequests: atomic.LoadUint64(&c.totalRequests),
		BytesSent:     atomic.LoadUint64(&c.bytesSent),
		BytesReceived: atomic.LoadUint64(&c.bytesReceived),
		Requests:      make([]RequestInfo, len(c.requests)),
	}
	copy(snap.Requests, c.requests)
	c.mu.Unlock()

	switch {
	case snap.PublicURL != "":
		snap.Status = "online"
	case snap.ErrorMsg != "":
		snap.Status = "error"
	default:
		snap.Status = "connecting"
	}
	return snap
}

// logger returns the structured logger for the client.
func (c *TunnelClient) logger() *slog.Logger {
	return c.log
}

// logf emits a structured lifecycle event. It is only active in --no-tui mode
// so a service manager / journal can observe connect and reconnect events; in
// TUI mode the dashboard already renders state and stderr would corrupt it.
func (c *TunnelClient) logf(level slog.Level, msg string, args ...any) {
	if c.NoTUI {
		c.logger().Log(context.Background(), level, msg, args...)
	}
}

// backoff returns an exponentially growing, jittered reconnect delay capped at
// max for the given 1-based attempt. The jitter avoids a thundering herd when
// many clients reconnect at once after a server restart.
func backoff(base, max time.Duration, attempt int) time.Duration {
	if attempt <= 0 {
		return base
	}
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return time.Duration(int64(d) * (75 + rand.Int63n(26)) / 100)
}

func (c *TunnelClient) reconnectInterval() time.Duration {
	if v := os.Getenv("MYGROK_RECONNECT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Second
}

// Start runs the reconnect loop until done is closed. Signal handling and the
// TUI dashboard live in the TunnelManager (or cmd), which owns the done channel.
func (c *TunnelClient) Start(done <-chan struct{}) {
	base := c.reconnectInterval()
	var attempt int
	for {
		select {
		case <-done:
			return
		default:
		}
		started := time.Now()
		err := c.connect(done)
		if err != nil {
			c.mu.Lock()
			c.publicURL = ""
			c.publicPort = 0
			c.errorMsg = err.Error()
			c.mu.Unlock()

			// A session that stayed up at least as long as the base interval is
			// considered healthy, so reset the backoff after real connectivity.
			if time.Since(started) >= base {
				attempt = 0
			}
			attempt++
			wait := backoff(base, 30*time.Second, attempt)
			c.logf(slog.LevelWarn, "disconnected", "err", err.Error(), "attempt", attempt, "reconnect_in", wait.String())
			select {
			case <-done:
				return
			case <-time.After(wait):
			}
			continue
		}
	}
}

func (c *TunnelClient) connect(done <-chan struct{}) error {
	dialer := net.Dialer{KeepAlive: 30 * time.Second}
	conn, err := dialer.Dial("tcp", c.ServerAddr)
	if err != nil {
		return err
	}

	// Wrap connection to track bytes
	conn = &countingConn{Conn: conn, c: c}
	defer conn.Close()

	// A single bufio.Reader is shared between the handshake and the yamux
	// session. The server may emit its first yamux frame in the same TCP
	// segment as the handshake response; if the handshake used its own reader
	// that frame would be buffered and lost when yamux wraps the conn in a
	// fresh reader.
	r := bufio.NewReader(conn)

	req := protocol.HandshakeRequest{
		Token:     c.Token,
		Subdomain: c.Subdomain,
		Protocol:  c.Protocol,
		Port:      c.RequestedPort,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}

	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	var resp protocol.HandshakeResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("server rejected handshake: %s", resp.Message)
	}

	host, _, _ := net.SplitHostPort(c.ServerAddr)
	c.mu.Lock()
	c.errorMsg = ""
	switch c.Protocol {
	case protocol.ProtocolTCP, protocol.ProtocolUDP:
		c.publicPort = resp.Port
		c.publicURL = fmt.Sprintf("%s://%s:%d", c.Protocol, host, resp.Port)
	default:
		c.publicURL = resp.URL
	}
	c.mu.Unlock()

	c.logf(slog.LevelInfo, "connected", "protocol", c.Protocol, "url", c.publicURL)

	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.LogOutput = nil // yamux rejects both Logger and LogOutput being set
	// Route yamux's internal [ERR]/[WARN] logging through slog in service mode;
	// discard it in TUI mode so it cannot corrupt the dashboard redraw.
	if c.NoTUI {
		cfg.Logger = log.New(slogWriter{logger: c.logger().With("component", "yamux")}, "", 0)
	} else {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	session, err := yamux.Server(&bufferedConn{r: r, w: conn, c: conn}, cfg)
	if err != nil {
		return err
	}

	switch c.Protocol {
	case protocol.ProtocolTCP:
		return c.serveTCP(session, done)
	case protocol.ProtocolUDP:
		return c.serveUDP(session, done)
	default:
		return c.serveHTTP(session, done)
	}
}

func (c *TunnelClient) serveHTTP(session *yamux.Session, done <-chan struct{}) error {
	// Create a reverse proxy to our local app
	target, _ := url.Parse(c.LocalAddr)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// The stdlib ReverseProxy already auto-flushes SSE (text/event-stream) and
	// chunked (ContentLength == -1) responses immediately, so FlushInterval
	// mainly affects responses with a known Content-Length.
	proxy.FlushInterval = c.FlushInterval

	// Ensure the Host header is set to the local target
	// Next.js and other dev servers often reject requests with the wrong Host header
	originalDirector := proxy.Director
	hostDirector := func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}
	// Inject user-provided headers after the original Director runs
	if len(c.SetHeaders) > 0 {
		proxy.Director = func(req *http.Request) {
			hostDirector(req)
			for _, h := range c.SetHeaders {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					continue
				}
				req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
	} else {
		proxy.Director = hostDirector
	}

	// Build a custom transport when --insecure and/or --upstream-timeout are set;
	// otherwise keep the default transport.
	if c.Insecure || c.UpstreamTimeout > 0 {
		var transport *http.Transport
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = dt.Clone()
		} else {
			transport = &http.Transport{}
		}
		if c.Insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		if c.UpstreamTimeout > 0 {
			transport.ResponseHeaderTimeout = c.UpstreamTimeout
		}
		proxy.Transport = transport
	}

	// Wrap the proxy with our logging middleware
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		proxy.ServeHTTP(rw, r)

		c.addRequest(RequestInfo{
			Timestamp: start,
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    rw.status,
			Duration:  time.Since(start),
		})
	})

	server := &http.Server{Handler: handler}
	go func() {
		<-done
		server.Close()
		// Close the yamux session too: http.Serve's own listener is the
		// session, so without this the serve loop (and thus Start) would
		// never observe done.
		session.Close()
	}()

	// Serve the HTTP requests over the yamux session
	return http.Serve(session, handler)
}

// serveTCP accepts one yamux stream per inbound (server-side) connection and
// bridges it to the local target.
func (c *TunnelClient) serveTCP(session *yamux.Session, done <-chan struct{}) error {
	go func() {
		<-done
		session.Close()
	}()
	for {
		stream, err := session.Accept()
		if err != nil {
			return err
		}
		go c.bridgeStreamToLocal(stream)
	}
}

func (c *TunnelClient) bridgeStreamToLocal(stream net.Conn) {
	defer stream.Close()
	local, err := net.Dial("tcp", c.LocalAddr)
	if err != nil {
		return
	}
	defer local.Close()
	errc := make(chan struct{}, 2)
	go func() { io.Copy(stream, local); errc <- struct{}{} }()
	go func() { io.Copy(local, stream); errc <- struct{}{} }()
	<-errc
}

// serveUDP bridges a single yamux stream to the local UDP target. Packets are
// framed with the external peer address so the server can route responses back
// to the correct peer. Responses are sent to the most recent peer (single-peer
// friendly; last-writer-wins under concurrent peers).
func (c *TunnelClient) serveUDP(session *yamux.Session, done <-chan struct{}) error {
	go func() {
		<-done
		session.Close()
	}()
	stream, err := session.Accept()
	if err != nil {
		return err
	}
	defer stream.Close()

	local, err := net.ResolveUDPAddr("udp", c.LocalAddr)
	if err != nil {
		return err
	}
	localConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}
	defer localConn.Close()

	var mu sync.Mutex
	var lastPeer []byte

	// stream -> local target
	go func() {
		for {
			peer, payload, err := protocol.ReadUDPPacket(stream)
			if err != nil {
				return
			}
			mu.Lock()
			lastPeer = peer
			mu.Unlock()
			localConn.WriteToUDP(payload, local)
		}
	}()

	// local target -> stream (responses routed to the most recent peer)
	buf := make([]byte, 65536)
	for {
		n, _, err := localConn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		mu.Lock()
		peer := lastPeer
		mu.Unlock()
		if err := protocol.WriteUDPPacket(stream, peer, buf[:n]); err != nil {
			return err
		}
	}
}

func (c *TunnelClient) addRequest(info RequestInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, info)
	if len(c.requests) > 20 {
		c.requests = c.requests[1:]
	}
	atomic.AddUint64(&c.totalRequests, 1)
}

// bufferedConn is an io.ReadWriteCloser that reads from a shared *bufio.Reader
// (used by the handshake) while writing to and closing the underlying conn.
// This lets yamux reuse any bytes the handshake reader already buffered.
type bufferedConn struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

func (bc *bufferedConn) Read(p []byte) (int, error)  { return bc.r.Read(p) }
func (bc *bufferedConn) Write(p []byte) (int, error) { return bc.w.Write(p) }
func (bc *bufferedConn) Close() error                { return bc.c.Close() }

// slogWriter bridges yamux's *log.Logger output into slog, mapping the
// "[ERR]"/"[WARN]" prefixes to levels so the noise is filterable.
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

type countingConn struct {
	net.Conn
	c *TunnelClient
}

func (cc *countingConn) Read(p []byte) (n int, err error) {
	n, err = cc.Conn.Read(p)
	atomic.AddUint64(&cc.c.bytesReceived, uint64(n))
	return
}

func (cc *countingConn) Write(p []byte) (n int, err error) {
	n, err = cc.Conn.Write(p)
	atomic.AddUint64(&cc.c.bytesSent, uint64(n))
	return
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacker not supported")
	}
	return h.Hijack()
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
