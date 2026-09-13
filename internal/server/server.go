package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/discovery"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/obs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	subscriptionWorkers   = 4
	subscriptionQueueSize = 256

	// HTTP server timeout defaults for the listeners this package still owns:
	// the admin listener and the metrics listener. Zero means "no limit";
	// these non-zero values defend against Slowloris and slow-body exhaustion.
	// ReadHeaderTimeout < ReadTimeout: header parsing has a tighter deadline
	// than the full body read.
	//
	// The protocol listener moved to pkg/sep2server and takes the same
	// four durations from core's sep2srv.Default*Timeout, which core
	// lifted from THIS block verbatim. The values are identical today; they
	// are named in two places because the two listeners now live in two
	// packages, and a future change to one is a deliberate choice about that
	// listener rather than an accidental change to both.
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
)

// newAdminServer constructs the admin http.Server with the standard timeout
// values. handler may be nil; callers assign Server.Handler after building
// the admin router.
func newAdminServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}

// Run starts the IEEE 2030.5 server with mutual TLS and optionally
// an admin HTTPS server on a separate port.
//
// The protocol half (listener, mutual TLS, server-identity derivation,
// the assembled routes and the graceful drain) is now the embeddable
// surface in pkg/sep2server, and this function is its first consumer.
// What remains here is what an embedder does NOT get: the admin
// listener, the dashboard, the metrics listener, mDNS and the operator banner.
//
// One consequence of that split is visible in the ordering below: the stores
// and the notifier are now built BEFORE the listener is bound, because the
// surface takes them as construction inputs. A deployment whose store path and
// whose bind address are BOTH bad now reports the store path first. Nothing
// else about the sequence changed, and no port is held while a store fails.
func Run(ctx context.Context, cfg *config.Config, svc *handler.AdminCertService) error {
	// #165: build the admin-mutated stores honoring SEP2_DATA_DIR.
	// Empty DataDir + empty per-store dedicated paths = pure in-memory
	// (back-compat). The constructors return non-persistent stores in
	// that case, so every existing test path keeps the same semantics.
	endDevices, err := memory.NewEndDeviceStoreWithPersistence(
		cfg.EffectiveStorePath("enddevices", ""),
	)
	if err != nil {
		return fmt.Errorf("EndDevice persistence: %w", err)
	}
	registrations, err := memory.NewRegistrationStoreWithPersistence(
		cfg.EffectiveStorePath("registrations", ""),
	)
	if err != nil {
		return fmt.Errorf("Registration persistence: %w", err)
	}
	adminFSAs, err := memory.NewAdminFSAStoreWithPersistence(
		cfg.EffectiveStorePath("fsas", ""),
	)
	if err != nil {
		return fmt.Errorf("AdminFSA persistence: %w", err)
	}
	derPrograms, err := memory.NewDERProgramStoreWithPersistence(
		cfg.EffectiveStorePath("derprograms", ""),
	)
	if err != nil {
		return fmt.Errorf("DERProgram persistence: %w", err)
	}

	// #224: build the subscription store with optional durable
	// persistence. #171 routes the path through
	// cfg.EffectiveStorePath so the precedence is:
	//
	//  1. SEP2_SUBSCRIPTION_STORE_PATH wins (back-compat for #224).
	//  2. Else SEP2_DATA_DIR set -> <datadir>/subscriptions.json.
	//  3. Else "" -> pure in-memory (historical default).
	subPath := cfg.EffectiveStorePath("subscriptions", cfg.SubscriptionStorePath)
	subStore, subErr := memory.NewSubscriptionStoreWithPersistence(subPath)
	if subErr != nil {
		return fmt.Errorf("subscription store: %w", subErr)
	}
	if subPath != "" {
		log.Printf("subscription persistence enabled: %s", subPath)
	}

	// Initialize stores
	stores := &Stores{
		EndDevices:        endDevices,
		EndDeviceManagers: memory.NewEndDeviceManagementStore(),
		// EndDeviceIndexes is seeded below, once every startup writer of
		// EndDevice records (the persisted reload above, and the boot
		// fixture that follows) has run; see the comment there.
		Registrations:            registrations,
		RegistrationPolicy:       memory.RegistrationPolicy{}, // fail-closed: no self-registration pIN resolver wired yet
		MirrorUsagePoints:        memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings:      memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:                     memory.NewScopedStore[sep2.DER](),
		DERCapabilities:          memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:              memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:              memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:        memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:              derPrograms,
		DERControls:              memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		FSAs:                     memory.NewScopedStore[sep2.FunctionSetAssignments](),
		AdminFSAs:                adminFSAs,
		Subscriptions:            subStore,
		UsagePoints:              memory.NewStore[sep2.UsagePoint](),
		MeterReadings:            memory.NewScopedStore[sep2.MeterReading](),
		Readings:                 memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:             memory.NewStore[sep2.ReadingType](),
		Configurations:           memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:           memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:                memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:            memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:        memory.NewStore[sep2.MessagingProgram](),
		TextMessages:             memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),
	}

	if cfg.BootFixtureFile != "" {
		target := &bootfixture.Target{
			EndDevices:         stores.EndDevices,
			FSAs:               stores.FSAs,
			DERPrograms:        stores.DERPrograms,
			DERControls:        stores.DERControls,
			DefaultDERControls: stores.DefaultDERControls,
			DERCurves:          stores.DERCurves,
		}
		if err := bootfixture.Load(ctx, target, cfg.BootFixtureFile); err != nil {
			return fmt.Errorf("load boot fixture %q: %w", cfg.BootFixtureFile, err)
		}
		log.Printf("boot fixture loaded: %s", cfg.BootFixtureFile)
	}

	// Seed the index only now: endDevices can hold both the reload above and
	// boot-fixture records, neither of which is known to the allocator's own
	// in-memory state. Seeding any earlier would leave a fixture record's id
	// invisible to Allocate, so a self-registering device could be handed
	// that same id and get a permanent 409 on every retry (#443).
	stores.EndDeviceIndexes = memory.NewEndDeviceIndexFromStore(endDevices)

	// Subscription notification dispatcher. Owns its own bounded worker pool
	// and exits when ctx is cancelled (see shutdown branch below). The
	// router takes it as a handler.ResourceNotifier so DELETE/UPDATE
	// handlers can fan out notifications without depending on the
	// subscription package directly.
	//
	// Worker count and queue size are tunable at startup via
	// SEP2_SUBSCRIPTION_WORKERS and SEP2_SUBSCRIPTION_QUEUE_SIZE; both fall
	// back to the compile-time defaults when the env var is unset, empty, or
	// not a positive integer (no crash: warn and use the default).
	subWorkers := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", subscriptionWorkers)
	subQueueSize := resolveSubParam("SEP2_SUBSCRIPTION_QUEUE_SIZE", subscriptionQueueSize)
	notifier := newSubscriptionNotifier(cfg, stores.Subscriptions, subWorkers, subQueueSize)
	notifier.SetObserver(obs.RecordNotification)
	go notifier.Start(ctx)

	// Build the embeddable protocol server: it binds the listener, derives
	// the server identity (SFDI/LFDI) from the leaf cert BEFORE assembling the
	// routes so /sdev and /sdev/sdi see non-empty values under both cipher
	// modes (#1), and owns the graceful drain.
	//
	// Middleware carries the two wrappers that are this deployment's own
	// concern rather than an embedder's: the Prometheus request middleware,
	// and the build-tagged CSIP mutation mux, which is a no-op in production
	// builds. ShutdownTimeout is left at zero, which drains without a bound,
	// as this server has always done.
	embedCfg := NewEmbedConfig(cfg, stores, notifier)
	embedCfg.Addr = cfg.Addr
	embedCfg.CertFile = cfg.CertFile
	embedCfg.KeyFile = cfg.KeyFile
	embedCfg.CAFile = cfg.CAFile
	embedCfg.ExtraClientCAs = cfg.ExtraClientCAs
	embedCfg.EnableCCM = cfg.EnableCCM
	embedCfg.Middleware = func(h http.Handler) http.Handler {
		return obs.Middleware(wrapMutationHandlers(h, stores, notifier))
	}
	embedCfg.ConnState = func(_ net.Conn, state http.ConnState) {
		obs.RecordConnState(state.String())
	}

	protocolSrv, err := sep2server.New(embedCfg)
	if err != nil {
		return err
	}
	serverSFDI, serverLFDI := protocolSrv.Identity().SFDI, protocolSrv.Identity().LFDI
	protocolRoutes := protocolSrv.Patterns()

	if cfg.EnableCCM {
		log.Printf("IEEE 2030.5 server listening on %s (mTLS, CCM-8 primary)", cfg.Addr)
	} else {
		log.Printf("IEEE 2030.5 server listening on %s (mTLS, GCM)", cfg.Addr)
	}
	if len(cfg.ExtraClientCAs) > 0 {
		log.Printf("trusted extra client CAs: %v", cfg.ExtraClientCAs)
	}

	// errCh capacity covers the admin listener and the optional metrics
	// listener so a fast-failing Serve never blocks on an unbuffered send
	// during startup. The protocol listener has its own channel, because its
	// shutdown is driven by cancelling protocolCtx rather than by a Close.
	errCh := make(chan error, 2)

	// protocolCtx is deliberately NOT derived from ctx. Every exit path below
	// stops the protocol listener explicitly, and keeping it independent means
	// the drain is ordered the same way whether the shutdown was requested or
	// was forced by an admin or metrics listener failing to come up.
	protocolCtx, stopProtocol := context.WithCancel(context.Background())
	defer stopProtocol()

	protocolDone := make(chan error, 1)
	go func() {
		protocolDone <- protocolSrv.Run(protocolCtx)
	}()

	// stopProtocolServer drains the protocol listener and reports, without
	// returning, any error the drain produced. Used by the startup-abort paths
	// below and by the shutdown branch, so all three drain the same way.
	stopProtocolServer := func() {
		stopProtocol()
		if err := <-protocolDone; err != nil {
			log.Printf("protocol server shutdown error: %v", err)
		}
	}

	// Start mDNS if configured
	if cfg.EnableMDNS {
		mdnsReg, err := discovery.Register(discovery.Config{
			Hostname: cfg.MDNSHost,
			Port:     parsePort(cfg.Addr),
			Path:     "/dcap",
		})
		if err != nil {
			log.Printf("mDNS registration failed: %v (continuing without mDNS)", err)
		} else {
			defer mdnsReg.Close()
		}

		// #246: also advertise the admin surface as ieee2030-5.local
		// when an admin listener is configured. RegisterAdmin returns
		// (nil, nil) when the admin listen is loopback (the published
		// address would be unreachable off-box) - that's a skip, not a
		// failure, so the server keeps coming up.
		if listen := cfg.EffectiveAdminListen(); listen != "" {
			adminMdnsReg, err := discovery.RegisterAdmin(discovery.AdminConfig{Listen: listen})
			if err != nil {
				log.Printf("mDNS admin registration failed: %v (continuing without admin mDNS)", err)
			} else if adminMdnsReg != nil {
				defer adminMdnsReg.Close()
			}
		}
	}

	// Start admin server if configured. #161: admin runs on its own
	// listener (SEP2_ADMIN_LISTEN, falling back to SEP2_ADMIN_ADDR for
	// back-compat) with a weaker TLS posture than the SEP2 wire, so the
	// browser login + cert-paste flows can use Bearer/cookie auth without
	// weakening the SEP2 mTLS requirement. AdminTLS=false serves plain HTTP
	// (Caddy mode); AdminTLS=true serves HTTPS (operator-supplied cert or
	// self-signed fallback).
	var (
		adminSrv     *http.Server
		adminTLSDesc string
		adminAddr    string
		adminRoutes  []string
	)
	tlsModeName := "GCM"
	if cfg.EnableCCM {
		tlsModeName = "CCM-8"
	}
	if cfg.EffectiveAdminListen() != "" && svc != nil {
		adminSrv, adminTLSDesc, adminAddr, adminRoutes, err = startAdminServer(cfg, svc, stores, tlsModeName, errCh)
		if err != nil {
			stopProtocolServer()
			return fmt.Errorf("admin server: %w", err)
		}
	}

	// Observability: dedicated plain-HTTP metrics listener (SEP2_METRICS_ADDR).
	// Empty addr leaves it disabled. Serves ONLY GET /metrics; never carries
	// the protocol mTLS posture or the admin auth gate. Shares errCh and the
	// shutdown path with the other listeners.
	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		// #268 parity with the admin listener: a bare ":<port>" resolves
		// to loopback so the UNAUTHENTICATED /metrics surface is not exposed
		// network-wide by default. Any explicit host (0.0.0.0, an LAN IP,
		// [::]) is honored verbatim and warned about below.
		metricsAddr := config.ResolveMetricsBind(cfg.MetricsAddr)
		if msg := metricsExposureWarning(metricsAddr); msg != "" {
			log.Print(msg)
		}
		metricsSrv, err = startMetricsServer(metricsAddr, errCh)
		if err != nil {
			stopProtocolServer()
			if adminSrv != nil {
				_ = adminSrv.Close()
			}
			return fmt.Errorf("metrics server: %w", err)
		}
	}

	// #272: enumerate routes mounted on each listener at boot. The
	// /api/certs/* mis-mount that became Leon CRITICAL on PR #246 was
	// hard to spot by reading router.go; surfacing every route here at
	// startup means a duplicate / cross-listener mount lands in the
	// boot log on first run. Defense-in-depth observability companion
	// to the routing-scope test pinned by router_certs_scope_test.go.
	log.Print("\n" + RenderRoutesLog(cfg.Addr, protocolRoutes, adminAddr, adminRoutes))

	// #206: print the operator-facing connection-details banner once
	// after both listeners are up. Banner is log output only - it does not
	// change behavior and intentionally suppresses secrets (admin key,
	// private keys). Format is pinned by TestRenderConnectionBanner_*.
	log.Print("\n" + RenderConnectionBanner(buildBannerInput(cfg, tlsModeName, serverSFDI, serverLFDI, adminTLSDesc)))

	select {
	case <-ctx.Done():
		log.Println("shutting down servers...")
		stopProtocolServer()
		if adminSrv != nil {
			if err := adminSrv.Shutdown(context.Background()); err != nil {
				log.Printf("admin server shutdown error: %v", err)
			}
		}
		if metricsSrv != nil {
			if err := metricsSrv.Shutdown(context.Background()); err != nil {
				log.Printf("metrics server shutdown error: %v", err)
			}
		}
		return nil
	case err := <-protocolDone:
		// The protocol listener failed on its own; protocolCtx was never
		// cancelled, so this is a real failure and not a drain. Surface it
		// rather than logging it, exactly as the shared error channel did
		// before the protocol listener moved to pkg/sep2server.
		return err
	case err := <-errCh:
		return err
	}
}

// Browser admin session lifetimes. The idle window is what an operator
// notices; the absolute cap is what a stolen cookie runs into, and it is
// never extended by use.
const (
	adminSessionIdleTimeout     = 30 * time.Minute
	adminSessionAbsoluteTimeout = 8 * time.Hour
)

// startAdminServer brings up the admin listener on its own port. #161:
// the listener selection matrix is
//
//	AdminListen empty  -> admin disabled (caller gates this case)
//	AdminTLS = false   -> plain HTTP (Caddy reverse-proxy mode)
//	AdminTLS = true    -> HTTPS with operator cert (AdminCert/AdminKeyFile)
//	                     or self-signed fallback if neither is set
//
// HTTPS modes use VerifyClientCertIfGiven so AdminAuthMiddleware's mTLS
// Path A still works for cert-bearing operators while Bearer/cookie clients
// can connect without presenting a cert. The SEP2 protocol listener keeps
// its own RequireAnyClientCert + manual-verify posture untouched.
func startAdminServer(cfg *config.Config, svc *handler.AdminCertService, stores *Stores, tlsMode string, errCh chan error) (*http.Server, string, string, []string, error) {
	// #268: resolve the operator-supplied env value into the actual
	// bind string. A bare ":<port>" gets a loopback default so the admin
	// listener is safe-by-default; any explicit host (0.0.0.0, an LAN IP,
	// [::]) is honored verbatim. The banner still surfaces the env value
	// (see buildBannerInput) - only the net.Listen site uses the resolved.
	addr := config.ResolveAdminBind(cfg.EffectiveAdminListen())

	// #365: fail closed on the exposure posture before anything opens a
	// socket. A warning that the admin plane is reachable from the network
	// is only useful to an operator who reads the boot log; a refusal is
	// useful to the one who does not.
	if err := validateAdminExposure(addr, cfg.AdminAllowNonLoopback); err != nil {
		return nil, "", "", nil, err
	}

	// #365: an operator who typed whitespace into the admin key was trying
	// to set one. Silently disabling Bearer auth hides the typo behind a
	// later connection refusal; a startup error names the variable to fix.
	if err := validateAdminKey(cfg.AdminKey); err != nil {
		return nil, "", "", nil, err
	}

	// #269: warn loudly when the admin listener is bound to a non-
	// loopback address WITHOUT a proxy hint. Without an upstream proxy
	// injecting X-Forwarded-For/Forwarded, AdminAuthMiddleware Path 0
	// admits ALL traffic as loopback-local (the SEP2 protocol listener
	// uses RequireAnyClientCert and the loopback bypass admits any
	// request that arrives over loopback with no XFF). nginx's stock
	// config does NOT inject XFF; an operator who fronts the admin
	// listener with stock-nginx would silently expose the admin surface
	// to public traffic. The warning is doc-and-startup defense in depth;
	// #268 is the structural fix.
	if msg := adminProxyWarning(addr, cfg.AdminBehindProxy); msg != "" {
		log.Print(msg)
	}

	// #365: surface the plain-HTTP-plus-Secure-cookie dead end at boot.
	// Serving a real SPA makes it far more visible than a single page did,
	// and an operator who cannot log in has no way to reach this fact.
	if msg := adminSecureCookieWarning(addr, cfg.AdminTLS, cfg.AdminBehindProxy); msg != "" {
		log.Print(msg)
	}

	// #269 follow-up (Wren MED-4): when the operator silences the
	// non-loopback warning by setting SEP2_ADMIN_BEHIND_PROXY=true, drop
	// a one-shot INFO line in the boot log so the operator-trust signal
	// is recorded for incident-response triage. The warning itself is
	// suppressed unconditionally (operator-trust signal); this INFO is
	// the audit trail.
	if cfg.AdminBehindProxy {
		log.Printf("admin: SEP2_ADMIN_BEHIND_PROXY=true on %s; trusting upstream proxy to inject X-Forwarded-For/Forwarded for AdminAuthMiddleware Path 0",
			addr)
	}

	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(adminSessionIdleTimeout, adminSessionAbsoluteTimeout)
	// #270: resolve the admin host-header allowlist from the static
	// defaults plus operator-supplied SEP2_ADMIN_ALLOWED_HOSTS extras.
	allowedHosts := ResolveAdminAllowedHosts(cfg.AdminAllowedHosts)
	adminRouter, adminRoutes := BuildAdminRouter(cfg.AdminKey, svc, stores, tlsMode, tickets, sessions, allowedHosts, cfg.AdminLegacyDashboard)

	adminListener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("admin listen: %w", err)
	}

	serveListener := adminListener
	tlsModeDescription := "plain HTTP (Caddy mode)"
	if cfg.AdminTLS {
		tlsCfg, desc, err := buildAdminTLSConfig(cfg)
		if err != nil {
			_ = adminListener.Close()
			return nil, "", "", nil, fmt.Errorf("admin TLS config: %w", err)
		}
		serveListener = tls.NewListener(adminListener, tlsCfg)
		tlsModeDescription = desc
	}

	adminSrv := newAdminServer(adminRouter)

	go func() {
		log.Printf("Admin server listening on %s (%s)", addr, tlsModeDescription)
		if cfg.AdminKey != "" {
			log.Println("Admin API key configured")
		} else {
			log.Println("WARNING: No admin API key set (SEP2_ADMIN_KEY). Bearer auth disabled.")
		}
		errCh <- adminSrv.Serve(serveListener)
	}()

	return adminSrv, tlsModeDescription, addr, adminRoutes, nil
}

// startMetricsServer brings up the dedicated plain-HTTP Prometheus metrics
// listener. It serves ONLY GET /metrics -> obs.Handler(); no other route is
// mounted, and it is deliberately NOT the mTLS protocol mux nor the
// auth-gated admin mux (exposing /metrics there would either require a
// client cert per scrape or leak through the admin auth surface). The
// server runs on the shared errCh and is shut down cleanly by Run's
// ctx.Done branch.
func startMetricsServer(addr string, errCh chan error) (*http.Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics listen: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", obs.Handler())

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}

	go func() {
		log.Printf("Metrics server listening on %s (plain HTTP, GET /metrics only)", addr)
		errCh <- srv.Serve(listener)
	}()

	return srv, nil
}

// buildBannerInput projects the runtime config + derived identity into the
// flat BannerInput struct. Keeping this projection separate from the
// renderer means tests can pin the formatting independently from changes
// to the config struct or the admin-listener wiring.
//
// Admin URL: empty AdminListen => banner shows "(disabled)". Admin auth:
// AdminKey present => "Bearer key set"; empty + no AdminTLS => "disabled"
// (the only auth path is Bearer at this listener - #161 admin runs on
// its own port and does not require client certs). The key itself is NEVER
// printed.
func buildBannerInput(cfg *config.Config, tlsMode, serverSFDI, serverLFDI, adminTLSDesc string) BannerInput {
	adminAuth := "disabled"
	if cfg.AdminKey != "" {
		adminAuth = "Bearer key set"
	}

	dataDir := "in-memory"
	if cfg.DataDir != "" {
		dataDir = cfg.DataDir
	}
	// SEP2_SUBSCRIPTION_STORE_PATH takes precedence over the DataDir-derived
	// path for the subscription store (back-compat from #224). Surface
	// it on the banner so the operator can see exactly which file the
	// subscription store is persisting to.
	if cfg.SubscriptionStorePath != "" {
		dataDir = fmt.Sprintf("%s (subscriptions: %s)", dataDir, cfg.SubscriptionStorePath)
	}

	return BannerInput{
		Addr:           cfg.Addr,
		TLSMode:        tlsMode,
		CertFile:       cfg.CertFile,
		ServerSFDI:     serverSFDI,
		ServerLFDI:     serverLFDI,
		CAFile:         cfg.CAFile,
		ExtraClientCAs: cfg.ExtraClientCAs,
		AdminListen:    cfg.EffectiveAdminListen(),
		AdminTLSDesc:   adminTLSDesc,
		AdminAuthDesc:  adminAuth,
		DataDirDesc:    dataDir,
		MDNSEnabled:    cfg.EnableMDNS,
	}
}

// buildAdminTLSConfig assembles the admin listener's *tls.Config from the
// admin-cert env vars. Operator-supplied cert/key wins; otherwise a fresh
// self-signed cert covering localhost is generated (current default). The
// resulting config uses VerifyClientCertIfGiven so mTLS-bearing operators
// flow through AdminAuthMiddleware Path A while browser clients without a
// cert still complete the handshake and authenticate via Bearer/cookie.
//
// The returned description string is human-readable for the startup log.
func buildAdminTLSConfig(cfg *config.Config) (*tls.Config, string, error) {
	var (
		cert tls.Certificate
		desc string
	)
	switch {
	case cfg.AdminCert != "" && cfg.AdminKeyFile != "":
		c, err := tls.LoadX509KeyPair(cfg.AdminCert, cfg.AdminKeyFile)
		if err != nil {
			return nil, "", fmt.Errorf("load admin cert/key: %w", err)
		}
		cert = c
		desc = "HTTPS, operator cert"
	case cfg.AdminCert != "" || cfg.AdminKeyFile != "":
		return nil, "", fmt.Errorf("admin cert/key must be set together (got AdminCert=%q AdminKeyFile=%q)", cfg.AdminCert, cfg.AdminKeyFile)
	default:
		certPEM, keyPEM, err := certs.GenerateSelfSignedTLS([]string{"localhost", "127.0.0.1", "::1"})
		if err != nil {
			return nil, "", fmt.Errorf("generate admin TLS cert: %w", err)
		}
		c, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, "", fmt.Errorf("parse admin TLS cert: %w", err)
		}
		cert = c
		desc = "HTTPS, self-signed"
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, desc, nil
}

// resolveSubParam reads the named environment variable and parses it as a
// positive integer. When the variable is unset or empty the defaultVal is
// returned without any log output (default behavior is byte-for-byte
// unchanged). When the variable is set but its value is not a positive integer
// (non-numeric, zero, or negative) a warning is logged naming the variable and
// the bad value, and the defaultVal is returned. This function never panics or
// calls os.Exit.
func resolveSubParam(envKey string, defaultVal int) int {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		log.Printf("WARNING: %s=%q is not a positive integer; using default %d", envKey, raw, defaultVal)
		return defaultVal
	}
	return v
}

func parsePort(addr string) int {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		if port, err := strconv.Atoi(addr[idx+1:]); err == nil {
			return port
		}
	}
	return 443
}

// validateAdminExposure refuses an admin bind that is reachable from outside
// this host unless the operator has opted in. It runs before net.Listen so a
// refused configuration opens no socket at all.
//
// The boundary is itself a candidate for the condition it rejects, so anything
// isLoopbackBind cannot positively establish as loopback counts as
// non-loopback: the unspecified addresses 0.0.0.0 and [::] (which bind every
// interface), an unresolved hostname, and a malformed or host-less address.
// An empty addr means the admin listener is disabled and the caller already
// gates that case.
func validateAdminExposure(addr string, allowNonLoopback bool) error {
	if addr == "" || allowNonLoopback || isLoopbackBind(addr) {
		return nil
	}
	return fmt.Errorf("admin listener refuses to bind non-loopback address %q, "+
		"which is reachable from outside this host: set "+
		"SEP2_ADMIN_ALLOW_NON_LOOPBACK=true to allow it, or set "+
		"SEP2_ADMIN_LISTEN to a bare :<port> for a loopback-only admin plane",
		addr)
}

// validateAdminKey refuses a configured admin key that carries no credential
// material. An unset key is the deliberate "Bearer auth disabled" state and is
// left alone; whitespace-only is a typo, and it is distinguished from unset
// rather than folded into it.
//
// The error describes the value without echoing it, so no configured
// credential can reach a log line by way of a startup failure.
func validateAdminKey(adminKey string) error {
	if adminKey == "" || !auth.IsBlankCredential(adminKey) {
		return nil
	}
	return errors.New("SEP2_ADMIN_KEY is set to whitespace only, which is not " +
		"a usable credential: set it to a non-blank token, or leave " +
		"SEP2_ADMIN_KEY unset to disable Bearer auth deliberately")
}

// adminProxyWarning returns the #269 startup-warning text when the
// admin listener is bound to a non-loopback address AND the operator
// has not declared an upstream proxy. Returns empty string when no
// warning is warranted (loopback bind, or proxy hint set, or empty
// addr - caller already gates the disabled case).
//
// The warning explains the failure mode in operator-facing terms: an
// upstream proxy MUST inject X-Forwarded-For (or RFC 7239 Forwarded)
// for AdminAuthMiddleware Path 0's loopback-bypass decline to function.
// Pure function so tests can assert content directly without intercepting
// log output.
func adminProxyWarning(addr string, behindProxy bool) string {
	if addr == "" || behindProxy {
		return ""
	}
	if isLoopbackBind(addr) {
		return ""
	}
	return "WARNING: admin listener bound to non-loopback address " + addr +
		" without SEP2_ADMIN_BEHIND_PROXY=true. " +
		"AdminAuthMiddleware Path 0 declines requests that carry " +
		"X-Forwarded-For/Forwarded headers, but stock nginx does NOT " +
		"inject those headers by default - an unconfigured nginx in " +
		"front of this listener would let all relayed traffic look " +
		"loopback-local and bypass admin auth. Configure your upstream " +
		"proxy to inject X-Forwarded-For (or Forwarded per RFC 7239), " +
		"then set SEP2_ADMIN_BEHIND_PROXY=true to silence this warning. " +
		"For loopback-only admin, leave SEP2_ADMIN_LISTEN as :<port> " +
		"(#268 default)."
}

// adminSecureCookieWarning returns a startup-warning string when a plain-HTTP
// admin listener is bound where the browser login flow cannot work: the
// admin_ticket cookie is minted Secure (see auth.NewAdminTicketCookie), and a
// browser discards a Secure cookie that arrives over plain HTTP from a
// non-loopback origin. The login POST then appears to succeed while every
// following request is unauthenticated, which reads as a server bug.
//
// Loopback is exempt because browsers treat a loopback origin as a
// potentially-trustworthy context and keep the cookie. AdminBehindProxy is
// exempt because the operator has declared a proxy that terminates TLS at the
// browser-facing origin, which is the supported Caddy-mode deployment.
//
// Pure function so tests assert content without intercepting log output.
func adminSecureCookieWarning(addr string, adminTLS, behindProxy bool) string {
	if addr == "" || adminTLS || behindProxy || isLoopbackBind(addr) {
		return ""
	}
	return "WARNING: admin listener is plain HTTP on non-loopback address " + addr +
		" with no upstream TLS proxy declared. The admin_ticket session cookie " +
		"is set Secure, and a browser discards a Secure cookie delivered over " +
		"plain HTTP from a non-loopback origin, so the browser login flow " +
		"CANNOT complete from another host: the login will appear to succeed " +
		"and every request after it will be unauthenticated. Set " +
		"SEP2_ADMIN_TLS=true to serve HTTPS directly, or terminate TLS in an " +
		"upstream proxy and set SEP2_ADMIN_BEHIND_PROXY=true. Bearer and mTLS " +
		"clients are unaffected."
}

// metricsExposureWarning returns a startup-warning string when the resolved
// metrics bind address is non-loopback, and empty otherwise (loopback bind or
// empty addr - caller gates the disabled case). The /metrics surface is
// UNAUTHENTICATED (no client cert, no Bearer), so a non-loopback bind exposes
// raw exposition data network-wide; the warning makes that exposure visible at
// boot. Mirrors adminProxyWarning; pure function so tests assert content
// without intercepting log output.
func metricsExposureWarning(addr string) string {
	if addr == "" {
		return ""
	}
	if isLoopbackBind(addr) {
		return ""
	}
	return "WARNING: metrics listener bound to non-loopback address " + addr +
		". The /metrics endpoint is UNAUTHENTICATED (no client cert, no " +
		"Bearer gate) and now exposes Prometheus exposition data on all " +
		"reachable interfaces. This is required for a containerized " +
		"Prometheus that scrapes via host.docker.internal (the docker " +
		"bridge gateway is NOT loopback), but it MUST sit behind a host " +
		"firewall / trusted network. For loopback-only metrics, set " +
		"SEP2_METRICS_ADDR=:<port> (#268 default)."
}

// isLoopbackBind reports whether addr is bound to a loopback host.
// Used by #269's warning gate. Accepts a "host:port" string; treats
// the empty host as non-loopback (caller already resolves bare
// ":<port>" via ResolveAdminBind to "127.0.0.1:<port>" so this case
// should not arise in production). Hostnames are NOT resolved - a name
// like "admin.internal" is treated as non-loopback so the warning
// fires. The hostname might be loopback but we will not gamble on it
// without DNS, and a false-positive warning is harmless.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Server-identity derivation from the leaf certificate moved to
// pkg/sep2server alongside the TLS listener it belongs to. It is read
// back here through sep2server.Server.Identity; the #1 regression
// guards in server_identity_test.go still drive it through the full Run flow.
