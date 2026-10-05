package client

import (
	"strings"
	"testing"

	"github.com/veloriba/mygrok/internal/protocol"
)

func TestNewManagerEmpty(t *testing.T) {
	if _, err := NewManager(nil, false); err == nil {
		t.Fatal("expected error for empty client list")
	}
}

func TestNewManagerDuplicateSubdomain(t *testing.T) {
	mk := func(sub, name string) *TunnelClient {
		return NewTunnelClient(Config{Subdomain: sub, LocalAddr: "127.0.0.1:3000", Name: name})
	}
	if _, err := NewManager([]*TunnelClient{mk("api", "a"), mk("api", "b")}, true); err == nil {
		t.Fatal("expected duplicate subdomain error")
	}
	// empty subdomains (random) never collide
	if _, err := NewManager([]*TunnelClient{mk("", "x"), mk("", "y")}, true); err != nil {
		t.Fatalf("empty subdomains must not collide: %v", err)
	}
}

func TestNewManagerOK(t *testing.T) {
	a := NewTunnelClient(Config{Subdomain: "a", LocalAddr: "127.0.0.1:3000"})
	b := NewTunnelClient(Config{Subdomain: "b", LocalAddr: "127.0.0.1:3001"})
	m, err := NewManager([]*TunnelClient{a, b}, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if len(m.clients) != 2 {
		t.Fatalf("clients = %d, want 2", len(m.clients))
	}
}

func TestNameFallback(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Name: "custom", Subdomain: "api", LocalAddr: "127.0.0.1:3000"}, "custom"},
		{Config{Subdomain: "api", LocalAddr: "127.0.0.1:3000"}, "api"},
		{Config{LocalAddr: "127.0.0.1:3000"}, "http://127.0.0.1:3000"},
		{Config{LocalAddr: "127.0.0.1:22", Protocol: protocol.ProtocolTCP}, "127.0.0.1:22"},
	}
	for _, tc := range cases {
		if got := NewTunnelClient(tc.cfg).Name; got != tc.want {
			t.Errorf("Name = %q, want %q", got, tc.want)
		}
	}
}

func TestSnapshotStatus(t *testing.T) {
	c := NewTunnelClient(Config{Subdomain: "a", LocalAddr: "127.0.0.1:3000"})

	snap := c.Snapshot()
	if snap.Status != "connecting" {
		t.Errorf("fresh status = %q, want connecting", snap.Status)
	}
	if snap.Name != "a" {
		t.Errorf("name = %q, want a", snap.Name)
	}
	if snap.Protocol != protocol.ProtocolHTTP {
		t.Errorf("protocol = %q, want http", snap.Protocol)
	}
	if snap.PublicURL != "" || snap.ErrorMsg != "" {
		t.Errorf("fresh snap has state: %+v", snap)
	}

	c.mu.Lock()
	c.publicURL = "http://a.example.com:8080"
	c.publicPort = 8080
	c.requests = append(c.requests, RequestInfo{Method: "GET", Path: "/", Status: 200})
	c.mu.Unlock()

	snap = c.Snapshot()
	if snap.Status != "online" {
		t.Errorf("online status = %q, want online", snap.Status)
	}
	if snap.PublicURL != "http://a.example.com:8080" || snap.PublicPort != 8080 {
		t.Errorf("snap = %+v", snap)
	}
	if len(snap.Requests) != 1 || snap.Requests[0].Method != "GET" {
		t.Errorf("requests = %+v", snap.Requests)
	}

	// a copy of the request ring: mutating the snapshot must not affect the client
	snap.Requests[0].Method = "PATCH"
	if got := c.Snapshot().Requests[0].Method; got != "GET" {
		t.Errorf("snapshot mutated client state: %q", got)
	}

	c.mu.Lock()
	c.publicURL = ""
	c.publicPort = 0
	c.errorMsg = "boom"
	c.mu.Unlock()
	if got := c.Snapshot().Status; got != "error" {
		t.Errorf("error status = %q, want error", got)
	}
}

func TestPadVisible(t *testing.T) {
	padded := padVisible("abc\x1b[31mred\x1b[0m", 8)
	if visibleLen(padded) != 8 {
		t.Errorf("visibleLen = %d, want 8", visibleLen(padded))
	}
	if !strings.HasSuffix(padded, " ") {
		t.Errorf("padded = %q", padded)
	}
	if got := padVisible("abcdefgh", 5); got != "abcdefgh" {
		t.Errorf("overlong string must not be padded: %q", got)
	}
}
