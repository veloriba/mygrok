package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/veloriba/mygrok/internal/client"
	"github.com/veloriba/mygrok/internal/protocol"
)

var (
	serverAddr      string
	token           string
	noTUI           bool
	publicPort      int
	configFile      string
	pidFile         string
	logLevel        string
	logFormat       string
	logVerbose      bool
	flushInterval   string
	localHost       string
	insecure        bool
	setHeaders      []string
	upstreamTimeout time.Duration
)

// TunnelSpec describes one tunnel in a config file's "tunnels" list.
type TunnelSpec struct {
	Name       string   `json:"name"`     // display name; falls back to subdomain, then local address
	Protocol   string   `json:"protocol"` // http|https|tcp|udp; https = http + insecure
	Port       int      `json:"port"`     // local port
	Subdomain  string   `json:"subdomain"`
	PublicPort int      `json:"public_port"` // tcp/udp requested public port (0 = auto-assign)
	Insecure   bool     `json:"insecure"`    // skip TLS verification for the local target
	SetHeaders []string `json:"set_headers"` // headers injected into proxied requests
	LocalHost  string   `json:"local_host"`  // per-tunnel override of --local-host
}

type ProfileConfig struct {
	Server    string       `json:"server"`
	Token     string       `json:"token"`
	Port      int          `json:"port"`
	Subdomain string       `json:"subdomain"`
	Scheme    string       `json:"scheme"`
	Tunnels   []TunnelSpec `json:"tunnels"`
}

func parseConfig(data []byte) (*ProfileConfig, error) {
	var cfg ProfileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadConfig(path string) (*ProfileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseConfig(data)
}

func findBinaryDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// waitForSignal returns a channel closed on SIGINT/SIGTERM.
func waitForSignal() <-chan struct{} {
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(done)
	}()
	return done
}

// resolveConfigPath returns the explicit --config path, or the default
// config.json next to the binary when it exists.
func resolveConfigPath() (string, error) {
	if configFile != "" {
		return configFile, nil
	}
	defaultCfg := filepath.Join(findBinaryDir(), "config.json")
	if _, err := os.Stat(defaultCfg); err == nil {
		return defaultCfg, nil
	}
	return "", fmt.Errorf("no config file: use --config <file> (or config.json next to the binary)")
}

// loadAndBuild runs the full startup validation chain for a config file:
// parse, resolve server/token (flags/env win, config fills the gaps), build
// the tunnel clients, and check the set (duplicate subdomains). Shared by
// the root/up commands, `validate`, `reload`, and the SIGHUP handler, so
// what `validate` accepts is exactly what startup and reload accept.
func loadAndBuild(path string, g globalOpts) (*ProfileConfig, []*client.TunnelClient, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config %s: %w", path, err)
	}
	if g.token == "" && cfg.Token != "" {
		g.token = cfg.Token
	}
	if g.serverAddr == "" && cfg.Server != "" {
		g.serverAddr = cfg.Server
	}
	if g.token == "" {
		return nil, nil, fmt.Errorf("token is required in config or via --token")
	}
	if g.serverAddr == "" {
		return nil, nil, fmt.Errorf("server is required in config or via --server")
	}
	clients, err := selectTunnels(cfg, g)
	if err != nil {
		return nil, nil, err
	}
	if _, err := client.NewManager(clients, true); err != nil {
		return nil, nil, err
	}
	return cfg, clients, nil
}

// watchReload re-reads the config file on SIGHUP and hot-swaps the tunnel
// set (nginx-style): new tunnels start, removed tunnels stop, changed
// tunnels restart, unchanged tunnels keep their live connections. An invalid
// config is reported and changes nothing -- the running tunnels keep
// serving. Logging is --no-tui only: stderr writes would corrupt the
// dashboard, which shows the new tunnel set on the next frame anyway.
func watchReload(m *client.TunnelManager, path string, g globalOpts) {
	ch := make(chan os.Signal, 1)
	notifyReload(ch)
	go func() {
		for range ch {
			if path == "" {
				if g.noTUI {
					slog.Warn("reload ignored: process was started without a config file")
				}
				continue
			}
			_, clients, err := loadAndBuild(path, g)
			if err != nil {
				if g.noTUI {
					slog.Error("reload skipped: invalid config", "path", path, "err", err.Error())
				}
				continue
			}
			rep, err := m.Reload(clients)
			if err != nil {
				if g.noTUI {
					slog.Error("reload failed", "err", err.Error())
				}
				continue
			}
			if g.noTUI {
				slog.Info("reload applied", "added", rep.Added, "removed", rep.Removed, "replaced", rep.Replaced, "kept", rep.Kept)
			}
		}
	}()
}

// writePidfileAndRun records the PID (so `mygrok reload` can find this
// process), removes the pidfile on exit, and runs the manager.
func writePidfileAndRun(m *client.TunnelManager, path string, g globalOpts) {
	watchReload(m, path, g)
	if err := writePidFile(pidFile); err != nil {
		slog.Warn("could not write pidfile", "path", pidFile, "err", err.Error(), "hint", "trigger reload with `docker kill --signal=HUP <container>` or pick a writable --pid-file")
	} else {
		defer removePidFile(pidFile)
	}
	m.Start(waitForSignal())
}

// globalOpts are the per-run defaults shared by all tunnels; per-tunnel spec
// fields override the matching value.
type globalOpts struct {
	serverAddr      string
	token           string
	localHost       string
	insecure        bool
	setHeaders      []string
	flushInterval   time.Duration
	upstreamTimeout time.Duration
	publicPort      int
	noTUI           bool
}

func runOpts(flushInterval time.Duration) globalOpts {
	return globalOpts{
		serverAddr:      serverAddr,
		token:           token,
		localHost:       localHost,
		insecure:        insecure,
		setHeaders:      setHeaders,
		flushInterval:   flushInterval,
		upstreamTimeout: upstreamTimeout,
		publicPort:      publicPort,
		noTUI:           noTUI,
	}
}

// newTunnelClient fills the per-tunnel display name and logger so log lines
// from every tunnel carry a "tunnel=" attribute.
func newTunnelClient(cfg client.Config) *client.TunnelClient {
	name := cfg.Name
	if name == "" {
		name = cfg.Subdomain
	}
	if name == "" {
		name = cfg.LocalAddr
	}
	cfg.Name = name
	cfg.Logger = slog.Default().With("tunnel", name)
	return client.NewTunnelClient(cfg)
}

// legacyTunnelClient builds the single-tunnel config from the legacy profile
// shape (top-level port/subdomain/scheme fields).
func legacyTunnelClient(cfg *ProfileConfig, g globalOpts) *client.TunnelClient {
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return newTunnelClient(client.Config{
		ServerAddr:      g.serverAddr,
		Token:           g.token,
		Subdomain:       cfg.Subdomain,
		LocalAddr:       fmt.Sprintf("%s://%s:%d", scheme, g.localHost, cfg.Port),
		Protocol:        protocol.ProtocolHTTP,
		NoTUI:           g.noTUI,
		FlushInterval:   g.flushInterval,
		Insecure:        g.insecure,
		SetHeaders:      g.setHeaders,
		UpstreamTimeout: g.upstreamTimeout,
	})
}

// selectTunnels builds the clients for a profile: a non-empty "tunnels" list
// wins over the legacy single-tunnel fields.
func selectTunnels(cfg *ProfileConfig, g globalOpts) ([]*client.TunnelClient, error) {
	if len(cfg.Tunnels) > 0 {
		return buildClients(cfg.Tunnels, g)
	}
	return []*client.TunnelClient{legacyTunnelClient(cfg, g)}, nil
}

func buildClients(specs []TunnelSpec, g globalOpts) ([]*client.TunnelClient, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no tunnels specified")
	}
	clients := make([]*client.TunnelClient, 0, len(specs))
	for i, spec := range specs {
		c, err := clientFromSpec(spec, g)
		if err != nil {
			return nil, fmt.Errorf("tunnel %d: %w", i+1, err)
		}
		clients = append(clients, c)
	}
	return clients, nil
}

func clientFromSpec(spec TunnelSpec, g globalOpts) (*client.TunnelClient, error) {
	proto := spec.Protocol
	if proto == "" {
		proto = protocol.ProtocolHTTP
	}
	if spec.Port <= 0 {
		return nil, fmt.Errorf("port is required")
	}
	localHost := spec.LocalHost
	if localHost == "" {
		localHost = g.localHost
	}
	setHeaders := spec.SetHeaders
	if len(setHeaders) == 0 {
		setHeaders = g.setHeaders
	}
	publicPort := g.publicPort
	if spec.PublicPort != 0 {
		publicPort = spec.PublicPort
	}
	insecure := g.insecure || spec.Insecure
	localAddr := fmt.Sprintf("%s:%d", localHost, spec.Port)
	switch proto {
	case protocol.ProtocolHTTP:
		localAddr = "http://" + localAddr
	case "https":
		localAddr = "https://" + localAddr
		proto = protocol.ProtocolHTTP
		insecure = true
	case protocol.ProtocolTCP, protocol.ProtocolUDP:
		// raw "host:port"
	default:
		return nil, fmt.Errorf("unknown protocol %q (want http, https, tcp, or udp)", spec.Protocol)
	}
	return newTunnelClient(client.Config{
		ServerAddr:      g.serverAddr,
		Token:           g.token,
		Subdomain:       spec.Subdomain,
		LocalAddr:       localAddr,
		Protocol:        proto,
		RequestedPort:   publicPort,
		NoTUI:           g.noTUI,
		FlushInterval:   g.flushInterval,
		Insecure:        insecure,
		SetHeaders:      setHeaders,
		UpstreamTimeout: g.upstreamTimeout,
		Name:            spec.Name,
	}), nil
}

// parseTriples parses positional args as repeated (protocol, port, subdomain)
// triples.
func parseTriples(args []string) ([]TunnelSpec, error) {
	if len(args)%3 != 0 {
		return nil, fmt.Errorf("expected <protocol> <port> <subdomain> triples, got %d arguments", len(args))
	}
	specs := make([]TunnelSpec, 0, len(args)/3)
	for i := 0; i < len(args); i += 3 {
		proto := args[i]
		switch proto {
		case "http", "https", protocol.ProtocolTCP, protocol.ProtocolUDP:
		default:
			return nil, fmt.Errorf("unknown protocol %q (want http, https, tcp, or udp)", proto)
		}
		port, err := strconv.Atoi(args[i+1])
		if err != nil || port <= 0 {
			return nil, fmt.Errorf("invalid port %q for %s tunnel", args[i+1], proto)
		}
		specs = append(specs, TunnelSpec{Protocol: proto, Port: port, Subdomain: args[i+2]})
	}
	return specs, nil
}

func startSingle(c *client.TunnelClient) {
	m, err := client.NewManager([]*client.TunnelClient{c}, c.NoTUI)
	if err != nil {
		log.Fatalf("Client failed: %v", err)
	}
	m.Start(waitForSignal())
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "mygrok",
		Short: "mygrok is a minimal ngrok clone",
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile == "" {
				defaultCfg := filepath.Join(findBinaryDir(), "config.json")
				if _, err := os.Stat(defaultCfg); err == nil {
					configFile = defaultCfg
				}
			}
			if configFile == "" {
				return cmd.Usage()
			}

			flushIntervalDur, err := parseFlushInterval(flushInterval)
			if err != nil {
				return fmt.Errorf("invalid --flush-interval %q: %w", flushInterval, err)
			}
			g := runOpts(flushIntervalDur)

			cfg, clients, err := loadAndBuild(configFile, g)
			if err != nil {
				return err
			}
			if len(cfg.Tunnels) == 0 && noTUI {
				scheme := cfg.Scheme
				if scheme == "" {
					scheme = "http"
				}
				localAddr := fmt.Sprintf("%s://%s:%d", scheme, localHost, cfg.Port)
				host, _, _ := net.SplitHostPort(g.serverAddr)
				publicURL := fmt.Sprintf("%s.%s:%d", cfg.Subdomain, host, 443)
				slog.Info("starting tunnel", "url", publicURL, "local", localAddr)
			}

			m, err := client.NewManager(clients, noTUI)
			if err != nil {
				return err
			}
			writePidfileAndRun(m, configFile, g)
			return nil
		},
	}
	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", os.Getenv("MYGROK_SERVER"), "mygrok server address (overrides config.json)")
	rootCmd.PersistentFlags().StringVar(&token, "token", os.Getenv("MYGROK_TOKEN"), "authentication token (overrides config.json)")
	rootCmd.PersistentFlags().BoolVarP(&noTUI, "no-tui", "", false, "run without TUI dashboard")
	rootCmd.PersistentFlags().IntVar(&publicPort, "public-port", 0, "requested public port for tcp/udp tunnels (0 = auto-assign)")
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "f", "", "path to profile config file (default: config.json next to binary)")
	rootCmd.PersistentFlags().StringVar(&pidFile, "pid-file", filepath.Join(os.TempDir(), "mygrok.pid"), "pid file written by the running process (empty disables), read by the reload command")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", getenvDefault("MYGROK_LOG_LEVEL", "info"), "log level: debug|info|warn|error (service mode)")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", getenvDefault("MYGROK_LOG_FORMAT", "text"), "log format: text|json (service mode)")
	rootCmd.PersistentFlags().BoolVarP(&logVerbose, "verbose", "v", false, "verbose (debug) logging; equivalent to --log-level=debug")

	rootCmd.PersistentFlags().StringVar(&flushInterval, "flush-interval", getenvDefault("MYGROK_FLUSH_INTERVAL", "-1"), "flush interval for proxied responses (-1 = after every write)")
	rootCmd.PersistentFlags().StringVar(&localHost, "local-host", getenvDefault("MYGROK_LOCAL_HOST", "127.0.0.1"), "local host to forward to")
	rootCmd.PersistentFlags().BoolVar(&insecure, "insecure", getenvBoolDefault("MYGROK_INSECURE", false), "skip TLS certificate verification for the local target")
	rootCmd.PersistentFlags().StringArrayVar(&setHeaders, "set-header", nil, "header to inject into proxied requests, repeatable (e.g. \"Authorization: Bearer x\")")
	upstreamTimeoutDefault, err := getenvDurationDefault("MYGROK_UPSTREAM_TIMEOUT", 0)
	if err != nil {
		log.Printf("MYGROK_UPSTREAM_TIMEOUT: %v (using default)", err)
		upstreamTimeoutDefault = 0
	}
	rootCmd.PersistentFlags().DurationVar(&upstreamTimeout, "upstream-timeout", upstreamTimeoutDefault, "timeout for upstream response headers (0 = no timeout)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if logVerbose {
			logLevel = "debug"
		}
		setupLogging(logLevel, logFormat)
		return nil
	}

	// HTTP command
	httpCmd := &cobra.Command{
		Use:   "http [port] [subdomain]",
		Short: "Expose a local HTTP service",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runProxy("http", args)
		},
	}

	// HTTPS command (for local services that already use HTTPS)
	httpsCmd := &cobra.Command{
		Use:   "https [port] [subdomain]",
		Short: "Expose a local HTTPS service",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runProxy("https", args)
		},
	}

	// TCP command (raw TCP port forwarding, e.g. SSH)
	tcpCmd := &cobra.Command{
		Use:   "tcp [local-port] [subdomain]",
		Short: "Expose a local TCP port (e.g. SSH)",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runRaw(protocol.ProtocolTCP, args)
		},
	}

	// UDP command (raw UDP port forwarding)
	udpCmd := &cobra.Command{
		Use:   "udp [local-port] [subdomain]",
		Short: "Expose a local UDP port",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runRaw(protocol.ProtocolUDP, args)
		},
	}

	// Up command: run several tunnels in one process, either from a config
	// file ("tunnels" list or legacy single shape) or from positional triples.
	upCmd := &cobra.Command{
		Use:   "up [protocol port subdomain]...",
		Short: "Run one or more tunnels in a single process",
		Long: `Run tunnels from a config file (default: config.json next to the binary) or
from positional <protocol> <port> <subdomain> triples:

  mygrok up http 3000 api tcp 22 ssh`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile != "" && len(args) > 0 {
				return fmt.Errorf("cannot combine --config with positional tunnel triples")
			}
			if configFile == "" && len(args) == 0 {
				if p, err := resolveConfigPath(); err == nil {
					configFile = p
				}
			}

			flushIntervalDur, err := parseFlushInterval(flushInterval)
			if err != nil {
				return fmt.Errorf("invalid --flush-interval %q: %w", flushInterval, err)
			}
			g := runOpts(flushIntervalDur)

			var clients []*client.TunnelClient
			switch {
			case configFile != "":
				_, clients, err = loadAndBuild(configFile, g)
				if err != nil {
					return err
				}
			case len(args) > 0:
				if token == "" {
					return fmt.Errorf("token is required in config or via --token")
				}
				if serverAddr == "" {
					return fmt.Errorf("server is required in config or via --server")
				}
				specs, perr := parseTriples(args)
				if perr != nil {
					return perr
				}
				clients, err = buildClients(specs, g)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("no tunnels specified: use --config <file> or <protocol> <port> <subdomain> triples")
			}

			m, err := client.NewManager(clients, noTUI)
			if err != nil {
				return err
			}
			writePidfileAndRun(m, configFile, g)
			return nil
		},
	}

	// Validate command: check a config file without starting anything.
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file without connecting",
		Long: `Parse and check the config file (tunnel specs, protocols, ports,
duplicate subdomains, server/token resolution) without starting tunnels
or contacting the server. Exits non-zero when the config is invalid.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveConfigPath()
			if err != nil {
				return err
			}
			flushIntervalDur, err := parseFlushInterval(flushInterval)
			if err != nil {
				return fmt.Errorf("invalid --flush-interval %q: %w", flushInterval, err)
			}
			_, clients, err := loadAndBuild(path, runOpts(flushIntervalDur))
			if err != nil {
				return fmt.Errorf("config %s is invalid: %w", path, err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s is valid: %d tunnel(s)\n", path, len(clients))
			for _, c := range clients {
				fmt.Fprintf(out, "  %-16s %-4s subdomain=%-12s -> %s\n", c.Name, c.Protocol, c.Subdomain, c.LocalAddr)
			}
			return nil
		},
	}

	// Reload command: validate, then signal the running process (SIGHUP).
	reloadCmd := &cobra.Command{
		Use:   "reload",
		Short: "Hot-reload the tunnels of a running process",
		Long: `Validate the config file, then send SIGHUP to the running process found
via --pid-file. The process re-reads its own config and applies the diff:
new tunnels start, removed tunnels stop, changed tunnels restart, and
unchanged tunnels keep serving without dropping connections. An invalid
config aborts the reload; the running tunnels keep serving.

In Docker prefer: docker kill --signal=HUP <container>`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if path, err := resolveConfigPath(); err == nil {
				flushIntervalDur, ferr := parseFlushInterval(flushInterval)
				if ferr != nil {
					return fmt.Errorf("invalid --flush-interval %q: %w", flushInterval, ferr)
				}
				if _, _, verr := loadAndBuild(path, runOpts(flushIntervalDur)); verr != nil {
					return fmt.Errorf("config %s is invalid, reload aborted: %w", path, verr)
				}
				fmt.Fprintf(out, "config %s is valid\n", path)
			} else {
				fmt.Fprintf(out, "no config file to validate (%v); the running process re-validates its own config on reload\n", err)
			}
			pid, err := readPidFile(pidFile)
			if err != nil {
				return fmt.Errorf("%w (is the client running with --pid-file %s? in Docker use `docker kill --signal=HUP <container>`)", err, pidFile)
			}
			if err := sendReloadSignal(pid); err != nil {
				return err
			}
			fmt.Fprintf(out, "reload signal sent to pid %d\n", pid)
			return nil
		},
	}

	rootCmd.AddCommand(httpCmd, httpsCmd, tcpCmd, udpCmd, upCmd, validateCmd, reloadCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runProxy(scheme string, args []string) {
	port := args[0]
	subdomain := ""
	if len(args) > 1 {
		subdomain = args[1]
	}

	if token == "" {
		log.Fatal("Token is required. Set MYGROK_TOKEN or use --token")
	}
	if serverAddr == "" {
		log.Fatal("Server address is required. Use --server (e.g. --server yourdomain.com:7000)")
	}

	flushIntervalDur, err := parseFlushInterval(flushInterval)
	if err != nil {
		log.Fatalf("invalid --flush-interval %q: %v", flushInterval, err)
	}

	localAddr := fmt.Sprintf("%s://%s:%s", scheme, localHost, port)
	c := newTunnelClient(client.Config{
		ServerAddr:      serverAddr,
		Token:           token,
		Subdomain:       subdomain,
		LocalAddr:       localAddr,
		Protocol:        protocol.ProtocolHTTP,
		NoTUI:           noTUI,
		FlushInterval:   flushIntervalDur,
		Insecure:        insecure,
		SetHeaders:      setHeaders,
		UpstreamTimeout: upstreamTimeout,
	})
	startSingle(c)
}

func runRaw(proto string, args []string) {
	port := args[0]
	subdomain := ""
	if len(args) > 1 {
		subdomain = args[1]
	}

	if token == "" {
		log.Fatal("Token is required. Set MYGROK_TOKEN or use --token")
	}
	if serverAddr == "" {
		log.Fatal("Server address is required. Use --server (e.g. --server yourdomain.com:7000)")
	}

	localAddr := fmt.Sprintf("%s:%s", localHost, port)
	c := newTunnelClient(client.Config{
		ServerAddr:    serverAddr,
		Token:         token,
		Subdomain:     subdomain,
		LocalAddr:     localAddr,
		Protocol:      proto,
		RequestedPort: publicPort,
		NoTUI:         noTUI,
	})
	startSingle(c)
}

func setupLogging(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBoolDefault(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return def
}

func getenvDurationDefault(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	return time.ParseDuration(v)
}

// parseFlushInterval converts the --flush-interval flag value to a duration.
// The sentinel "-1" means flush after every write.
func parseFlushInterval(s string) (time.Duration, error) {
	if s == "-1" {
		return time.Duration(-1), nil
	}
	return time.ParseDuration(s)
}
