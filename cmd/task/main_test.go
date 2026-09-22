package main

import (
	"reflect"
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
