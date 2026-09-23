package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
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
	resolver := &certDirResolver{}
	cfg, err := configFromEnv(resolver)
	if err != nil {
		return err
	}

	// #622: load both CA pairs for the admin cert service. SEP2_CA_KEY
	// stays the shared default (mirrors CAFile): SEP2_SERVING_CA_KEY and
	// SEP2_DEVICE_CA_KEY fall back to it, exactly as ServingCAFile/
	// DeviceCAFile fall back to CAFile, so an unsplit deployment resolves
	// the one key path twice rather than needing a second setting.
	caKeyFile, err := resolver.envPathOrCertDir("SEP2_CA_KEY", "ca.key")
	if err != nil {
		return err
	}
	servingCAKeyFile, err := envPathOr("SEP2_SERVING_CA_KEY", caKeyFile)
	if err != nil {
		return err
	}
	deviceCAKeyFile, err := envPathOr("SEP2_DEVICE_CA_KEY", caKeyFile)
	if err != nil {
		return err
	}

	var (
		servingCert *x509.Certificate
		servingKey  *ecdsa.PrivateKey
		servingPEM  []byte
		deviceCert  *x509.Certificate
		deviceKey   *ecdsa.PrivateKey
	)
	if c, k, loadErr := certs.LoadCA(cfg.EffectiveServingCA(), servingCAKeyFile); loadErr != nil {
		log.Printf("serving CA not loaded (%v): server-cert minting and CA download disabled", loadErr)
	} else {
		servingCert, servingKey = c, k
		servingPEM, _ = os.ReadFile(cfg.EffectiveServingCA())
	}
	if c, k, loadErr := certs.LoadCA(cfg.EffectiveDeviceCA(), deviceCAKeyFile); loadErr != nil {
		log.Printf("device CA not loaded (%v): device-cert minting disabled", loadErr)
	} else {
		deviceCert, deviceKey = c, k
	}

	// svc is constructed whenever EITHER CA loaded: each handler checks
	// its own pair (internal/handler/admin_certs.go), so a deployment
	// missing one CA still serves the routes the other CA covers.
	var svc *handler.AdminCertService
	if servingCert != nil || deviceCert != nil {
		svc = handler.NewAdminCertServiceWithCAs(servingCert, servingKey, servingPEM, deviceCert, deviceKey)
		switch {
		case servingCert != nil && deviceCert != nil:
			log.Println("CA(s) loaded: admin cert API enabled")
		case servingCert != nil:
			log.Println("serving CA loaded: admin cert API partially enabled (device-cert minting stays disabled)")
		default:
			log.Println("device CA loaded: admin cert API partially enabled (server-cert minting and CA download stay disabled)")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg, svc)
}

// configFromEnv builds the server configuration from SEP2_* environment
// variables. r is the default certificate directory resolver
// (certDirResolver): CertFile, KeyFile and CAFile default under it unless
// individually overridden, and the directory itself (which needs a
// determinable $HOME when SEP2_CERT_DIR is unset) is resolved only if one
// of them actually falls back to it (#601 review finding 2). Every setting
// naming a filesystem path is routed through envPathOr, envPathOrCertDir or
// expandCSVPaths so a value given in tilde form is expanded the same way a
// shell would expand it on a command line (#598); settings naming a
// network address, hostname, or token are read with the plain
// envOr/os.Getenv they always used, since a tilde has no meaning there.
func configFromEnv(r *certDirResolver) (*config.Config, error) {
	certFile, err := r.envPathOrCertDir("SEP2_CERT", "server.crt")
	if err != nil {
		return nil, err
	}
	keyFile, err := r.envPathOrCertDir("SEP2_KEY", "server.key")
	if err != nil {
		return nil, err
	}
	caFile, err := r.envPathOrCertDir("SEP2_CA", "ca.crt")
	if err != nil {
		return nil, err
	}
	// #622: SEP2_SERVING_CA / SEP2_DEVICE_CA are read raw (envPathOr, not
	// envPathOrCertDir) and left empty when unset, so Config.EffectiveServingCA
	// / EffectiveDeviceCA fall back to the just-resolved caFile rather than to
	// the certDirResolver's own "ca.crt" default. Falling back through
	// envPathOrCertDir here would ignore an explicit SEP2_CA in favor of the
	// cert-dir default, breaking the "unsplit deployment reads what it read
	// before" invariant the moment an operator set SEP2_CA to a custom path.
	servingCAFile, err := envPathOr("SEP2_SERVING_CA", "")
	if err != nil {
		return nil, err
	}
	deviceCAFile, err := envPathOr("SEP2_DEVICE_CA", "")
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
	trafficDir, err := envPathOr("SEP2_TRAFFIC_DIR", "")
	if err != nil {
		return nil, err
	}

	return &config.Config{
		Addr:            envOr("SEP2_ADDR", ":443"),
		CertFile:        certFile,
		KeyFile:         keyFile,
		CAFile:          caFile,
		ServingCAFile:   servingCAFile,
		DeviceCAFile:    deviceCAFile,
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

		// #628 fix round 1: SEP2_TRAFFIC_CAPTURE is the permit; off by
		// default. SEP2_TRAFFIC_DIR only decides where segments go once
		// permitted (Config.EffectiveTrafficDir). Fix round 2:
		// TrafficCaptureEnv carries the raw value too, so the disabled boot
		// line can name a rejected value instead of always saying unset.
		TrafficCapture:    os.Getenv("SEP2_TRAFFIC_CAPTURE") == "true",
		TrafficCaptureEnv: os.Getenv("SEP2_TRAFFIC_CAPTURE"),
		TrafficDir:        trafficDir,

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
