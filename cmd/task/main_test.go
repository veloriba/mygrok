package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestBuildCommandArgs pins the client CLI args the scaffolder generates, in
// particular that --insecure is derived from --proto (an https upstream is
// typically self-signed in dev) and that --public-port is only added when
// requested.
func TestBuildCommandArgs(t *testing.T) {
	cases := []struct {
		name       string
		proto      string
		port       string
		sub        string
		localHost  string
		publicPort int
		want       []string
	}{
		{
			name:  "http",
			proto: "http", port: "3000", sub: "my-app", localHost: "127.0.0.1",
			want: []string{
				"mygrok", "http", "3000", "my-app",
				"--server", "${MYGROK_SERVER}",
				"--no-tui", "--log-format", "json",
				"--local-host", "127.0.0.1",
			},
		},
		{
			name:  "https adds --insecure",
			proto: "https", port: "8443", sub: "my-app", localHost: "127.0.0.1",
			want: []string{
				"mygrok", "https", "8443", "my-app",
				"--server", "${MYGROK_SERVER}",
				"--no-tui", "--log-format", "json",
				"--local-host", "127.0.0.1",
				"--insecure",
			},
		},
		{
			name:  "tcp with public port",
			proto: "tcp", port: "22", sub: "ssh", localHost: "127.0.0.1", publicPort: 2222,
			want: []string{
				"mygrok", "tcp", "22", "ssh",
				"--server", "${MYGROK_SERVER}",
				"--no-tui", "--log-format", "json",
				"--local-host", "127.0.0.1",
				"--public-port", "2222",
			},
		},
		{
			name:  "udp",
			proto: "udp", port: "53", sub: "dns", localHost: "127.0.0.1",
			want: []string{
				"mygrok", "udp", "53", "dns",
				"--server", "${MYGROK_SERVER}",
				"--no-tui", "--log-format", "json",
				"--local-host", "127.0.0.1",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCommandArgs(tc.proto, tc.port, tc.sub, tc.localHost, tc.publicPort)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildCommandArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSanitizeLabel pins how the multi-mode scaffolder turns a directory
// basename into a valid compose project / container name.
func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"myapp":       "myapp",
		"My_App!":     "my-app",
		"my game.dev": "my-game-dev",
		"---":         "app",
		"":            "app",
		"____":        "app",
		strings.Repeat("a", 60) + strings.Repeat("b", 20): strings.Repeat("a", 60) + "bbb",
		strings.Repeat("a", 62) + "-bc":                   strings.Repeat("a", 62),
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// multiTestParams returns a valid multi-add parameter set for a fresh dir.
func multiTestParams(dir string) multiAddParams {
	return multiAddParams{
		dir:       dir,
		localHost: "127.0.0.1",
		server:    "your-vps.com:7000",
		token:     "your-secret-token",
		tag:       "0.3.0",
	}
}

// TestMultiAddFirstRun pins the files a first `add --multi` produces: the
// compose file runs `mygrok up --config` with the tunnels.json mount and
// env passthrough, .env is 0600, tunnels.json is 0644 and parses to the
// requested spec.
func TestMultiAddFirstRun(t *testing.T) {
	dir := t.TempDir()
	p := multiTestParams(dir)
	p.sub, p.proto, p.port = "api", "http", 3000
	if err := runMultiAdd(p); err != nil {
		t.Fatalf("runMultiAdd: %v", err)
	}

	mygrokDir := filepath.Join(dir, "mygrok")
	for name, wantMode := range map[string]os.FileMode{
		"tunnels.json":       0o644,
		"docker-compose.yml": 0o644,
		".env":               0o600,
	} {
		info, err := os.Stat(filepath.Join(mygrokDir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %o, want %o", name, info.Mode().Perm(), wantMode)
		}
	}

	cfg, err := loadMultiConfig(filepath.Join(mygrokDir, "tunnels.json"))
	if err != nil {
		t.Fatalf("loadMultiConfig: %v", err)
	}
	wantSpecs := []multiTunnelSpec{{Name: "api", Protocol: "http", Port: 3000, Subdomain: "api"}}
	if !reflect.DeepEqual(cfg.Tunnels, wantSpecs) {
		t.Errorf("tunnels = %+v, want %+v", cfg.Tunnels, wantSpecs)
	}

	composePath := filepath.Join(mygrokDir, "docker-compose.yml")
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		`command: ["mygrok", "up", "--config", "/etc/mygrok/tunnels.json", "--no-tui", "--log-format", "json"]`,
		"MYGROK_SERVER: ${MYGROK_SERVER}",
		"MYGROK_TOKEN: ${MYGROK_TOKEN}",
		"./tunnels.json:/etc/mygrok/tunnels.json:ro",
		"network_mode: host",
		"restart: unless-stopped",
		"name: mygrok-" + sanitizeLabel(filepath.Base(dir)),
		"container_name: mygrok_" + sanitizeLabel(filepath.Base(dir)),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("compose file missing %q\n%s", want, s)
		}
	}

	env, err := os.ReadFile(filepath.Join(mygrokDir, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	for _, want := range []string{
		"MYGROK_SERVER=your-vps.com:7000",
		"MYGROK_TOKEN=your-secret-token",
		"MYGROK_TAG=0.3.0",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env missing %q\n%s", want, env)
		}
	}
}

// TestMultiAddNetwork pins that --network produces the external-network
// compose block (no host networking) and that the per-tunnel local_host
// lands in the spec.
func TestMultiAddNetwork(t *testing.T) {
	dir := t.TempDir()
	p := multiTestParams(dir)
	p.sub, p.proto, p.port = "api", "http", 3000
	p.network, p.localHost = "proxy", "api"
	if err := runMultiAdd(p); err != nil {
		t.Fatalf("runMultiAdd: %v", err)
	}

	cfg, err := loadMultiConfig(filepath.Join(dir, "mygrok", "tunnels.json"))
	if err != nil {
		t.Fatalf("loadMultiConfig: %v", err)
	}
	wantSpecs := []multiTunnelSpec{{Name: "api", Protocol: "http", Port: 3000, Subdomain: "api", LocalHost: "api"}}
	if !reflect.DeepEqual(cfg.Tunnels, wantSpecs) {
		t.Errorf("tunnels = %+v, want %+v", cfg.Tunnels, wantSpecs)
	}

	data, err := os.ReadFile(filepath.Join(dir, "mygrok", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "networks:\n      - proxy") || !strings.Contains(s, "external: true") {
		t.Errorf("compose file missing external network block\n%s", s)
	}
	if strings.Contains(s, "network_mode: host") {
		t.Errorf("compose file must not use host networking with --network\n%s", s)
	}
}

// TestMultiAddAppends pins that a second `add --multi` appends to
// tunnels.json (first entry preserved) and leaves the compose file untouched.
func TestMultiAddAppends(t *testing.T) {
	dir := t.TempDir()
	first := multiTestParams(dir)
	first.sub, first.proto, first.port = "api", "http", 3000
	if err := runMultiAdd(first); err != nil {
		t.Fatalf("first add: %v", err)
	}
	second := multiTestParams(dir)
	second.sub, second.proto, second.port = "ssh", "tcp", 22
	second.publicPort = 2222
	if err := runMultiAdd(second); err != nil {
		t.Fatalf("second add: %v", err)
	}

	mygrokDir := filepath.Join(dir, "mygrok")
	composePath := filepath.Join(mygrokDir, "docker-compose.yml")
	before, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if err := runMultiAdd(multiTestParamsWith(dir, "db", "tcp", 5432, 0)); err != nil {
		t.Fatalf("third add: %v", err)
	}
	after, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("re-read compose: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("compose file changed on append:\n%s", after)
	}

	cfg, err := loadMultiConfig(filepath.Join(mygrokDir, "tunnels.json"))
	if err != nil {
		t.Fatalf("loadMultiConfig: %v", err)
	}
	want := []multiTunnelSpec{
		{Name: "api", Protocol: "http", Port: 3000, Subdomain: "api"},
		{Name: "ssh", Protocol: "tcp", Port: 22, Subdomain: "ssh", PublicPort: 2222},
		{Name: "db", Protocol: "tcp", Port: 5432, Subdomain: "db"},
	}
	if !reflect.DeepEqual(cfg.Tunnels, want) {
		t.Errorf("tunnels = %+v, want %+v", cfg.Tunnels, want)
	}
}

// TestMultiAddDuplicateSub pins that re-adding an existing subdomain is
// refused (the server would tear the older tunnel down on takeover).
func TestMultiAddDuplicateSub(t *testing.T) {
	dir := t.TempDir()
	p := multiTestParams(dir)
	p.sub, p.proto, p.port = "api", "http", 3000
	if err := runMultiAdd(p); err != nil {
		t.Fatalf("first add: %v", err)
	}
	q := multiTestParams(dir)
	q.sub, q.proto, q.port = "api", "http", 3000
	if err := runMultiAdd(q); err == nil {
		t.Fatal("duplicate subdomain accepted, want error")
	} else if !strings.Contains(err.Error(), "already in") {
		t.Errorf("error = %q, want it to mention the existing entry", err)
	}
}

// TestMultiAddSingleModeConflict pins that a single-mode dir (compose file
// but no tunnels.json) is refused by `add --multi`.
func TestMultiAddSingleModeConflict(t *testing.T) {
	dir := t.TempDir()
	mygrokDir := filepath.Join(dir, "mygrok")
	if err := os.MkdirAll(mygrokDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mygrokDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := multiTestParams(dir)
	p.sub, p.proto, p.port = "api", "http", 3000
	err := runMultiAdd(p)
	if err == nil {
		t.Fatal("single-mode dir accepted by --multi, want error")
	}
	if !strings.Contains(err.Error(), "single-tunnel") {
		t.Errorf("error = %q, want it to name the conflict", err)
	}
}

// TestModeGuards pins both direction guards: a multi-mode dir is refused by
// plain `add`, a single-mode dir by `add --multi`, clean dirs pass both.
func TestModeGuards(t *testing.T) {
	clean := t.TempDir()
	multiDir := filepath.Join(clean, "multi", "mygrok")
	singleDir := filepath.Join(clean, "single", "mygrok")
	for _, d := range []string{multiDir, singleDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(dir, file string) {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(multiDir, "docker-compose.yml")
	mk(multiDir, "tunnels.json")
	mk(singleDir, "docker-compose.yml")

	if err := guardSingleAdd(multiDir); err == nil {
		t.Error("guardSingleAdd: multi-mode dir accepted, want error")
	}
	if err := guardSingleAdd(singleDir); err != nil {
		t.Errorf("guardSingleAdd: single-mode dir rejected: %v", err)
	}
	if err := guardSingleAdd(clean); err != nil {
		t.Errorf("guardSingleAdd: clean dir rejected: %v", err)
	}

	if err := guardMultiAdd(singleDir); err == nil {
		t.Error("guardMultiAdd: single-mode dir accepted, want error")
	}
	if err := guardMultiAdd(multiDir); err != nil {
		t.Errorf("guardMultiAdd: multi-mode dir rejected: %v", err)
	}
	if err := guardMultiAdd(clean); err != nil {
		t.Errorf("guardMultiAdd: clean dir rejected: %v", err)
	}
}

// multiTestParamsWith is a convenience constructor for the append tests.
func multiTestParamsWith(dir, sub, proto string, port, publicPort int) multiAddParams {
	p := multiTestParams(dir)
	p.sub, p.proto, p.port, p.publicPort = sub, proto, port, publicPort
	return p
}
