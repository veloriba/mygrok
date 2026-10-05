package client

import (
	"strings"
	"testing"
	"time"

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
	if len(m.tunnels) != 2 {
		t.Fatalf("clients = %d, want 2", len(m.tunnels))
	}
}

func mkReloadClient(sub, local string, mut func(*Config)) *TunnelClient {
	cfg := Config{Subdomain: sub, LocalAddr: local, ServerAddr: "srv:7000", Token: "tok"}
	if mut != nil {
		mut(&cfg)
	}
	return NewTunnelClient(cfg)
}

func TestReloadDiff(t *testing.T) {
	a := mkReloadClient("a", "http://127.0.0.1:3000", nil)
	b := mkReloadClient("b", "http://127.0.0.1:3001", nil)
	m, err := NewManager([]*TunnelClient{a, b}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	go m.Start(nil)
	defer m.Quit()
	time.Sleep(100 * time.Millisecond) // let Start spawn the initial loops
	oldB := m.tunnels[1]

	a2 := mkReloadClient("a", "http://127.0.0.1:3000", func(c *Config) { c.Name = "renamed" }) // cosmetic: kept
	b2 := mkReloadClient("b", "http://127.0.0.1:3001", func(c *Config) { c.SetHeaders = []string{"X: 1"} })
	c2 := mkReloadClient("c", "http://127.0.0.1:3002", nil)

	rep, err := m.Reload([]*TunnelClient{a2, b2, c2})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if rep.Added != 1 || rep.Removed != 0 || rep.Replaced != 1 || rep.Kept != 1 {
		t.Errorf("report = %v, want 1 added, 0 removed, 1 replaced, 1 kept", rep)
	}
	if m.tunnels[0].client != a {
		t.Error("unchanged tunnel must keep its live client")
	}
	if m.tunnels[1].client != b2 {
		t.Error("changed tunnel must be replaced")
	}
	if m.tunnels[2].client != c2 {
		t.Error("new tunnel must be appended")
	}
	// the kept tunnel must not have been stopped; the replaced one must have
	select {
	case <-m.tunnels[0].stop:
		t.Error("kept tunnel was stopped")
	default:
	}
	select {
	case <-oldB.stop:
	default:
		t.Error("replaced tunnel was not stopped")
	}
}

func TestReloadRemovedTunnelStops(t *testing.T) {
	a := mkReloadClient("a", "http://127.0.0.1:3000", nil)
	b := mkReloadClient("b", "http://127.0.0.1:3001", nil)
	m, err := NewManager([]*TunnelClient{a, b}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	go m.Start(nil)
	defer m.Quit()
	time.Sleep(100 * time.Millisecond)
	oldB := m.tunnels[1]

	if _, err := m.Reload([]*TunnelClient{a}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	select {
	case <-oldB.stop:
	default:
		t.Error("removed tunnel was not stopped")
	}
	if len(m.tunnels) != 1 {
		t.Errorf("tunnels = %d, want 1", len(m.tunnels))
	}
}

func TestReloadRejects(t *testing.T) {
	a := mkReloadClient("a", "http://127.0.0.1:3000", nil)
	m, err := NewManager([]*TunnelClient{a}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := m.Reload(nil); err == nil {
		t.Error("empty reload must be rejected")
	}
	dup := []*TunnelClient{
		mkReloadClient("x", "http://127.0.0.1:3000", nil),
		mkReloadClient("x", "http://127.0.0.1:3001", nil),
	}
	if _, err := m.Reload(dup); err == nil {
		t.Error("duplicate subdomains must be rejected")
	}
	// rejected reloads must not change the set
	if len(m.tunnels) != 1 || m.tunnels[0].client != a {
		t.Error("rejected reload changed the tunnel set")
	}
}

func TestReloadAfterQuit(t *testing.T) {
	a := mkReloadClient("a", "http://127.0.0.1:3000", nil)
	m, err := NewManager([]*TunnelClient{a}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	go m.Start(nil)
	m.Quit()
	time.Sleep(100 * time.Millisecond)
	if _, err := m.Reload([]*TunnelClient{a}); err == nil {
		t.Error("reload after shutdown must be rejected")
	}
}

func TestSnapshots(t *testing.T) {
	a := mkReloadClient("a", "http://127.0.0.1:3000", nil)
	m, err := NewManager([]*TunnelClient{a}, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	snaps := m.snapshots()
	if len(snaps) != 1 || snaps[0].Name != "a" {
		t.Errorf("snapshots = %+v", snaps)
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
