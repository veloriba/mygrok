package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veloriba/mygrok/internal/client"
	"github.com/veloriba/mygrok/internal/protocol"
)

func TestTunnelIntegration(t *testing.T) {
	token := "test-token"
	domain := "example.com"

	// Start Server on dynamic ports
	srv := NewTunnelServer(token, domain, "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(100 * time.Millisecond) // Wait for addresses to update
	defer srv.Stop()

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from local"))
	}))
	defer localSrv.Close()

	// Client uses srv.ControlListenAddr() which is now updated with the real port
	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "test-sub",
		LocalAddr:  localSrv.URL,
	})
	go cl.Start()
	time.Sleep(200 * time.Millisecond)

	req, _ := http.NewRequest("GET", "http://"+srv.HTTPListenAddr(), nil)
	req.Host = "test-sub.example.com"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from local" {
		t.Errorf("Expected 'hello from local', got '%s'", string(body))
	}
}

func TestHandshakeAuth(t *testing.T) {
	token := "secret"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(50 * time.Millisecond)
	defer srv.Stop()

	conn, _ := net.Dial("tcp", srv.ControlListenAddr())
	json.NewEncoder(conn).Encode(protocol.HandshakeRequest{Token: "wrong"})

	var resp protocol.HandshakeResponse
	json.NewDecoder(conn).Decode(&resp)
	if resp.Status != "error" || resp.Message != "unauthorized" {
		t.Errorf("Expected unauthorized error, got %v", resp)
	}
	conn.Close()
}

func TestMultipleTunnels(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	// Start 3 tunnels
	for i := 1; i <= 3; i++ {
		sub := fmt.Sprintf("sub%d", i)
		localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(r.URL.Query().Get("sub")))
		}))
		cl := client.NewTunnelClient(client.Config{
			ServerAddr: srv.ControlListenAddr(),
			Token:      token,
			Subdomain:  sub,
			LocalAddr:  localSrv.URL + "?sub=" + sub,
		})
		go cl.Start()
	}
	time.Sleep(300 * time.Millisecond)

	// Verify all 3
	for i := 1; i <= 3; i++ {
		sub := fmt.Sprintf("sub%d", i)
		req, _ := http.NewRequest("GET", "http://"+srv.HTTPListenAddr(), nil)
		req.Host = sub + ".ex.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("Tunnel %s failed: %v", sub, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != sub {
			t.Errorf("Expected %s, got %s", sub, string(body))
		}
		resp.Body.Close()
	}
}

// startTCPEcho launches a local TCP server that echoes whatever it receives.
func startTCPEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(cc net.Conn) {
				defer cc.Close()
				buf := make([]byte, 1024)
				for {
					n, err := cc.Read(buf)
					if n > 0 {
						cc.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestTunnelTCP(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	localAddr, stop := startTCPEcho(t)
	defer stop()

	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "tcp-sub",
		LocalAddr:  localAddr,
		Protocol:   protocol.ProtocolTCP,
		NoTUI:      true,
	})
	go cl.Start()

	var port int
	for i := 0; i < 100; i++ {
		if port = cl.PublicPort(); port != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("client never received a public port")
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial public tcp port: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Errorf("expected echo 'ping', got %q", string(buf[:n]))
	}
}

// startUDPEcho launches a local UDP server that echoes whatever it receives.
func startUDPEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve udp: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			conn.WriteToUDP(buf[:n], addr)
		}
	}()
	return conn.LocalAddr().String(), func() { conn.Close() }
}

func TestTunnelUDP(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	localAddr, stop := startUDPEcho(t)
	defer stop()

	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "udp-sub",
		LocalAddr:  localAddr,
		Protocol:   protocol.ProtocolUDP,
		NoTUI:      true,
	})
	go cl.Start()

	var port int
	for i := 0; i < 100; i++ {
		if port = cl.PublicPort(); port != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("client never received a public port")
	}

	pubAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	clientUDP, err := net.DialUDP("udp", nil, pubAddr)
	if err != nil {
		t.Fatalf("dial public udp port: %v", err)
	}
	defer clientUDP.Close()

	if _, err := clientUDP.Write([]byte("udp-ping")); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	clientUDP.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := clientUDP.Read(buf)
	if err != nil {
		t.Fatalf("read udp echo: %v", err)
	}
	if string(buf[:n]) != "udp-ping" {
		t.Errorf("expected echo 'udp-ping', got %q", string(buf[:n]))
	}
}

// TestAdminStats verifies the loopback admin surface: /healthz and /stats work,
// /stats reflects the active tunnel and its served counter, and the stats path
// is NOT reachable through the public HTTP listener (which routes by subdomain).
func TestAdminStats(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	srv.AdminAddr = "127.0.0.1:0"
	go srv.Start()
	time.Sleep(150 * time.Millisecond)
	defer srv.Stop()

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer localSrv.Close()

	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "stats-sub",
		LocalAddr:  localSrv.URL,
		NoTUI:      true,
	})
	go cl.Start()
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", "http://"+srv.HTTPListenAddr(), nil)
		req.Host = "stats-sub.ex.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("tunnel request: %v", err)
		}
		resp.Body.Close()
	}

	var out struct {
		TunnelCount int `json:"tunnels"`
		Tunnels     []struct {
			Subdomain string `json:"subdomain"`
			Served    uint64 `json:"served"`
		} `json:"tunnel_list"`
	}
	resp, err := http.Get("http://" + srv.AdminListenAddr() + "/stats")
	if err != nil {
		t.Fatalf("stats request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stats status = %d, want 200", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if out.TunnelCount < 1 {
		t.Fatalf("expected >=1 tunnel, got %d", out.TunnelCount)
	}
	var found bool
	for _, tn := range out.Tunnels {
		if tn.Subdomain == "stats-sub" {
			found = true
			if tn.Served < 5 {
				t.Errorf("served = %d, want >=5", tn.Served)
			}
		}
	}
	if !found {
		t.Fatalf("stats-sub missing from stats: %+v", out.Tunnels)
	}

	// /stats must not be reachable via the public listener (no subdomain host).
	pr, err := http.Get("http://" + srv.HTTPListenAddr() + "/_mygrok/stats")
	if err != nil {
		t.Fatalf("public stats probe: %v", err)
	}
	pr.Body.Close()
	if pr.StatusCode != 404 {
		t.Errorf("public /_mygrok/stats = %d, want 404", pr.StatusCode)
	}
}

// TestHTTPStreamReclaimed is the regression guard for the wedge: it drives a
// burst of concurrent requests through an HTTP tunnel and asserts the in-flight
// counter drains back to zero and every request is accounted for. A yamux
// stream leak would pin in-flight handlers and grow without bound.
func TestHTTPStreamReclaimed(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	srv.AdminAddr = "127.0.0.1:0"
	go srv.Start()
	time.Sleep(150 * time.Millisecond)
	defer srv.Stop()

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	defer local.Close()

	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "burst",
		LocalAddr:  local.URL,
		NoTUI:      true,
	})
	go cl.Start()
	time.Sleep(200 * time.Millisecond)

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://"+srv.HTTPListenAddr(), nil)
			req.Host = "burst.ex.com"
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	var inflight int64
	var served uint64
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + srv.AdminListenAddr() + "/stats")
		if err != nil {
			t.Fatalf("stats request: %v", err)
		}
		var out struct {
			Inflight int64 `json:"inflight_requests"`
			Tunnels  []struct {
				Subdomain string `json:"subdomain"`
				Served    uint64 `json:"served"`
			} `json:"tunnel_list"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		inflight = out.Inflight
		for _, tn := range out.Tunnels {
			if tn.Subdomain == "burst" {
				served = tn.Served
			}
		}
		if inflight == 0 && served >= n {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if inflight != 0 {
		t.Errorf("in-flight did not drain to 0: %d", inflight)
	}
	if served < n {
		t.Errorf("served = %d, want >=%d", served, n)
	}
}

// TestSubdomainFromHost pins the Host-header parsing rules: the subdomain must
// be separated from the server domain by a dot, and a port suffix in the host
// ("api.example.com:8080", when dialing the raw front port directly) must not
// break the match.
func TestSubdomainFromHost(t *testing.T) {
	cases := []struct {
		domain string
		host   string
		want   string
	}{
		{"example.com", "api.example.com", "api"},
		{"example.com", "a.b.example.com", "a.b"},
		{"example.com", "api.example.com:8080", "api"},
		{"example.com", "example.com", ""},
		{"example.com", "example.com:8080", ""},
		{"ex.com", "subex.com", ""}, // old suffix-only match parsed this as "su"
		{"example.com", "other.com", ""},
		{"example.com", "", ""},
	}
	for _, tc := range cases {
		srv := NewTunnelServer("t", tc.domain, "127.0.0.1:0", "127.0.0.1:0", 0, 0)
		if got := srv.subdomainFromHost(tc.host); got != tc.want {
			t.Errorf("domain %q host %q: subdomainFromHost = %q, want %q", tc.domain, tc.host, got, tc.want)
		}
	}
}

// TestHTTPRequestToTCPTunnel is the regression guard for the deployment break:
// an HTTP request routed by hostname to a tunnel declared as "tcp" (e.g. a vllm
// server exposed as https://<sub>.domain/v1) must be proxied over the tunnel's
// session, not panic on a typed-nil *http.Transport.
func TestHTTPRequestToTCPTunnel(t *testing.T) {
	token := "tok"
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0", 0, 0)
	go srv.Start()
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello-over-tcp:" + r.URL.Path))
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	cl := client.NewTunnelClient(client.Config{
		ServerAddr: srv.ControlListenAddr(),
		Token:      token,
		Subdomain:  "tcpweb",
		LocalAddr:  backendAddr,
		Protocol:   protocol.ProtocolTCP,
		NoTUI:      true,
	})
	go cl.Start()
	time.Sleep(300 * time.Millisecond)

	req, _ := http.NewRequest("GET", "http://"+srv.HTTPListenAddr()+"/v1/models", nil)
	req.Host = "tcpweb.ex.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request to tcp tunnel over HTTP failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (body=%q)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "hello-over-tcp:/v1/models") {
		t.Errorf("unexpected body: %q", body)
	}
}
