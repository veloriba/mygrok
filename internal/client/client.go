package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/veloriba/mygrok/internal/protocol"
	"github.com/fatih/color"
	"github.com/hashicorp/yamux"
)

type RequestInfo struct {
	Timestamp time.Time
	Method    string
	Path      string
	Status    int
	Duration  time.Duration
}

type TunnelClient struct {
	ServerAddr string
	Token      string
	Subdomain  string
	LocalAddr  string // e.g. "http://localhost:3000"

	mu        sync.Mutex
	requests  []RequestInfo
	publicURL string
	errorMsg  string
	startTime time.Time
}

func NewTunnelClient(serverAddr, token, subdomain, localAddr string) *TunnelClient {
	// Ensure localAddr has scheme
	if !strings.HasPrefix(localAddr, "http") {
		localAddr = "http://" + localAddr
	}
	return &TunnelClient{
		ServerAddr: serverAddr,
		Token:      token,
		Subdomain:  subdomain,
		LocalAddr:  localAddr,
		startTime:  time.Now(),
	}
}

func (c *TunnelClient) Start() error {
	go c.uiLoop()
	for {
		err := c.connect()
		if err != nil {
			c.mu.Lock()
			c.publicURL = ""
			c.errorMsg = err.Error()
			c.mu.Unlock()
			time.Sleep(5 * time.Second)
			continue
		}
	}
}

func (c *TunnelClient) connect() error {
	conn, err := net.Dial("tcp", c.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := protocol.HandshakeRequest{
		Token:     c.Token,
		Subdomain: c.Subdomain,
		Protocol:  "http",
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}

	var resp protocol.HandshakeResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("server rejected handshake: %s", resp.Message)
	}

	c.mu.Lock()
	c.publicURL = resp.URL
	c.errorMsg = ""
	c.mu.Unlock()

	session, err := yamux.Server(conn, nil)
	if err != nil {
		return err
	}

	// Create a reverse proxy to our local app
	target, _ := url.Parse(c.LocalAddr)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Ensure the Host header is set to the local target
	// Next.js and other dev servers often reject requests with the wrong Host header
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
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

	// Serve the HTTP requests over the yamux session
	return http.Serve(session, handler)
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

func (c *TunnelClient) addRequest(info RequestInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, info)
	if len(c.requests) > 20 {
		c.requests = c.requests[1:]
	}
}

func (c *TunnelClient) uiLoop() {
	blue := color.New(color.FgBlue).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	white := color.New(color.FgWhite, color.Bold).SprintFunc()

	for {
		c.mu.Lock()
		urlStr := c.publicURL
		errMsg := c.errorMsg
		requests := make([]RequestInfo, len(c.requests))
		copy(requests, c.requests)
		c.mu.Unlock()

		fmt.Print("\033[H\033[2J")
		fmt.Printf("%s\n\n", white("mygrok"))
		
		status := yellow("connecting...")
		if urlStr != "" {
			status = green("online")
		} else if errMsg != "" {
			status = color.New(color.FgRed).Sprint("error")
		}

		fmt.Printf("%-20s %s\n", "Session Status", status)
		if errMsg != "" {
			fmt.Printf("%-20s %s\n", "Error", color.New(color.FgRed).Sprint(errMsg))
		}
		fmt.Printf("%-20s %s\n", "Version", "0.1.0")
		fmt.Printf("%-20s %s\n", "Region", "Europe")
		
		// Show both HTTP and HTTPS if possible, or just the one we have
		if urlStr != "" {
			fmt.Printf("%-20s %s -> %s\n", "Forwarding", blue(urlStr), cyan(c.LocalAddr))
			// If it's http, also show how it would look with https (assuming Nginx is set up)
			if strings.HasPrefix(urlStr, "http://") {
				httpsURL := "https://" + urlStr[7:]
				fmt.Printf("%-20s %s -> %s\n", "", blue(httpsURL), cyan(c.LocalAddr))
			}
		}
		
		fmt.Printf("\n%s\n", white("HTTP Requests"))
		fmt.Printf("%-30s %-10s %-40s %s\n", "TIME", "METHOD", "PATH", "STATUS")
		fmt.Println(strings.Repeat("-", 100))

		for i := len(requests) - 1; i >= 0; i-- {
			req := requests[i]
			statusStr := fmt.Sprintf("%d", req.Status)
			if req.Status >= 200 && req.Status < 300 {
				statusStr = green(statusStr)
			} else if req.Status >= 400 {
				statusStr = color.New(color.FgRed).Sprint(statusStr)
			}
			
			fmt.Printf("%-30s %-10s %-40s %s\n", 
				req.Timestamp.Format("15:04:05.000"),
				req.Method,
				req.Path,
				statusStr,
			)
		}

		time.Sleep(500 * time.Millisecond)
	}
}
