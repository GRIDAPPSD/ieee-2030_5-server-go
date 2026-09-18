package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const version = "0.1.0"

func main() {
	// #253: route the stdlib log package through a slog JSON handler
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
	certDir, err := resolveCertDir()
	if err != nil {
		return err
	}
	cfg, err := configFromEnv(certDir)
	if err != nil {
		return err
	}

	// Load CA for admin cert service. Reuses cfg.CAFile rather than
	// re-reading SEP2_CA, since both name the same setting.
	var svc *handler.AdminCertService
	caKeyFile, err := envPathOr("SEP2_CA_KEY", filepath.Join(certDir, "ca.key"))
	if err != nil {
		return err
	}
	caCert, caKey, loadErr := certs.LoadCA(cfg.CAFile, caKeyFile)
	if loadErr != nil {
		log.Printf("CA not loaded (%v): admin cert API disabled", loadErr)
	} else {
		caCertPEM, _ := os.ReadFile(cfg.CAFile)
		svc = handler.NewAdminCertService(caCert, caKey, caCertPEM)
		log.Println("CA loaded: admin cert API enabled")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg, svc)
}

// configFromEnv builds the server configuration from SEP2_* environment
// variables. certDir is the resolved base certificate directory
// (resolveCertDir): CertFile, KeyFile and CAFile default under it unless
// individually overridden. Every setting naming a filesystem path is
// routed through envPathOr or expandCSVPaths so a value given in tilde
// form is expanded the same way a shell would expand it on a command
// line (#598); settings naming a network address, hostname, or token are
// read with the plain envOr/os.Getenv they always used, since a tilde has
// no meaning there.
func configFromEnv(certDir string) (*config.Config, error) {
	certFile, err := envPathOr("SEP2_CERT", filepath.Join(certDir, "server.crt"))
	if err != nil {
		return nil, err
	}
	keyFile, err := envPathOr("SEP2_KEY", filepath.Join(certDir, "server.key"))
	if err != nil {
		return nil, err
	}
	caFile, err := envPathOr("SEP2_CA", filepath.Join(certDir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	extraClientCAs, err := expandCSVPaths(os.Getenv("SEP2_EXTRA_CLIENT_CAS"))
	if err != nil {
		return nil, err
	}
	bootFixtureFile, err := envPathOr("SEP2_BOOT_FIXTURE", "")
	if err != nil {
		return nil, err
	}
	adminCert, err := envPathOr("SEP2_ADMIN_CERT", "")
	if err != nil {
		return nil, err
	}
	adminKeyFile, err := envPathOr("SEP2_ADMIN_KEY_FILE", "")
	if err != nil {
		return nil, err
	}
	dataDir, err := envPathOr("SEP2_DATA_DIR", "")
	if err != nil {
		return nil, err
	}
	subscriptionStorePath, err := envPathOr("SEP2_SUBSCRIPTION_STORE_PATH", "")
	if err != nil {
		return nil, err
	}

	return &config.Config{
		Addr:            envOr("SEP2_ADDR", ":443"),
		CertFile:        certFile,
		KeyFile:         keyFile,
		CAFile:          caFile,
		ExtraClientCAs:  extraClientCAs,
		BootFixtureFile: bootFixtureFile,

		// #161: admin listener configuration. SEP2_ADMIN_LISTEN is the
		// canonical knob; SEP2_ADMIN_ADDR is preserved as a deprecated alias
		// for pre-#161 deployments. SEP2_ADMIN_TLS toggles between plain
		// HTTP (Caddy mode, default) and HTTPS. SEP2_ADMIN_CERT +
		// SEP2_ADMIN_KEY_FILE point at an operator-supplied cert/key; when
		// HTTPS is on and they are empty, a self-signed cert is generated.
		AdminListen:      os.Getenv("SEP2_ADMIN_LISTEN"),
		AdminAddr:        os.Getenv("SEP2_ADMIN_ADDR"),
		AdminKey:         os.Getenv("SEP2_ADMIN_KEY"),
		AdminTLS:         os.Getenv("SEP2_ADMIN_TLS") == "true",
		AdminCert:        adminCert,
		AdminKeyFile:     adminKeyFile,
		AdminBehindProxy: os.Getenv("SEP2_ADMIN_BEHIND_PROXY") == "true",

		// #365: opt-in for an admin bind reachable from outside this host.
		// Without it a non-loopback admin address refuses to start.
		AdminAllowNonLoopback: os.Getenv("SEP2_ADMIN_ALLOW_NON_LOOPBACK") == "true",

		// #270: extra Host-header allowlist entries (defense-in-depth
		// against DNS rebinding). CSV; appended to the static defaults.
		AdminAllowedHosts: parseCSV(os.Getenv("SEP2_ADMIN_ALLOWED_HOSTS")),

		// #364: rollback knob for the dashboard rewrite. True serves the
		// pre-Svelte string-constant page at GET / instead of the SPA.
		AdminLegacyDashboard: os.Getenv("SEP2_ADMIN_LEGACY_DASHBOARD") == "true",

		// #165: single-knob persistence. Empty SEP2_DATA_DIR keeps the
		// historical pure in-memory behavior. SEP2_SUBSCRIPTION_STORE_PATH
		// is preserved for back-compat and wins over the derived datadir
		// path when both are set.
		DataDir:               dataDir,
		SubscriptionStorePath: subscriptionStorePath,

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

		// Refused by default: the admin listener is on loopback. For test
		// harnesses whose notification receivers listen there.
		NotificationAllowLoopback: os.Getenv("SEP2_NOTIFICATION_ALLOW_LOOPBACK") == "true",
	}, nil
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
