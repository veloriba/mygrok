package main

import (
	"flag"
	"log"
	"os"

	"github.com/veloriba/mygrok/internal/server"
)

func main() {
	token := flag.String("token", os.Getenv("MYGROK_TOKEN"), "Authentication token")
	domain := flag.String("domain", "", "Base domain for tunnels (required)")
	controlAddr := flag.String("control", ":7000", "Control server address")
	httpAddr := flag.String("http", ":8080", "HTTP proxy address")
	flag.Parse()

	if *token == "" {
		log.Fatal("MYGROK_TOKEN environment variable or -token flag is required")
	}
	if *domain == "" {
		log.Fatal("-domain flag is required")
	}

	srv := server.NewTunnelServer(*token, *domain, *controlAddr, *httpAddr)
	log.Printf("Starting mygrok server for domain %s", *domain)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
