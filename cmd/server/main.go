package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"

	"github.com/veloriba/mygrok/internal/server"
)

func main() {
	token := flag.String("token", os.Getenv("MYGROK_TOKEN"), "Authentication token")
	domain := flag.String("domain", "", "Base domain for tunnels (required)")
	controlAddr := flag.String("control", ":7000", "Control server address")
	httpAddr := flag.String("http", ":8080", "HTTP proxy address")
	adminAddr := flag.String("admin", "127.0.0.1:7001", "loopback admin/stats listener address (empty or \"off\" to disable)")
	adminToken := flag.String("admin-token", os.Getenv("MYGROK_ADMIN_TOKEN"), "optional token granting non-loopback access to /stats")
	portBase := flag.Int("port-base", 0, "base port for auto-assigned tcp/udp tunnels (0 = default 20000)")
	portCount := flag.Int("port-count", 0, "size of the tcp/udp auto-assign port range (0 = default 100)")
	logLevel := flag.String("log-level", getenvDefault("MYGROK_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	logFormat := flag.String("log-format", getenvDefault("MYGROK_LOG_FORMAT", "text"), "log format: text|json")
	verbose := flag.Bool("v", false, "verbose (debug) logging; equivalent to -log-level=debug")
	flag.Parse()

	if *verbose {
		*logLevel = "debug"
	}
	setupLogging(*logLevel, *logFormat)

	if *token == "" {
		slog.Error("MYGROK_TOKEN environment variable or -token flag is required")
		os.Exit(1)
	}
	if *domain == "" {
		slog.Error("-domain flag is required")
		os.Exit(1)
	}

	admin := *adminAddr
	if admin == "off" {
		admin = ""
	}

	srv := server.NewTunnelServer(*token, *domain, *controlAddr, *httpAddr, *portBase, *portCount)
	srv.AdminAddr = admin
	srv.AdminToken = *adminToken
	srv.Logger = slog.Default()

	slog.Info("starting mygrok server", "domain", *domain)
	if err := srv.Start(); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
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
