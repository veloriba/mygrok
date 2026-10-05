package main

import (
	"reflect"
	"testing"

	"github.com/veloriba/mygrok/internal/protocol"
)

func TestParseConfigLegacy(t *testing.T) {
	cfg, err := parseConfig([]byte(`{"server":"srv.example.com:7000","token":"tok","port":3000,"subdomain":"api","scheme":"https"}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Server != "srv.example.com:7000" || cfg.Token != "tok" || cfg.Port != 3000 ||
		cfg.Subdomain != "api" || cfg.Scheme != "https" {
		t.Errorf("legacy fields: %+v", cfg)
	}
	if len(cfg.Tunnels) != 0 {
		t.Errorf("Tunnels = %v, want empty", cfg.Tunnels)
	}
}

func TestParseConfigTunnels(t *testing.T) {
	data := `{
		"tunnels": [
			{"name":"web","protocol":"https","port":8443,"subdomain":"web","insecure":true,"set_headers":["X-Env: dev"],"local_host":"10.0.0.5"},
			{"protocol":"tcp","port":22,"subdomain":"ssh","public_port":2222},
			{"protocol":"udp","port":53,"subdomain":"dns"}
		]
	}`
	cfg, err := parseConfig([]byte(data))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Tunnels) != 3 {
		t.Fatalf("Tunnels = %d entries, want 3", len(cfg.Tunnels))
	}
	web := cfg.Tunnels[0]
	if web.Name != "web" || web.Protocol != "https" || web.Port != 8443 || web.Subdomain != "web" ||
		!web.Insecure || web.LocalHost != "10.0.0.5" ||
		!reflect.DeepEqual(web.SetHeaders, []string{"X-Env: dev"}) {
		t.Errorf("web spec: %+v", web)
	}
	ssh := cfg.Tunnels[1]
	if ssh.Protocol != "tcp" || ssh.Port != 22 || ssh.Subdomain != "ssh" || ssh.PublicPort != 2222 {
		t.Errorf("ssh spec: %+v", ssh)
	}
	dns := cfg.Tunnels[2]
	if dns.Protocol != "udp" || dns.Port != 53 || dns.Subdomain != "dns" {
		t.Errorf("dns spec: %+v", dns)
	}
}

func TestParseTriples(t *testing.T) {
	specs, err := parseTriples([]string{"http", "3000", "api", "tcp", "22", "ssh"})
	if err != nil {
		t.Fatalf("parseTriples: %v", err)
	}
	want := []TunnelSpec{
		{Protocol: "http", Port: 3000, Subdomain: "api"},
		{Protocol: "tcp", Port: 22, Subdomain: "ssh"},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("specs = %+v, want %+v", specs, want)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"bad arity", []string{"http", "3000", "api", "tcp"}},
		{"unknown proto", []string{"sctp", "22", "ssh"}},
		{"non-numeric port", []string{"http", "abc", "api"}},
		{"negative port", []string{"http", "-1", "api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTriples(tc.args); err == nil {
				t.Fatalf("args %v: expected error", tc.args)
			}
		})
	}
}

func testGlobals() globalOpts {
	return globalOpts{
		serverAddr:    "srv:7000",
		token:         "tok",
		localHost:     "127.0.0.1",
		setHeaders:    []string{"X-Global: 1"},
		flushInterval: -1,
	}
}

func TestClientFromSpec(t *testing.T) {
	g := testGlobals()

	c, err := clientFromSpec(TunnelSpec{Protocol: "https", Port: 8443, Subdomain: "web"}, g)
	if err != nil {
		t.Fatalf("https spec: %v", err)
	}
	if c.Protocol != protocol.ProtocolHTTP || !c.Insecure || c.LocalAddr != "https://127.0.0.1:8443" {
		t.Errorf("https spec: proto=%q insecure=%v local=%q", c.Protocol, c.Insecure, c.LocalAddr)
	}

	c, err = clientFromSpec(TunnelSpec{Protocol: "tcp", Port: 22, Subdomain: "ssh", PublicPort: 2222, LocalHost: "10.0.0.5"}, g)
	if err != nil {
		t.Fatalf("tcp spec: %v", err)
	}
	if c.Protocol != protocol.ProtocolTCP || c.LocalAddr != "10.0.0.5:22" || c.RequestedPort != 2222 {
		t.Errorf("tcp spec: proto=%q local=%q port=%d", c.Protocol, c.LocalAddr, c.RequestedPort)
	}

	c, err = clientFromSpec(TunnelSpec{Protocol: "http", Port: 3000, Subdomain: "api", SetHeaders: []string{"X-Spec: 2"}}, g)
	if err != nil {
		t.Fatalf("http spec: %v", err)
	}
	if !reflect.DeepEqual(c.SetHeaders, []string{"X-Spec: 2"}) {
		t.Errorf("spec headers must win: %v", c.SetHeaders)
	}

	c, err = clientFromSpec(TunnelSpec{Protocol: "http", Port: 3000, Subdomain: "api"}, g)
	if err != nil {
		t.Fatalf("global headers: %v", err)
	}
	if !reflect.DeepEqual(c.SetHeaders, g.setHeaders) {
		t.Errorf("global headers must be the fallback: %v", c.SetHeaders)
	}
	if c.Name != "api" {
		t.Errorf("name fallback = %q, want api", c.Name)
	}

	if _, err := clientFromSpec(TunnelSpec{Protocol: "sctp", Port: 22}, g); err == nil {
		t.Error("unknown protocol must fail")
	}
	if _, err := clientFromSpec(TunnelSpec{Protocol: "http", Port: 0}, g); err == nil {
		t.Error("missing port must fail")
	}
}

func TestSelectTunnelsListWinsOverLegacy(t *testing.T) {
	g := testGlobals()
	cfg := &ProfileConfig{
		Server:    "srv:7000",
		Token:     "tok",
		Port:      3000,
		Subdomain: "legacy",
		Scheme:    "http",
		Tunnels: []TunnelSpec{
			{Protocol: "http", Port: 4000, Subdomain: "list"},
		},
	}
	clients, err := selectTunnels(cfg, g)
	if err != nil {
		t.Fatalf("selectTunnels: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(clients))
	}
	if clients[0].Subdomain != "list" || clients[0].LocalAddr != "http://127.0.0.1:4000" {
		t.Errorf("tunnels list must win over legacy fields: sub=%q local=%q", clients[0].Subdomain, clients[0].LocalAddr)
	}
}

func TestSelectTunnelsLegacyOnly(t *testing.T) {
	g := testGlobals()
	cfg := &ProfileConfig{Server: "srv:7000", Token: "tok", Port: 3000, Subdomain: "api", Scheme: "https"}
	clients, err := selectTunnels(cfg, g)
	if err != nil {
		t.Fatalf("selectTunnels: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(clients))
	}
	c := clients[0]
	if c.Subdomain != "api" || c.LocalAddr != "https://127.0.0.1:3000" || c.Protocol != protocol.ProtocolHTTP {
		t.Errorf("legacy client: sub=%q local=%q proto=%q", c.Subdomain, c.LocalAddr, c.Protocol)
	}
	if c.Insecure {
		t.Error("legacy https scheme must not force --insecure")
	}
}
