package csiptest

// server.go: BootServer helper for the CSIP conformance harness.
//
// Lifts the inline boot-an-in-process-server pattern out of
// test/csip/handshake_test.go (#23) so Phase 3 tests can express
// the canonical "run one CSIP test against a fresh server" flow in
// three lines:
//
//	srv := csiptest.BootServer(t)
//	dcap, err := srv.Client().GetDeviceCapability(ctx)
//	// ...
//
// Each call boots its own listener on TCP :0 (kernel-assigned port) with
// its own store set, so parallel tests using t.Parallel() never collide
// on port or state.
//
// Two cipher modes are supported:
//
//  1. GCM (default) uses stdlib crypto/tls. Fast, no fixture deps.
//     Use this for everything that does not specifically assert CCM-8
//     wire behavior. The default keeps Phase 3 tests cheap.
//
//  2. CCM-8 uses the fork vendored at
//     vendor/github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls
//     that registers TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 (0xC0AE). Opt in via
//     WithCCMMode(). Tests that prove spec-cipher conformance should
//     opt in; everything else should not pay the cost.
//
// The helper generates an ephemeral CA + server cert per boot. By
// default it also generates an ephemeral device cert and wires the
// returned Client to present it on every request. Tests that want to
// drive the server with an external client cert (e.g. SunSpec V1.2
// fixtures in handshake_test.go) pass WithClientCert.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// deriveServerIdentity parses the leaf cert from a raw DER chain (as
// found in tls.Certificate.Certificate / gotls.Certificate.Certificate)
// and returns the server SFDI and LFDI. Mirrors the unexported helper
// of the same name in internal/server/server.go (#1) so the
// in-process harness populates /sdev and /sdev/sdi the same way the
// production Run() flow does. Mode-agnostic: same code path for both
// GCM (stdlib crypto/tls) and CCM-8 (vendored gotls). t.Fatal on any
// failure; an empty chain means the caller fed BootServer a broken
// PKI and the test should surface that loudly.
func deriveServerIdentity(t *testing.T, rawChain [][]byte) (sfdi, lfdi string) {
	t.Helper()
	if len(rawChain) == 0 {
		t.Fatalf("csiptest: derive server identity: empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(rawChain[0])
	if err != nil {
		t.Fatalf("csiptest: derive server identity: parse leaf: %v", err)
	}
	return sepTLS.SFDI(leaf), sepTLS.LFDI(leaf)
}

// cipherMode selects the TLS path the booted server listens on.
type cipherMode int

const (
	cipherGCM cipherMode = iota // stdlib crypto/tls, default
	cipherCCM                   // vendored gotls + CCM-8
)

// bootCfg is the resolved configuration assembled from BootOptions
// before the listener is opened. Internal, not exported.
type bootCfg struct {
	cipher        cipherMode
	stores        *server.Stores
	serverConfig  *config.Config
	clientCert    *tls.Certificate         // if nil, helper generates an ephemeral device cert
	clientCAsPath string                   // if non-empty, overrides the ClientCAs file fed to the server-side TLS config
	notifier      handler.ResourceNotifier // if nil, BootServer wires a default subscription.Manager bound to Stores.Subscriptions (#157)
}

// BootOption configures BootServer. Apply via the functional-options
// pattern; unrecognized fields fall through to the zero-value defaults.
type BootOption func(*bootCfg)

// WithCCMMode flips the booted server to the spec-compliant CCM-8
// cipher path (vendored gotls). The default is GCM, which is faster
// and good enough for most procedural tests. Use this when a test
// specifically asserts CCM-8 negotiation.
func WithCCMMode() BootOption {
	return func(c *bootCfg) { c.cipher = cipherCCM }
}

// WithStores supplies a caller-built *server.Stores. The helper takes
// ownership and does not mutate the slice. Use this with the fixture
// loader (#52) to seed topology-specific state. When omitted, the
// helper installs a fresh in-memory store set so the server boots into
// a working /dcap that returns an empty resource graph.
func WithStores(s *server.Stores) BootOption {
	return func(c *bootCfg) { c.stores = s }
}

// WithServerConfig overrides the default *config.Config. The default
// is a deterministic UTC-8 timezone block sufficient for /dcap, /tm,
// and identity-derivation flows. Pass this when a test needs a
// specific time-quality value or DST window.
func WithServerConfig(cfg *config.Config) BootOption {
	return func(c *bootCfg) { c.serverConfig = cfg }
}

// WithClientCert wires the returned Client's transport to present the
// given certificate on every request. Use this when the test drives
// the server with an external client cert (e.g. SunSpec V1.2 fixture).
// When omitted, BootServer generates an ephemeral device cert signed
// by its ephemeral CA and presents that.
func WithClientCert(cert tls.Certificate) BootOption {
	return func(c *bootCfg) { c.clientCert = &cert }
}

// WithNotifier supplies a caller-built handler.ResourceNotifier. The
// default is a fresh subscription.Manager wired to Stores.Subscriptions
// and started on a t.Cleanup-cancelled context (#157); pass an
// explicit value (including a stub) to override.
//
// The default Manager runs its worker pool on a background context that
// BootServer cancels at test teardown: workers drain and exit before
// the listener is closed.
func WithNotifier(n handler.ResourceNotifier) BootOption {
	return func(c *bootCfg) { c.notifier = n }
}

// WithClientCAsFile overrides the trust root the server uses to
// validate incoming client certs at handshake. Default: the helper
// generates an ephemeral CA and puts that in ClientCAs (so the
// helper-supplied device cert validates). Override when the test
// drives the server with an external cert chain, e.g. handshake_test
// drives with a SunSpec V1.2 leaf and must put the SunSpec roots in
// ClientCAs. The path is read at boot time by the underlying core
// pkg/sep2tls.NewCCMServerConfig / NewServerTLSConfig.
func WithClientCAsFile(path string) BootOption {
	return func(c *bootCfg) { c.clientCAsPath = path }
}

// BootedServer is the handle returned by BootServer. Lifetime is bound
// to the test via t.Cleanup; callers must not close the listener or
// the http.Server themselves.
type BootedServer struct {
	// BaseURL is the https://host:port prefix tests use to issue requests.
	BaseURL string

	// ServerCert is the ephemeral server-leaf certificate (PEM). Tests
	// that need to validate cert content (e.g. SAN, identity hash) can
	// parse this directly.
	ServerCert []byte

	// RootCA is the ephemeral CA cert (PEM) that signed ServerCert.
	// Tests building their own client config trust this via RootCAs.
	RootCA []byte

	// MountedPatterns is the protocol router's own enumeration of every
	// pattern it registered, as returned by server.BuildProtocolRouter.
	//
	// It is captured from the SAME router instance that serves this
	// server's requests, not rebuilt alongside it: a second router built
	// from the same inputs could drift from the one under test, and a
	// route-coverage claim backed by a different object than the one
	// answering the requests is not evidence. The WADL conformance sweep
	// (test/conformance/wadl) uses it as an independent cross-check on
	// what the wire reports, so a 404 can be attributed to a missing route
	// or to an empty store two separate ways rather than one.
	MountedPatterns []string

	// Stores is the live *server.Stores backing this booted server.
	// Tests can read/write through this handle to assert post-conditions
	// (e.g. side-effects of a PUT on /edev). Mutating the stores after
	// the server is serving has the same race-rules as in production:
	// callers are responsible for synchronization if they need it.
	Stores *server.Stores

	client *Client // memoized; constructed lazily by Client()

	// internal fields below
	listener net.Listener
	srv      *http.Server
	once     sync.Once
}

// Client returns a *csiptest.Client wired to this booted server.
// The underlying *http.Client trusts the server's ephemeral CA via
// RootCAs and presents the configured client cert (caller-supplied or
// ephemeral). The Client is memoized; repeated calls return the same
// instance.
func (b *BootedServer) Client() *Client {
	return b.client
}

// HTTPClient returns the underlying *http.Client used by Client(),
// already wired with the server's ephemeral CA in RootCAs and the
// configured device cert in Certificates. Use this for direct PUT /
// POST / DELETE flows that the csiptest.Client (read-only at present)
// does not cover: UTIL-002 commissioning, UTIL-003 subscription POST,
// UTIL-004 Response POST. The returned client is safe to use across
// concurrent goroutines per net/http semantics.
func (b *BootedServer) HTTPClient() *http.Client {
	return b.client.http
}

// Addr returns the listener's local address. Useful for tests that
// need to verify the server is actually torn down (post-cleanup Dial
// should fail).
func (b *BootedServer) Addr() string {
	return b.listener.Addr().String()
}

// BootServer boots an IEEE 2030.5 server in-process on a random TCP
// port and registers cleanup via t.Cleanup. The returned *BootedServer
// is ready to serve before this function returns: a /dcap GET via
// b.Client() succeeds without further setup.
//
// Hard guarantees:
//
//   - Each call gets its own listener on its own port (TCP :0).
//     Concurrent t.Parallel tests do not collide.
//   - Each call gets its own *server.Stores (unless WithStores
//     overrides). State does not leak between tests.
//   - t.Cleanup tears the server down. After cleanup, a Dial against
//     b.Addr() fails with connection-refused (or an equivalent error,
//     depending on platform).
//
// BootServer is safe to call from t.Parallel tests.
func BootServer(t *testing.T, opts ...BootOption) *BootedServer {
	t.Helper()

	cfg := bootCfg{
		cipher:       cipherGCM,
		serverConfig: defaultServerConfig(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.stores == nil {
		cfg.stores = NewFreshStores()
	}

	caCertPEM, caKeyPEM, serverCertPEM, serverKeyPEM := mustGenerateServerPKI(t)

	// If the caller did not supply a client cert, generate an ephemeral
	// device cert signed by our ephemeral CA. The Client returned by
	// b.Client() presents it.
	var clientCert tls.Certificate
	if cfg.clientCert != nil {
		clientCert = *cfg.clientCert
	} else {
		clientCert = mustGenerateDeviceCert(t, caCertPEM, caKeyPEM)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("csiptest: listen: %v", err)
	}

	httpSrv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Pick the trust root the server validates client certs against.
	// Default: our ephemeral CA (so the helper-generated device cert
	// validates). Override: caller-supplied path (e.g. SunSpec roots
	// when the test drives with a SunSpec V1.2 leaf).
	clientCAsPEM := caCertPEM
	if cfg.clientCAsPath != "" {
		raw, readErr := os.ReadFile(cfg.clientCAsPath)
		if readErr != nil {
			_ = listener.Close()
			t.Fatalf("csiptest: read client CAs %s: %v", cfg.clientCAsPath, readErr)
		}
		clientCAsPEM = raw
	}

	// Build the TLS listener AND derive the server's SFDI/LFDI from its
	// leaf cert BEFORE constructing the router. Mirrors the production
	// Run() flow fixed by #1 so /sdev and /sdev/sdi see populated
	// identity under both cipher modes. Without this, NewRouter is fed
	// empty strings and the SelfDevice handler closes over them: exactly
	// the regression #1 fixed in production but which this harness
	// did not previously replicate.
	var (
		tlsListener net.Listener
		serverSFDI  string
		serverLFDI  string
	)
	switch cfg.cipher {
	case cipherCCM:
		ccmCfg, ccmErr := newCCMConfig(t, serverCertPEM, serverKeyPEM, clientCAsPEM)
		if ccmErr != nil {
			_ = listener.Close()
			t.Fatalf("csiptest: CCM config: %v", ccmErr)
		}
		serverSFDI, serverLFDI = deriveServerIdentity(t, ccmCfg.Certificates[0].Certificate)
		tlsListener = gotls.NewListener(listener, ccmCfg)
	default:
		stdCfg, stdErr := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, clientCAsPEM)
		if stdErr != nil {
			_ = listener.Close()
			t.Fatalf("csiptest: GCM config: %v", stdErr)
		}
		serverSFDI, serverLFDI = deriveServerIdentity(t, stdCfg.Certificates[0].Certificate)
		tlsListener = tls.NewListener(listener, stdCfg)
	}

	// #157: wire a notifier so the test surface fans out Notifications.
	// The default is a real subscription.Manager bound to Stores.Subscriptions
	// Same dispatcher production uses. Manager.Start blocks on ctx.Done,
	// so we own a context tied to test teardown and cancel it from Cleanup.
	// The Manager's worker pool drains before BootServer's listener closes.
	notifier := cfg.notifier
	if notifier == nil && cfg.stores != nil && cfg.stores.Subscriptions != nil {
		mgr := coresub.NewManager(cfg.stores.Subscriptions, 2, 64, AllowLoopbackReceivers())
		notifierCtx, cancel := context.WithCancel(context.Background())
		mgrDone := make(chan struct{})
		go func() {
			defer close(mgrDone)
			mgr.Start(notifierCtx)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-mgrDone:
			case <-time.After(2 * time.Second):
				t.Logf("csiptest: subscription manager did not drain within 2s")
			}
		})
		notifier = mgr
	}

	router, mountedPatterns := server.BuildProtocolRouter(cfg.serverConfig, cfg.stores, nil, serverSFDI, serverLFDI, notifier)

	if cfg.cipher == cipherCCM {
		sepTLS.SetupCCMServer(httpSrv)
		httpSrv.Handler = sepTLS.CCMIdentityMiddleware(router)
	} else {
		httpSrv.Handler = router
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(tlsListener) }()

	baseURL := "https://" + listener.Addr().String()

	booted := &BootedServer{
		BaseURL:         baseURL,
		ServerCert:      serverCertPEM,
		RootCA:          caCertPEM,
		Stores:          cfg.stores,
		MountedPatterns: mountedPatterns,
		listener:        listener,
	}
	booted.srv = httpSrv

	// Build the http.Client. Trust the ephemeral CA via RootCAs; present
	// the resolved client cert (caller-supplied or ephemeral device).
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caCertPEM) {
		_ = httpSrv.Close()
		_ = listener.Close()
		t.Fatal("csiptest: append root CA to pool")
	}
	clientTLSCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      rootPool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
	}
	httpClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}
	booted.client = NewClient(httpClient, baseURL)

	t.Cleanup(func() {
		booted.shutdown(t, serveErr)
	})

	return booted
}

// shutdown closes the http.Server, waits for the serve goroutine to
// drain, and reports the result via t.Logf. Safe to call from a
// t.Cleanup or directly from a test. Idempotent via sync.Once.
func (b *BootedServer) shutdown(t *testing.T, serveErr <-chan error) {
	b.once.Do(func() {
		_ = b.srv.Close()
		select {
		case err := <-serveErr:
			if err != nil && err != http.ErrServerClosed {
				t.Logf("csiptest: server exited: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("csiptest: server did not shut down within 5s")
		}
	})
}

// defaultServerConfig returns a deterministic *config.Config sufficient
// for /dcap, /tm, and identity-derivation flows. Tests that need
// non-default time semantics override via WithServerConfig.
func defaultServerConfig() *config.Config {
	return &config.Config{
		TZOffset:    -28800,
		DSTOffset:   3600,
		DSTStart:    1583661600,
		DSTEnd:      1604214000,
		TimeQuality: sep2.TimeQualityNTP,
	}
}

// mustGenerateServerPKI returns (caCertPEM, caKeyPEM, serverCertPEM,
// serverKeyPEM) for an ephemeral root-CA + server-leaf pair. Both
// certs cover 127.0.0.1 + localhost. t.Fatal on any failure.
func mustGenerateServerPKI(t *testing.T) (caCertPEM, caKeyPEM, serverCertPEM, serverKeyPEM []byte) {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "CSIP Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("csiptest: generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("csiptest: parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("csiptest: parse CA key: %v", err)
	}
	serverCertPEM, serverKeyPEM, err = certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "CSIP Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("csiptest: generate server cert: %v", err)
	}
	return caCertPEM, caKeyPEM, serverCertPEM, serverKeyPEM
}

// mustGenerateDeviceCert returns a tls.Certificate for an ephemeral
// device leaf signed by the supplied CA. The cert carries the
// IEEE-2030.5 critical HardwareModuleName SAN so the server's verifier
// hook accepts it. t.Fatal on any failure.
func mustGenerateDeviceCert(t *testing.T, caCertPEM, caKeyPEM []byte) tls.Certificate {
	t.Helper()
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("csiptest: parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("csiptest: parse CA key: %v", err)
	}
	devCertPEM, devKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "CSIPTEST-DEVICE-001",
	})
	if err != nil {
		t.Fatalf("csiptest: generate device cert: %v", err)
	}
	cert, err := tls.X509KeyPair(devCertPEM, devKeyPEM)
	if err != nil {
		t.Fatalf("csiptest: parse device cert: %v", err)
	}
	return cert
}

// newCCMConfig wraps core's pkg/sep2tls.NewCCMServerConfig, which reads PEM
// material from disk. We write the in-memory PEMs into t.TempDir so
// the OS reaps them automatically when the test exits.
func newCCMConfig(t *testing.T, serverCertPEM, serverKeyPEM, caCertPEM []byte) (*gotls.Config, error) {
	t.Helper()
	dir := t.TempDir()
	serverCertFile := filepath.Join(dir, "server.crt")
	serverKeyFile := filepath.Join(dir, "server.key")
	caCertFile := filepath.Join(dir, "ca.crt")
	for _, p := range []struct {
		path string
		data []byte
	}{
		{serverCertFile, serverCertPEM},
		{serverKeyFile, serverKeyPEM},
		{caCertFile, caCertPEM},
	} {
		if err := os.WriteFile(p.path, p.data, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", filepath.Base(p.path), err)
		}
	}
	return sepTLS.NewCCMServerConfig(serverCertFile, serverKeyFile, caCertFile)
}

// NewFreshStores returns a fully-populated in-memory store set
// suitable as the default backing state for a BootServer. Each call
// returns a new, independent set: concurrent BootServer callers do
// not share store state.
//
// Exported because the fixture loader (#52) builds on top of
// this to seed topology-specific state, and Phase 3 tests that want
// to mutate stores before booting can call this themselves and pass
// the result via WithStores.
func NewFreshStores() *server.Stores {
	return &server.Stores{
		EndDevices:               memory.NewEndDeviceStore(),
		EndDeviceManagers:        memory.NewEndDeviceManagementStore(),
		MirrorUsagePoints:        memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings:      memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:                     memory.NewScopedStore[sep2.DER](),
		DERCapabilities:          memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:              memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:              memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:        memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:              memory.NewDERProgramStore(),
		DERControls:              memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		FSAs:                     memory.NewScopedStore[sep2.FunctionSetAssignments](),
		Subscriptions:            memory.NewSubscriptionStore(),
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
}
