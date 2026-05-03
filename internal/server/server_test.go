package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veloriba/mygrok/internal/client"
	"github.com/veloriba/mygrok/internal/protocol"
)

func TestTunnelIntegration(t *testing.T) {
	token := "test-token"
	domain := "example.com"

	// Start Server on dynamic ports
	srv := NewTunnelServer(token, domain, "127.0.0.1:0", "127.0.0.1:0")
	go srv.Start()
	time.Sleep(100 * time.Millisecond) // Wait for addresses to update
	defer srv.Stop()

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from local"))
	}))
	defer localSrv.Close()

	// Client uses srv.ControlAddr which is now updated with the real port
	cl := client.NewTunnelClient(srv.ControlAddr, token, "test-sub", localSrv.URL)
	go cl.Start()
	time.Sleep(200 * time.Millisecond)

	req, _ := http.NewRequest("GET", "http://"+srv.HTTPAddr, nil)
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
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0")
	go srv.Start()
	time.Sleep(50 * time.Millisecond)
	defer srv.Stop()

	conn, _ := net.Dial("tcp", srv.ControlAddr)
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
	srv := NewTunnelServer(token, "ex.com", "127.0.0.1:0", "127.0.0.1:0")
	go srv.Start()
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	// Start 3 tunnels
	for i := 1; i <= 3; i++ {
		sub := fmt.Sprintf("sub%d", i)
		localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(r.URL.Query().Get("sub")))
		}))
		cl := client.NewTunnelClient(srv.ControlAddr, token, sub, localSrv.URL+"?sub="+sub)
		go cl.Start()
	}
	time.Sleep(300 * time.Millisecond)

	// Verify all 3
	for i := 1; i <= 3; i++ {
		sub := fmt.Sprintf("sub%d", i)
		req, _ := http.NewRequest("GET", "http://"+srv.HTTPAddr, nil)
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
