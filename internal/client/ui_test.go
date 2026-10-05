package client

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/veloriba/mygrok/internal/protocol"
	"github.com/veloriba/mygrok/internal/version"
)

// TestMain forces plain output so the render tests are deterministic whether
// or not the test binary happens to run with a TTY on stdout.
func TestMain(m *testing.M) {
	color.NoColor = true
	os.Exit(m.Run())
}

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// testSnaps is the fixed fixture for the multi-dashboard tests: two online
// tunnels (one http with requests, one tcp), one connecting, one error.
func testSnaps() []ClientSnapshot {
	return []ClientSnapshot{
		{
			Name: "api", Protocol: protocol.ProtocolHTTP, Status: "online",
			PublicURL: "http://api.example.com:8080", LocalAddr: "http://127.0.0.1:3000",
			StartTime:     testNow.Add(-1 * time.Hour),
			TotalRequests: 1234, BytesSent: 12500000, BytesReceived: 45000000,
			Requests: []RequestInfo{
				{Timestamp: testNow.Add(-30 * time.Second), Method: "GET", Path: "/v1/chat", Status: 200, Duration: 120 * time.Millisecond},
				{Timestamp: testNow.Add(-20 * time.Second), Method: "POST", Path: "/v1/submit", Status: 500, Duration: 1500 * time.Millisecond},
				{Timestamp: testNow.Add(-10 * time.Second), Method: "GET", Path: "/", Status: 200, Duration: 45 * time.Millisecond},
			},
		},
		{
			Name: "web", Protocol: protocol.ProtocolHTTP, Status: "connecting",
			LocalAddr: "http://127.0.0.1:3001", StartTime: testNow.Add(-5 * time.Minute),
		},
		{
			Name: "ssh", Protocol: protocol.ProtocolTCP, Status: "online",
			PublicURL: "tcp://127.0.0.1:20001", LocalAddr: "127.0.0.1:22",
			StartTime: testNow.Add(-30 * time.Minute),
			BytesSent: 1069547, BytesReceived: 922746,
		},
		{
			Name: "dns", Protocol: protocol.ProtocolHTTP, Status: "error",
			ErrorMsg:  "dial tcp 127.0.0.1:53: connection refused",
			LocalAddr: "http://127.0.0.1:53", StartTime: testNow.Add(-5 * time.Minute),
		},
	}
}

func frameLines(t *testing.T, render func(w *bytes.Buffer)) []string {
	t.Helper()
	var buf bytes.Buffer
	render(&buf)
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	return lines
}

func assertLines(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\ngot:\n%s\nwant:\n%s",
			len(got), len(want), strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i+1, got[i], want[i])
		}
	}
}

// multiTableLines is the shared table part of the multi-dashboard for
// testSnaps: header, column header, and the four rows.
func multiTableLines() []string {
	s := func(n int) string { return strings.Repeat(" ", n) }
	return []string{
		"mygrok",
		"",
		fmt.Sprintf("mygrok v%s · 4 tunnels · 2 online · 1 error · 1 connecting     j/k select · r refresh · q quit", version.Version),
		"",
		"NAME" + s(5) + "STATUS" + s(8) + "FORWARDING" + s(45) + "REQ" + s(2) + "TRAFFIC (sent/received)",
		"api" + s(6) + "● online" + s(6) + "http://api.example.com:8080 -> http://127.0.0.1:3000" + s(2) + "1234" + s(2) + "11.92 MB / 42.92 MB",
		"web" + s(6) + "● connecting" + s(2) + "http://127.0.0.1:3001" + s(36) + "0" + s(2) + "0.00 KB / 0.00 KB",
		"ssh" + s(6) + "● online" + s(6) + "tcp://127.0.0.1:20001 -> 127.0.0.1:22" + s(20) + "-" + s(2) + "1.02 MB / 901.12 KB",
		"dns" + s(6) + "○ error" + s(7) + "http://127.0.0.1:53" + s(38) + "0" + s(2) + "0.00 KB / 0.00 KB",
		"",
	}
}

// TestRenderMultiTable pins the multi-tunnel dashboard layout for a mixed
// fixture: header counts, the aligned table (online/connecting/error,
// http/tcp), and the detail section of the selected (first) tunnel.
func TestRenderMultiTable(t *testing.T) {
	sp := testSnaps()
	got := frameLines(t, func(w *bytes.Buffer) {
		renderMulti(w, newUIColors(), sp, 0, testNow)
	})

	want := append(multiTableLines(),
		"── api · last requests ──",
		"11:59:50.000"+strings.Repeat(" ", 9)+"GET"+strings.Repeat(" ", 8)+"200"+strings.Repeat(" ", 8)+"/ 45ms",
		"11:59:40.000"+strings.Repeat(" ", 9)+"POST"+strings.Repeat(" ", 7)+"500"+strings.Repeat(" ", 8)+"/v1/submit 1.5s",
		"11:59:30.000"+strings.Repeat(" ", 9)+"GET"+strings.Repeat(" ", 8)+"200"+strings.Repeat(" ", 8)+"/v1/chat 120ms",
	)
	assertLines(t, got, want)
}

// TestRenderMultiDetail covers the other detail branches: a tcp tunnel shows
// a stats line, an http tunnel without requests shows "no requests yet".
func TestRenderMultiDetail(t *testing.T) {
	sp := testSnaps()

	got := frameLines(t, func(w *bytes.Buffer) {
		renderMulti(w, newUIColors(), sp, 2, testNow)
	})
	want := append(multiTableLines(),
		"── ssh · stats ──",
		"1.02 MB sent, 901.12 KB received · up 30m0s",
	)
	assertLines(t, got, want)

	got = frameLines(t, func(w *bytes.Buffer) {
		renderMulti(w, newUIColors(), sp, 1, testNow)
	})
	want = append(multiTableLines(),
		"── web · last requests ──",
		"no requests yet",
	)
	assertLines(t, got, want)
}

// TestRenderSingleUnchanged pins the classic single-tunnel dashboard output
// (the N==1 path must stay byte-identical).
func TestRenderSingleUnchanged(t *testing.T) {
	s := ClientSnapshot{
		Name: "api", Protocol: protocol.ProtocolHTTP, Status: "online",
		PublicURL: "http://api.example.com:8080", LocalAddr: "http://127.0.0.1:3000",
		StartTime:     testNow.Add(-1 * time.Hour),
		TotalRequests: 42, BytesSent: 12345, BytesReceived: 67890,
		Requests: []RequestInfo{
			{Timestamp: testNow.Add(-30 * time.Second), Method: "GET", Path: "/v1/chat", Status: 200, Duration: 120 * time.Millisecond},
			{Timestamp: testNow.Add(-10 * time.Second), Method: "POST", Path: "/v1/submit", Status: 500, Duration: 1500 * time.Millisecond},
		},
	}
	got := frameLines(t, func(w *bytes.Buffer) {
		renderSingle(w, newUIColors(), s, testNow)
	})
	spc := func(n int) string { return strings.Repeat(" ", n) }
	want := []string{
		"mygrok",
		"",
		"Session Status" + spc(7) + "online",
		"Version" + spc(14) + version.Version,
		"Region" + spc(15) + "Europe",
		"Forwarding" + spc(11) + "http://api.example.com:8080 -> http://127.0.0.1:3000",
		spc(21) + "https://api.example.com:8080 -> http://127.0.0.1:3000",
		"",
		"Total Requests" + spc(7) + "42",
		"KB Transmitted" + spc(7) + "12.06 KB",
		"KB Received" + spc(10) + "66.30 KB",
		"Uptime" + spc(15) + "1h0m0s",
		"",
		"HTTP Requests",
		"TIME" + spc(17) + "METHOD" + spc(5) + "STATUS" + spc(5) + "PATH",
		strings.Repeat("-", 100),
		"11:59:50.000" + spc(9) + "POST" + spc(7) + "500" + spc(8) + "/v1/submit",
		"11:59:30.000" + spc(9) + "GET" + spc(8) + "200" + spc(8) + "/v1/chat",
	}
	assertLines(t, got, want)
}

func TestUILoopShutdown(t *testing.T) {
	m := &TunnelManager{tunnels: []*managedTunnel{{client: NewTunnelClient(Config{Subdomain: "a", LocalAddr: "127.0.0.1:39999"})}}}
	done := make(chan struct{})
	quit := func() {}
	finished := make(chan struct{})
	go func() {
		m.uiLoop(done, quit)
		close(finished)
	}()
	time.Sleep(100 * time.Millisecond)
	close(done)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("uiLoop did not exit after done was closed")
	}
}

func TestWrapSelection(t *testing.T) {
	cases := []struct {
		n, cur, dir, want int
	}{
		{3, 0, 1, 1},
		{3, 1, 1, 2},
		{3, 2, 1, 0},  // forward wrap
		{3, 0, -1, 2}, // backward wrap
		{3, 1, -1, 0},
		{3, 2, -1, 1},
		{1, 0, 1, 0},
		{1, 0, -1, 0},
		{0, 0, 1, 0},
	}
	for _, tc := range cases {
		if got := wrapSelection(tc.n, tc.cur, tc.dir); got != tc.want {
			t.Errorf("wrapSelection(%d, %d, %d) = %d, want %d", tc.n, tc.cur, tc.dir, got, tc.want)
		}
	}
}

func TestAutoSelect(t *testing.T) {
	online := ClientSnapshot{Name: "a", Status: "online", PublicURL: "http://a"}
	online2 := ClientSnapshot{Name: "b", Status: "online", PublicURL: "http://b"}
	err := ClientSnapshot{Name: "e", Status: "error", ErrorMsg: "boom"}

	// an error wins, even over traffic
	hot := online
	hot.BytesSent, hot.BytesReceived = 1<<30, 1<<30
	if got := autoSelect([]ClientSnapshot{hot, err, online2}); got != 1 {
		t.Errorf("error preference: got %d, want 1", got)
	}

	// then the most recent request wins over the earlier one
	a := ClientSnapshot{Name: "a", Status: "online", PublicURL: "http://a",
		Requests: []RequestInfo{{Timestamp: testNow.Add(-60 * time.Second)}}}
	b := ClientSnapshot{Name: "b", Status: "online", PublicURL: "http://b",
		Requests: []RequestInfo{{Timestamp: testNow.Add(-5 * time.Second)}}}
	if got := autoSelect([]ClientSnapshot{a, b}); got != 1 {
		t.Errorf("most recent request: got %d, want 1", got)
	}

	// then the most traffic
	c := ClientSnapshot{Name: "c", Status: "online", PublicURL: "http://c", BytesSent: 100}
	d := ClientSnapshot{Name: "d", Status: "online", PublicURL: "http://d", BytesSent: 1000}
	if got := autoSelect([]ClientSnapshot{c, d}); got != 1 {
		t.Errorf("traffic preference: got %d, want 1", got)
	}

	// nothing interesting: index 0
	if got := autoSelect([]ClientSnapshot{online, online2}); got != 0 {
		t.Errorf("default: got %d, want 0", got)
	}
	if got := autoSelect(nil); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
}
