package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const version = "0.1.0"

func main() {
	// IEEE-172: route the stdlib log package through a slog JSON handler
	// (stdout) so container logs are structured for the observability stack.
	setupLogging(os.Stdout)

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

		// IEEE-094: admin listener configuration. SEP2_ADMIN_LISTEN is the
		// canonical knob; SEP2_ADMIN_ADDR is preserved as a deprecated alias
		// for pre-IEEE-094 deployments. SEP2_ADMIN_TLS toggles between plain
		// HTTP (Caddy mode, default) and HTTPS. SEP2_ADMIN_CERT +
		// SEP2_ADMIN_KEY_FILE point at an operator-supplied cert/key; when
		// HTTPS is on and they are empty, a self-signed cert is generated.
		AdminListen:      os.Getenv("SEP2_ADMIN_LISTEN"),
		AdminAddr:        os.Getenv("SEP2_ADMIN_ADDR"),
		AdminKey:         os.Getenv("SEP2_ADMIN_KEY"),
		AdminTLS:         os.Getenv("SEP2_ADMIN_TLS") == "true",
		AdminCert:        os.Getenv("SEP2_ADMIN_CERT"),
		AdminKeyFile:     os.Getenv("SEP2_ADMIN_KEY_FILE"),
		AdminBehindProxy: os.Getenv("SEP2_ADMIN_BEHIND_PROXY") == "true",

		// IEEE-138: extra Host-header allowlist entries (defense-in-depth
		// against DNS rebinding). CSV; appended to the static defaults.
		AdminAllowedHosts: parseCSV(os.Getenv("SEP2_ADMIN_ALLOWED_HOSTS")),

		// IEEE-097: single-knob persistence. Empty SEP2_DATA_DIR keeps the
		// historical pure in-memory behavior. SEP2_SUBSCRIPTION_STORE_PATH
		// is preserved for back-compat and wins over the derived datadir
		// path when both are set.
		DataDir:               os.Getenv("SEP2_DATA_DIR"),
		SubscriptionStorePath: os.Getenv("SEP2_SUBSCRIPTION_STORE_PATH"),

		// Observability: dedicated plain-HTTP Prometheus metrics listener.
		// Empty SEP2_METRICS_ADDR (default) leaves it OFF; set e.g. ":9100"
		// to expose GET /metrics on a separate port. Never mounted on the
		// protocol or admin listeners.
		MetricsAddr: os.Getenv("SEP2_METRICS_ADDR"),

		TZOffset:    -28800,
		TimeQuality: 7,
		EnableCCM:   os.Getenv("SEP2_CCM") == "true",
		EnableMDNS:  os.Getenv("SEP2_MDNS") == "true",
		MDNSHost:    envOr("SEP2_MDNS_HOST", "localhost"),
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
