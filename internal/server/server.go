package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
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
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/discovery"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/obs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
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

	// captureShutdownTimeout bounds the traffic-capture Recorder and Store
	// close (#611), run after every listener has itself stopped. Distinct
	// from the admin/metrics listeners' unbounded context.Background()
	// shutdown: capture's Close contract is explicitly ctx-bounded (see
	// sep2capture's lifecycle design), so Run gives it a real deadline
	// rather than passing one that never fires.
	captureShutdownTimeout = 5 * time.Second

	// adminShutdownTimeout bounds adminSrv.Shutdown (#628 fix round 1,
	// silent-failure MEDIUM): closeCapture runs first and now guarantees
	// Store.Subscribe refuses to register a new subscription once the
	// store is closed (store_reader.go), so a stream that arrives during
	// shutdown can no longer hold Shutdown open on its own. This bound is
	// defense in depth for any other handler that blocks past it, so the
	// admin listener's own shutdown can never hang past a fixed grace
	// period. Two later steps on the same path are still unbounded:
	// metricsSrv.Shutdown(context.Background()) and the bare <-notifierDone
	// wait (#628 fix round 2, silent-failure LOW).
	adminShutdownTimeout = 10 * time.Second
)

// startNotifier runs the notification manager until its ctx is cancelled. A
// test replaces it to hold the manager's shutdown open.
var startNotifier = (*coresub.Manager).Start

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
	endDeviceManagers, err := memory.NewEndDeviceManagementStoreWithPersistence(
		cfg.EffectiveStorePath("enddevicemanagement", ""),
	)
	if err != nil {
		return fmt.Errorf("EndDeviceManagement persistence: %w", err)
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
		EndDeviceManagers: endDeviceManagers,
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
		// With a data_dir the fixture seeds once and yields to persisted
		// state; without one the seed path is empty and every record is
		// created, as before (#352).
		seedPath := cfg.EffectiveStorePath("bootfixture-seed", "")
		if err := bootfixture.Reconcile(ctx, target, cfg.BootFixtureFile, seedPath, log.Printf); err != nil {
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
	notifierDone := make(chan struct{})
	go func() {
		defer close(notifierDone)
		startNotifier(notifier, ctx)
	}()

	// #628 fix round 1: traffic capture is off unless SEP2_TRAFFIC_CAPTURE
	// explicitly permits it. Two lanes measured capture turning itself on
	// from SEP2_DATA_DIR alone, with no boot line and no way off, on every
	// deployment that already sets SEP2_DATA_DIR for persistence; one read
	// back an Authorization header byte for byte through the traffic API.
	// cfg.TrafficCapture decides WHETHER; EffectiveTrafficDir (SEP2_TRAFFIC_DIR,
	// else <DataDir>/traffic) keeps deciding only WHERE, as it did before.
	// A reset refusal (a foreign file or a symlinked directory, see
	// sep2capture's guarded reset) leaves capture off with a named boot
	// error rather than failing startup, since the traffic log is a
	// debugging aid the protocol does not depend on. The store's defaults
	// already ARE the operator's 2026-09-21 limits (600 MB cap, 64 MB
	// segments, 4 MiB per direction, ~256 MB index budget), so
	// StoreConfig sets only Dir.
	//
	// captureStore.Handler() is threaded into startAdminServer below so it
	// mounts on the authenticated admin mux; captureRecorder is threaded
	// into embedCfg.Capture so sep2server.New attaches it to the protocol
	// listener. Both are nil when capture is off, which is a no-op at both
	// sites.
	//
	// closeCapture is called explicitly, not only deferred: it must run
	// BEFORE adminSrv.Shutdown below, not after. Store.Close ends every
	// open GET /api/traffic/stream subscription (and now refuses to
	// register one that arrives after it, per Store.Subscribe's
	// closed-store guard) so a handler blocked in that stream's select
	// loop returns immediately instead of holding its connection open;
	// adminSrv.Shutdown below is itself bounded now too, as defense in
	// depth against any other handler that blocks past its budget.
	// Registered as a defer too, idempotently (both Recorder.Close and
	// Store.Close document repeat calls as safe and cheap), so every other
	// exit path (an admin or metrics startup failure, a protocol listener
	// failure) still closes capture rather than leaking its writer
	// goroutine.
	var (
		captureStore    *sep2capture.Store
		captureRecorder *sep2capture.Recorder
		closeCapture    = func() {}
	)
	if !cfg.TrafficCapture {
		if cfg.TrafficCaptureEnv == "" {
			log.Printf("traffic capture disabled: SEP2_TRAFFIC_CAPTURE is not set")
		} else {
			log.Printf("traffic capture disabled: SEP2_TRAFFIC_CAPTURE=%q is not \"true\"", cfg.TrafficCaptureEnv)
		}
	} else if trafficDir := cfg.EffectiveTrafficDir(); trafficDir == "" {
		log.Printf("traffic capture disabled: SEP2_TRAFFIC_CAPTURE is set but neither SEP2_TRAFFIC_DIR nor SEP2_DATA_DIR is set")
	} else if store, storeErr := sep2capture.NewStore(sep2capture.StoreConfig{Dir: trafficDir}); storeErr != nil {
		log.Printf("traffic capture disabled: %v", storeErr)
	} else {
		captureStore = store
		captureRecorder = sep2capture.NewRecorder(captureStore, log.Default())
		log.Printf("traffic capture enabled: recording to %s (cap 600 MB)", trafficDir)
		closeCapture = func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), captureShutdownTimeout)
			defer cancel()
			if cerr := captureRecorder.Close(shutdownCtx); cerr != nil {
				log.Printf("traffic capture recorder close: %v", cerr)
			}
			if cerr := captureStore.Close(shutdownCtx); cerr != nil {
				log.Printf("traffic capture store close: %v", cerr)
			}
		}
		defer closeCapture()
	}

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
	// #622: the protocol listener's ClientCAs pool is the DEVICE CA role,
	// not the serving one - it verifies DEVICES, never this server's own
	// leaf. EffectiveDeviceCA falls back to CAFile, so an unsplit
	// deployment reads the same file it always did.
	embedCfg.CAFile = cfg.EffectiveDeviceCA()
	embedCfg.ExtraClientCAs = cfg.ExtraClientCAs
	embedCfg.Middleware = func(h http.Handler) http.Handler {
		return obs.Middleware(wrapMutationHandlers(h, stores, notifier))
	}
	embedCfg.ConnState = func(_ net.Conn, state http.ConnState) {
		obs.RecordConnState(state.String())
	}
	embedCfg.Capture = captureRecorder

	protocolSrv, err := sep2server.New(embedCfg)
	if err != nil {
		return err
	}
	serverSFDI, serverLFDI := protocolSrv.Identity().SFDI, protocolSrv.Identity().LFDI
	protocolRoutes := protocolSrv.Patterns()

	log.Printf("IEEE 2030.5 server listening on %s (mTLS, CCM-8)", cfg.Addr)
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
	tlsModeName := "CCM-8"
	// #638 fix round 3 item 1: svc != nil alone is not the right gate here.
	// It turns non-nil whenever a CA CERTIFICATE loads (round 1's keyless
	// posture), which would bring up the whole admin plane - login, the UI,
	// and every admin route - on a deployment that kept only a certificate
	// on purpose. CanMint restores the pre-split gate: the admin listener
	// starts only when at least one role can actually do something with
	// its CA beyond reporting it, matching the behavior before #622 split
	// the single CA into independently loadable serving/device pairs. The
	// startup banner and the mismatch warning below are unaffected: both
	// read svc directly, outside this gate.
	if cfg.EffectiveAdminListen() != "" && svc != nil && svc.CanMint() {
		var trafficHandler http.Handler
		if captureStore != nil {
			trafficHandler = captureStore.Handler()
		}
		adminSrv, adminTLSDesc, adminAddr, adminRoutes, err = startAdminServer(cfg, svc, stores, tlsModeName, errCh, trafficHandler)
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

	// #638 fix round 1 (MEDIUM 2): warn, before the banner prints it as
	// healthy, when the server's own leaf does not chain to the serving CA.
	// svc may be nil (no CA loaded at all); servingCAMismatchWarning treats
	// a nil serving CA the same as "nothing to compare against".
	if svc != nil {
		if msg := servingCAMismatchWarning(cfg.CertFile, svc.ServingCA()); msg != "" {
			log.Print(msg)
		}
	}

	// #206: print the operator-facing connection-details banner once
	// after both listeners are up. Banner is log output only - it does not
	// change behavior and intentionally suppresses secrets (admin key,
	// private keys). Format is pinned by TestRenderConnectionBanner_*.
	log.Print("\n" + RenderConnectionBanner(buildBannerInput(cfg, svc, tlsModeName, serverSFDI, serverLFDI, adminTLSDesc)))

	select {
	case <-ctx.Done():
		log.Println("shutting down servers...")
		stopProtocolServer()
		// Runs before adminSrv.Shutdown: see closeCapture's own comment
		// above for why the order is load-bearing rather than incidental.
		closeCapture()
		if adminSrv != nil {
			// #628 fix round 1: bounded rather than context.Background().
			// See adminShutdownTimeout's own comment for why.
			adminShutdownCtx, adminShutdownCancel := context.WithTimeout(context.Background(), adminShutdownTimeout)
			if err := adminSrv.Shutdown(adminShutdownCtx); err != nil {
				log.Printf("admin server shutdown error: %v", err)
			}
			adminShutdownCancel()
		}
		if metricsSrv != nil {
			if err := metricsSrv.Shutdown(context.Background()); err != nil {
				log.Printf("metrics server shutdown error: %v", err)
			}
		}
		// The manager counts and logs the notifications it drops at shutdown;
		// returning first could let the process exit before it has.
		<-notifierDone
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
func startAdminServer(cfg *config.Config, svc *handler.AdminCertService, stores *Stores, tlsMode string, errCh chan error, trafficHandler http.Handler) (*http.Server, string, string, []string, error) {
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
	adminRouter, adminRoutes := BuildAdminRouter(cfg.AdminKey, svc, stores, tlsMode, tickets, sessions, allowedHosts, cfg.AdminLegacyDashboard, trafficHandler)

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

// caRoleInfo reads the subject and SHA-256 fingerprint the banner prints for
// a loaded CA certificate. Returns two empty strings when cert is nil (not
// loaded), which caLine renders as "(not loaded)" rather than a blank line.
func caRoleInfo(cert *x509.Certificate) (subject, fingerprint string) {
	if cert == nil {
		return "", ""
	}
	fp := sepTLS.Fingerprint(cert)
	return cert.Subject.String(), hex.EncodeToString(fp[:])
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
//
// #622: svc carries the loaded serving/device CA certificates (nil when a
// CA failed to load, or when the admin cert API is off entirely); their
// subject and fingerprint come from svc's read-only accessors, never from
// re-reading the CA files here. SameCA compares the RESOLVED paths
// (EffectiveServingCA/EffectiveDeviceCA), so an unsplit deployment (both
// empty, both falling back to CAFile) still gets the "one certificate
// fills both roles" note.
//
// #638 fix round 1 (MEDIUM 1): svc.ServingCA()/DeviceCA() now return a
// loaded certificate whenever that CA's certificate file parsed, whether or
// not its key is present or usable - see certs.LoadCAPair, wired in at
// cmd/sep2server/main.go. Before that change this projection quietly went
// through a service that existed only when a key had ALSO loaded, so a
// keys-off-the-server deployment read every CA as "(not loaded)" although
// the listener was actively trusting it.
func buildBannerInput(cfg *config.Config, svc *handler.AdminCertService, tlsMode, serverSFDI, serverLFDI, adminTLSDesc string) BannerInput {
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

	var servingCert, deviceCert *x509.Certificate
	if svc != nil {
		servingCert, deviceCert = svc.ServingCA(), svc.DeviceCA()
	}
	servingSubject, servingFingerprint := caRoleInfo(servingCert)
	deviceSubject, deviceFingerprint := caRoleInfo(deviceCert)

	return BannerInput{
		Addr:                 cfg.Addr,
		TLSMode:              tlsMode,
		CertFile:             cfg.CertFile,
		ServerSFDI:           serverSFDI,
		ServerLFDI:           serverLFDI,
		ServingCAFile:        cfg.EffectiveServingCA(),
		ServingCASubject:     servingSubject,
		ServingCAFingerprint: servingFingerprint,
		DeviceCAFile:         cfg.EffectiveDeviceCA(),
		DeviceCASubject:      deviceSubject,
		DeviceCAFingerprint:  deviceFingerprint,
		SameCA:               cfg.EffectiveServingCA() == cfg.EffectiveDeviceCA(),
		ExtraClientCAs:       cfg.ExtraClientCAs,
		AdminListen:          cfg.EffectiveAdminListen(),
		AdminTLSDesc:         adminTLSDesc,
		AdminAuthDesc:        adminAuth,
		DataDirDesc:          dataDir,
		MDNSEnabled:          cfg.EnableMDNS,
		// #638 fix round 1 (HIGH 1): set explicitly, rather than left empty
		// for RenderConnectionBanner's fallback to fill in silently. A
		// device verifies THIS server with the serving CA (see
		// RenderConnectionBanner's own comment on caHint), and this is the
		// only production call site, so the choice belongs here where a
		// test can assert it directly rather than only in the renderer's
		// untested-at-boot fallback branch.
		DeviceCAHint: cfg.EffectiveServingCA(),
	}
}

// servingCAMismatchWarning returns a boot-log warning when the server's own
// leaf certificate (certFile) is not signed by servingCA, and "" when they
// match, the leaf cannot be read or parsed, or servingCA is nil (no serving
// CA loaded - the "(not loaded)" banner line already covers that case, and
// there is nothing here to compare against).
//
// #638 fix round 1 (MEDIUM 2): nothing previously compared the server's own
// leaf against the serving CA the banner advertises as this server's
// anchor. The mismatch fails no local handshake - the protocol listener
// never verifies its own leaf - and surfaces only off this host, at a
// device that trusted the printed serving CA and got "certificate signed
// by unknown authority" with nothing in the boot log pointing at the
// cause. A warning, not a refusal: #622's scope is a role split, and a
// deployment that would have started before this fix round must still
// start after it.
//
// #638 fix round 3 item 4: the original check was leaf.CheckSignatureFrom
// (direct signature only), so a leaf issued by an intermediate under the
// advertised serving CA - a valid chained deployment - warned falsely. This
// now verifies the chain the way a client would: certFile's later PEM
// blocks (if any) go into Intermediates, and servingCA is the sole trusted
// root, matching the pattern the shared library's own peer verifier uses
// (vendor/.../pkg/sep2tls/verify.go).
//
// Pure function so tests assert content directly without intercepting log
// output or standing up a listener.
func servingCAMismatchWarning(certFile string, servingCA *x509.Certificate) string {
	if servingCA == nil {
		return ""
	}
	leafPEM, err := os.ReadFile(certFile)
	if err != nil {
		return ""
	}
	chain, err := certs.ParseCertificateChainPEM(leafPEM)
	if err != nil {
		return ""
	}
	leaf := chain[0]
	roots := x509.NewCertPool()
	roots.AddCert(servingCA)
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates}); err != nil {
		return fmt.Sprintf(
			"WARNING: server certificate %s is not signed by the advertised serving CA (subject %s): %v. "+
				"A device that trusts the printed serving CA will refuse this server with "+
				`"certificate signed by unknown authority". Regenerate the server certificate `+
				"from the current serving CA, or point SEP2_SERVING_CA at the CA that signed it.",
			certFile, servingCA.Subject, err)
	}
	return ""
}

// buildAdminTLSConfig assembles the admin listener's *tls.Config from the
// admin-cert env vars. Operator-supplied cert/key wins; otherwise a fresh
// self-signed cert covering localhost is generated (current default). The
// resulting config uses VerifyClientCertIfGiven so mTLS-bearing operators
// flow through AdminAuthMiddleware Path A while browser clients without a
// cert still complete the handshake and authenticate via Bearer/cookie.
//
// #624: ClientCAs comes from adminClientCAPool. Before this, ClientCAs was
// left nil: per crypto/tls (handshake_server.go, the VerifyClientCertIfGiven
// branch) and crypto/x509 (Certificate.Verify, the opts.Roots == nil
// branch), a nil ClientCAs pool falls back to the HOST ROOT set, so a
// private-CA operator cert failed the handshake before
// AdminAuthMiddleware's policy-OID check ever ran (#418).
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

	clientCAs, anchorDesc, err := adminClientCAPool(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("admin client CA: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    clientCAs,
	}, desc + ", client CA: " + anchorDesc, nil
}

// adminClientCARemedy is the operator-facing fix for every shape of
// adminClientCAPool refusal: named once so an explicit refusal and an
// explicit non-CA refusal give the identical instruction.
const adminClientCARemedy = `set SEP2_CA, SEP2_SERVING_CA, or SEP2_ADMIN_CLIENT_CA (a path, or "system" for host roots)`

// adminClientCAPool resolves the admin listener's ClientCAs pool and its
// startup-banner description from Config.EffectiveAdminClientCA (#624).
// AdminClientCASystemRoots defers to buildAdminTLSConfig's nil handling.
//
// Every path below that can refuse (return a non-nil error) honours the same
// provenance split: a hard refusal when the operator set the anchor
// explicitly (AdminClientCA != ""), and a closed-but-running degrade (an
// empty, non-nil pool - never nil, which would reopen #418's host-root
// fallback) otherwise. Exactly two paths can refuse: the file read, and
// finding zero usable CA certificates in it (parseAdminClientCAPool).
func adminClientCAPool(cfg *config.Config) (*x509.CertPool, string, error) {
	anchor := cfg.EffectiveAdminClientCA()
	explicit := cfg.AdminClientCA != ""

	switch anchor {
	case config.AdminClientCASystemRoots:
		return nil, "host root trust store (SEP2_ADMIN_CLIENT_CA=system)", nil
	case "":
		log.Print("admin client CA not configured: operator certificate sign-in disabled")
		return x509.NewCertPool(), "none configured; operator certificate sign-in disabled", nil
	}

	pemBytes, err := os.ReadFile(anchor)
	if err != nil {
		if explicit {
			return nil, "", fmt.Errorf("load %s: %w; %s", anchor, err, adminClientCARemedy)
		}
		log.Printf("admin client CA not loaded (%s: %v): operator certificate sign-in disabled", anchor, err)
		return x509.NewCertPool(), fmt.Sprintf("NOT LOADED (%s: %v); operator certificate sign-in disabled", anchor, err), nil
	}

	// One pass builds the pool AND counts its CAs (parseAdminClientCAPool),
	// so the count can never describe a pool other than the one
	// buildAdminTLSConfig hands to tls.Config.ClientCAs.
	pool, cas, certCount, skipped := parseAdminClientCAPool(pemBytes)
	if skipped > 0 {
		// A skipped block (wrong PEM type, headers, or a parse failure) is
		// otherwise invisible: the file still loads and the banner still
		// prints a CA count, so an operator who meant to trust N CAs sees a
		// smaller number with nothing explaining the gap.
		log.Printf("admin client CA %s: skipped %d unusable PEM block(s) while loading", anchor, skipped)
	}
	caCount := len(cas)
	if caCount == 0 {
		if explicit {
			return nil, "", fmt.Errorf("%s holds no CA certificate; %s", anchor, adminClientCARemedy)
		}
		log.Printf("admin client CA not loaded (%s: no CA certificate): operator certificate sign-in disabled", anchor)
		return x509.NewCertPool(), "no CA certificate in " + anchor, nil
	}

	// AddCert (parseAdminClientCAPool) makes every parsed certificate a
	// usable anchor, not only the CA-flagged ones, matching
	// AppendCertsFromPEM. Naming only caCount understates what the pool
	// trusts whenever a non-CA certificate rode along, so the certificate
	// total is folded in wherever it differs from the CA count.
	desc := anchor + " ("
	if certCount != caCount {
		desc += strconv.Itoa(certCount) + " certificates; "
	}
	desc += strconv.Itoa(caCount) + " CA"
	if caCount == 1 {
		subject, fingerprint := caRoleInfo(cas[0])
		desc += "; " + subject + "; " + fingerprint
	} else {
		// The banner line stays terse above one CA (design-pinned: no single
		// member identifies a bundle anchor); the per-CA detail goes to the
		// startup log instead of nowhere.
		log.Printf("admin client CA %s trusts %d certificate authorities:", anchor, caCount)
		for _, ca := range cas {
			subject, fingerprint := caRoleInfo(ca)
			log.Printf("  %s; %s", subject, fingerprint)
		}
	}
	desc += ")"
	return pool, desc, nil
}

// parseAdminClientCAPool builds an *x509.CertPool from pemBytes using the
// same per-block accept/skip rule as x509.CertPool.AppendCertsFromPEM (skip
// a block whose type isn't "CERTIFICATE", that carries PEM headers, or that
// fails to parse), so the pool matches what AppendCertsFromPEM would have
// built. cas returns, in file order, only the CA-flagged certificates;
// certCount is every certificate actually added to pool (CA-flagged or
// not, since AddCert makes either one a usable anchor); skipped is every
// PEM block that did not become a pool entry.
func parseAdminClientCAPool(pemBytes []byte) (pool *x509.CertPool, cas []*x509.Certificate, certCount, skipped int) {
	pool = x509.NewCertPool()
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			skipped++
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			skipped++
			continue
		}
		pool.AddCert(cert)
		certCount++
		if cert.IsCA && cert.BasicConstraintsValid {
			cas = append(cas, cert)
		}
	}
	return pool, cas, certCount, skipped
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
