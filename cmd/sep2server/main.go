package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
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
		Addr:            envOr("SEP2_ADDR", ":443"),
		CertFile:        envOr("SEP2_CERT", "certs/server.crt"),
		KeyFile:         envOr("SEP2_KEY", "certs/server.key"),
		CAFile:          envOr("SEP2_CA", "certs/ca.crt"),
		ExtraClientCAs:  parseCSV(os.Getenv("SEP2_EXTRA_CLIENT_CAS")),
		BootFixtureFile: os.Getenv("SEP2_BOOT_FIXTURE"),
		AdminAddr:       os.Getenv("SEP2_ADMIN_ADDR"),
		AdminKey:        os.Getenv("SEP2_ADMIN_KEY"),
		TZOffset:        -28800,
		TimeQuality:     7,
		EnableCCM:       os.Getenv("SEP2_CCM") == "true",
		EnableMDNS:      os.Getenv("SEP2_MDNS") == "true",
		MDNSHost:        envOr("SEP2_MDNS_HOST", "localhost"),

		// IEEE-077: opt-in subscription persistence. Empty = in-memory only.
		SubscriptionStorePath: os.Getenv("SEP2_SUBSCRIPTION_STORE_PATH"),
	}

	// Load CA for admin cert service
	var svc *handler.AdminCertService
	caFile := envOr("SEP2_CA", "certs/ca.crt")
	caKeyFile := envOr("SEP2_CA_KEY", "certs/ca.key")
	caCert, caKey, err := certs.LoadCA(caFile, caKeyFile)
	if err != nil {
		log.Printf("CA not loaded (%v) — admin cert API disabled", err)
	} else {
		caCertPEM, _ := os.ReadFile(caFile)
		svc = handler.NewAdminCertService(caCert, caKey, caCertPEM)
		log.Println("CA loaded — admin cert API enabled")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg, svc)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseCSV splits a comma-separated env value into trimmed, non-empty
// entries. Returns nil for an empty input (no-op cascade through the
// TLS-config layer). Used by SEP2_EXTRA_CLIENT_CAS so callers can list
// any number of additional client-CA PEM files on a single env var.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
