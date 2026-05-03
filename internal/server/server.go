package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/veloriba/mygrok/internal/protocol"
	"github.com/hashicorp/yamux"
)

type TunnelServer struct {
	Token       string
	Domain      string
	ControlAddr string
	HTTPAddr    string

	mu        sync.RWMutex
	tunnels   map[string]*yamux.Session
	listeners []net.Listener
}

func NewTunnelServer(token, domain, controlAddr, httpAddr string) *TunnelServer {
	return &TunnelServer{
		Token:       token,
		Domain:      domain,
		ControlAddr: controlAddr,
		HTTPAddr:    httpAddr,
		tunnels:     make(map[string]*yamux.Session),
	}
}

func (s *TunnelServer) Start() error {
	errc := make(chan error, 2)
	go func() {
		errc <- s.startHTTP()
	}()
	go func() {
		errc <- s.startControl()
	}()
	return <-errc
}

func (s *TunnelServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.listeners {
		ln.Close()
	}
	for _, session := range s.tunnels {
		session.Close()
	}
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
	
	log.Printf("Control server listening on %s", s.ControlAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleControl(conn)
	}
}

func (s *TunnelServer) handleControl(conn net.Conn) {
	var req protocol.HandshakeRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		conn.Close()
		return
	}

	if req.Token != s.Token {
		log.Printf("Unauthorized connection attempt")
		json.NewEncoder(conn).Encode(protocol.HandshakeResponse{Status: "error", Message: "unauthorized"})
		conn.Close()
		return
	}

	subdomain := req.Subdomain
	if subdomain == "" {
		subdomain = s.generateRandomSubdomain()
	}

	s.mu.Lock()
	if oldSession, exists := s.tunnels[subdomain]; exists {
		log.Printf("Subdomain %s taken, closing old session to allow reconnect", subdomain)
		oldSession.Close()
		delete(s.tunnels, subdomain)
	}

	session, err := yamux.Client(conn, nil)
	if err != nil {
		s.mu.Unlock()
		conn.Close()
		return
	}

	s.tunnels[subdomain] = session
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.tunnels, subdomain)
		s.mu.Unlock()
		session.Close()
	}()

	log.Printf("Tunnel established: %s.%s", subdomain, s.Domain)
	json.NewEncoder(conn).Encode(protocol.HandshakeResponse{
		Status:    "ok",
		Subdomain: subdomain,
		URL:       fmt.Sprintf("http://%s.%s", subdomain, s.Domain),
	})

	for {
		if session.IsClosed() {
			break
		}
		time.Sleep(1 * time.Second)
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

	log.Printf("HTTP proxy listening on %s", s.HTTPAddr)
	
	server := &http.Server{Handler: s}
	return server.Serve(ln)
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
	session, ok := s.tunnels[subdomain]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("Tunnel %s not online", subdomain), http.StatusNotFound)
		return
	}

	// Support WebSockets (hijacking)
	if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
		s.handleWebSocket(w, r, session)
		return
	}

	// Use ReverseProxy with a custom Transport that opens Yamux streams
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
			// Forwarding headers
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		},
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				return session.Open()
			},
		},
	}

	proxy.ServeHTTP(w, r)
}

func (s *TunnelServer) handleWebSocket(w http.ResponseWriter, r *http.Request, session *yamux.Session) {
	// Open stream to client
	stream, err := session.Open()
	if err != nil {
		http.Error(w, "Could not open tunnel stream", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Forward initial HTTP request to client
	if err := r.Write(stream); err != nil {
		log.Printf("Failed to write websocket request to tunnel: %v", err)
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
		log.Printf("Failed to hijack connection: %v", err)
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

func (s *TunnelServer) generateRandomSubdomain() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
