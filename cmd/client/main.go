package main

import (
	"fmt"
	"log"
	"os"

	"github.com/veloriba/mygrok/internal/client"
	"github.com/spf13/cobra"
)

var (
	serverAddr string
	token      string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "mygrok",
		Short: "mygrok is a minimal ngrok clone",
	}

	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", os.Getenv("MYGROK_SERVER"), "mygrok server address (or set MYGROK_SERVER env var)")
	rootCmd.PersistentFlags().StringVar(&token, "token", os.Getenv("MYGROK_TOKEN"), "authentication token (or set MYGROK_TOKEN env var)")

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

	rootCmd.AddCommand(httpCmd, httpsCmd)

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

	localAddr := fmt.Sprintf("%s://127.0.0.1:%s", scheme, port)
	c := client.NewTunnelClient(serverAddr, token, subdomain, localAddr)
	if err := c.Start(); err != nil {
		log.Fatalf("Client failed: %v", err)
	}
}
