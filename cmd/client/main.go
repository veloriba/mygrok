package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	logLevel        string
	logFormat       string
	logVerbose      bool
	flushInterval   string
	localHost       string
	insecure        bool
	setHeaders      []string
	upstreamTimeout time.Duration
)

type ProfileConfig struct {
	Server    string `json:"server"`
	Token     string `json:"token"`
	Port      int    `json:"port"`
	Subdomain string `json:"subdomain"`
	Scheme    string `json:"scheme"`
}

func loadConfig(path string) (*ProfileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProfileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func findBinaryDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "mygrok",
		Short: "mygrok is a minimal ngrok clone",
		RunE: func(cmd *cobra.Command, args []string) error {
			binDir := findBinaryDir()

			if configFile == "" {
				defaultCfg := filepath.Join(binDir, "config.json")
				if _, err := os.Stat(defaultCfg); err == nil {
					configFile = defaultCfg
				}
			}

			var cfg *ProfileConfig
			if configFile != "" {
				c, err := loadConfig(configFile)
				if err != nil {
					return fmt.Errorf("failed to read config %s: %w", configFile, err)
				}
				cfg = c
			}

			if cfg == nil {
				return cmd.Usage()
			}

			scheme := cfg.Scheme
			if scheme == "" {
				scheme = "http"
			}

			if token == "" && cfg.Token != "" {
				token = cfg.Token
			}
			if serverAddr == "" && cfg.Server != "" {
				serverAddr = cfg.Server
			}

			subdomain := cfg.Subdomain

			if token == "" {
				return fmt.Errorf("token is required in config or via --token")
			}
			if serverAddr == "" {
				return fmt.Errorf("server is required in config or via --server")
			}

			flushIntervalDur, err := parseFlushInterval(flushInterval)
			if err != nil {
				return fmt.Errorf("invalid --flush-interval %q: %w", flushInterval, err)
			}

			localAddr := fmt.Sprintf("%s://%s:%d", scheme, localHost, cfg.Port)
			c := client.NewTunnelClient(client.Config{
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
			host, _, _ := net.SplitHostPort(serverAddr)
			publicURL := fmt.Sprintf("%s.%s:%d", subdomain, host, 443)
			if noTUI {
				slog.Info("starting tunnel", "url", publicURL, "local", localAddr)
			}
			return c.Start()
		},
	}
	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", os.Getenv("MYGROK_SERVER"), "mygrok server address (overrides config.json)")
	rootCmd.PersistentFlags().StringVar(&token, "token", os.Getenv("MYGROK_TOKEN"), "authentication token (overrides config.json)")
	rootCmd.PersistentFlags().BoolVarP(&noTUI, "no-tui", "", false, "run without TUI dashboard")
	rootCmd.PersistentFlags().IntVar(&publicPort, "public-port", 0, "requested public port for tcp/udp tunnels (0 = auto-assign)")
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "path to profile config file (default: config.json next to binary)")
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

	rootCmd.AddCommand(httpCmd, httpsCmd, tcpCmd, udpCmd)

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
	c := client.NewTunnelClient(client.Config{
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
	if err := c.Start(); err != nil {
		log.Fatalf("Client failed: %v", err)
	}
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
	c := client.NewTunnelClient(client.Config{
		ServerAddr:    serverAddr,
		Token:         token,
		Subdomain:     subdomain,
		LocalAddr:     localAddr,
		Protocol:      proto,
		RequestedPort: publicPort,
		NoTUI:         noTUI,
	})
	if err := c.Start(); err != nil {
		log.Fatalf("Client failed: %v", err)
	}
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
