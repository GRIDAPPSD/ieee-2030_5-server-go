package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/server"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("sep2server %s\n", version)
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "certs":
		if err := runCerts(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(1)
	}
}

func runServe() error {
	cfg := &config.Config{
		Addr:        envOr("SEP2_ADDR", ":443"),
		CertFile:    envOr("SEP2_CERT", "certs/server.crt"),
		KeyFile:     envOr("SEP2_KEY", "certs/server.key"),
		CAFile:      envOr("SEP2_CA", "certs/ca.crt"),
		TZOffset:    -28800,
		TimeQuality: 7,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "IEEE 2030.5 Server v%s\n\n", version)
	fmt.Fprintln(os.Stderr, "Usage: sep2server <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  serve    Start the IEEE 2030.5 server")
	fmt.Fprintln(os.Stderr, "  certs    Certificate management")
	fmt.Fprintln(os.Stderr, "  version  Print version")
}
