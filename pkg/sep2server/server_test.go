package sep2server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// TestProtocolServerTimeoutsSet asserts every timeout on the protocol
// http.Server is non-zero. A zero timeout means no limit, which is a Slowloris
// and slow-body exhaustion surface. This is the assertion that lived in
// internal/server's timeout_test.go before the protocol server moved
// here; the guarantee is unchanged.
func TestProtocolServerTimeoutsSet(t *testing.T) {
	t.Parallel()

	srv := newProtocolServer(nil)

	// The values are taken from core's sep2srv.Default*Timeout, which core
	// lifted from this repository's own internal/server verbatim. They are
	// asserted as literals rather than against those constants so that a
	// change on core's side surfaces HERE as a deliberate decision about this
	// server's wire posture, instead of silently retiming the protocol
	// listener on the next dependency bump.
	for _, tc := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, 10 * time.Second},
		{"ReadTimeout", srv.ReadTimeout, 30 * time.Second},
		{"WriteTimeout", srv.WriteTimeout, 30 * time.Second},
		{"IdleTimeout", srv.IdleTimeout, 120 * time.Second},
	} {
		if tc.got == 0 {
			t.Errorf("protocol server %s is 0 (no limit)", tc.name)
			continue
		}
		if tc.got != tc.want {
			t.Errorf("protocol server %s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if srv.ReadHeaderTimeout >= srv.ReadTimeout {
		t.Errorf("ReadHeaderTimeout (%v) should be shorter than ReadTimeout (%v)",
			srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
}

// TestNewRejectsIncompleteConfig pins the fail-closed construction contract.
//
// The nil-Auth.Wrap case is the one that matters most: core tolerates it with
// a log line, so without this refusal a deployment could reach a fully served
// protocol surface with no identity extraction and no ACL by simply leaving a
// field unset. Each case asserts on the message, not just on non-nil, so a
// future refactor cannot satisfy the test by failing for a different reason.
func TestNewRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()

	complete := func() Config {
		return Config{
			Addr:     "127.0.0.1:0",
			CertFile: "cert.pem",
			KeyFile:  "key.pem",
			CAFile:   "ca.pem",
			Auth:     DefaultAuthPolicy(),
		}
	}

	tests := []struct {
		name     string
		mutate   func(*Config)
		wantText string
	}{
		{"no addr", func(c *Config) { c.Addr = "" }, "Config.Addr is required"},
		{"no cert", func(c *Config) { c.CertFile = "" }, "are all required"},
		{"no key", func(c *Config) { c.KeyFile = "" }, "are all required"},
		{"no ca", func(c *Config) { c.CAFile = "" }, "are all required"},
		{"nil auth wrap", func(c *Config) { c.Auth.Wrap = nil }, "no ACL enforcement"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := complete()
			tc.mutate(&cfg)

			srv, err := New(cfg)
			if err == nil {
				t.Fatalf("New succeeded with %s; expected a refusal", tc.name)
			}
			if srv != nil {
				t.Errorf("New returned a non-nil Server alongside an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantText)
			}
		})
	}
}

// TestBuildHandlerMiddlewareIsOutermost pins the documented chain order:
// Config.Middleware wraps everything, including the auth policy and the CCM
// identity layer between them.
//
// The relative position is a contract rather than an accident. The CCM layer
// is what populates r.TLS from the forked connection, so anything that needs
// peer certificates has to sit inside it, and the auth policy does. Config
// documents Middleware as outermost, so an instrumentation wrapper sees every
// request including ones the auth policy goes on to refuse.
func TestBuildHandlerMiddlewareIsOutermost(t *testing.T) {
	t.Parallel()

	var seen []string

	cfg := Config{
		Auth: recordingAuth(&seen),
		Middleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, "middleware")
				next.ServeHTTP(w, r)
			})
		},
	}

	handler, _ := BuildHandler(cfg, sep2srv.Identity{SFDI: "111111111111", LFDI: "aabb"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil))

	want := []string{"middleware", "auth"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("composition order: got %v, want %v (Middleware must be outermost)", seen, want)
	}
}

// recordingAuth is a pass-through auth policy whose Wrap notes that it ran.
// Wrap is the innermost layer BuildHandler installs, because core composes it
// directly around the protocol mux, so observing it is how a test learns where
// the outer layers sat relative to the router.
func recordingAuth(seen *[]string) assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				*seen = append(*seen, "auth")
				next.ServeHTTP(w, r)
			})
		},
	}
}

// TestBuildHandlerServesTheProtocolSurface asserts BuildHandler produces a
// handler that actually answers a protocol request with a real body, and that
// it reports the same pattern list the handler is serving.
//
// Serving /dcap and reading the payload, rather than only asserting a 200,
// is the point: a handler that routes but returns an empty document would pass
// a status-only check and be useless on the wire.
func TestBuildHandlerServesTheProtocolSurface(t *testing.T) {
	t.Parallel()

	var seen []string
	handler, patterns := BuildHandler(Config{Auth: recordingAuth(&seen)},
		sep2srv.Identity{SFDI: "123456789012", LFDI: "aabbccddee"})

	if len(patterns) == 0 {
		t.Fatal("BuildHandler reported no mounted patterns")
	}
	var sawDCap bool
	for _, p := range patterns {
		if p == "GET /dcap" {
			sawDCap = true
		}
	}
	if !sawDCap {
		t.Fatalf("patterns do not include \"GET /dcap\": %v", patterns)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dcap: status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DeviceCapability") {
		t.Errorf("GET /dcap body is not a DeviceCapability document: %q", body)
	}
}

// TestDefaultAuthPolicyRefusesAnUnauthenticatedRequest asserts the policy this
// package hands embedders actually denies a request that carries no client
// identity.
//
// This is the whole reason DefaultAuthPolicy is exported: an embedder that had
// to reimplement enforcement would end up with a second, divergent ACL, which
// is the failure this package exists to retire. A policy that let an
// identity-free request through would be worse than no policy, because it
// would look like enforcement.
func TestDefaultAuthPolicyRefusesAnUnauthenticatedRequest(t *testing.T) {
	t.Parallel()

	handler, _ := BuildHandler(Config{Auth: DefaultAuthPolicy()},
		sep2srv.Identity{SFDI: "123456789012", LFDI: "aabbccddee"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("GET /dcap with no client identity returned 200; DefaultAuthPolicy is not enforcing")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /dcap with no client identity: status %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestBuildHandlerDefaultsStores asserts a nil Config.Stores is filled from
// NewStores rather than nil-panicking or quietly serving a smaller surface.
func TestBuildHandlerDefaultsStores(t *testing.T) {
	t.Parallel()

	withNil, nilPatterns := BuildHandler(Config{Auth: DefaultAuthPolicy()}, sep2srv.Identity{})
	withSet, setPatterns := BuildHandler(Config{Auth: DefaultAuthPolicy(), Stores: NewStores()}, sep2srv.Identity{})

	if withNil == nil || withSet == nil {
		t.Fatal("BuildHandler returned a nil handler")
	}
	if !reflect.DeepEqual(nilPatterns, setPatterns) {
		t.Errorf("a nil Config.Stores serves a different route surface than an explicit one:\n nil: %v\n set: %v",
			nilPatterns, setPatterns)
	}
}

// TestServerLifecycle drives the whole surface the way an embedder does: build
// the TLS material, construct, read the identity and address back, serve a
// real mutual-TLS request, then cancel and confirm the drain returns cleanly.
func TestServerLifecycle(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	stores := NewStores()
	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
		Stores:   stores,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Identity is derived from the leaf, and both halves must be populated:
	// /sdev and /sdev/sdi close over them, and an empty value there is the
	// #1 regression.
	if got := srv.Identity(); got.SFDI == "" || got.LFDI == "" {
		t.Errorf("Identity is not fully populated: %+v", got)
	}
	if got, want := srv.Identity().SFDI, material.wantSFDI; got != want {
		t.Errorf("Identity.SFDI: got %q, want %q", got, want)
	}
	if got, want := srv.Identity().LFDI, material.wantLFDI; got != want {
		t.Errorf("Identity.LFDI: got %q, want %q", got, want)
	}

	// The store accessor must hand back the very handle that was supplied,
	// not a copy: a consumer that seeds through it is writing to the stores
	// the handlers read.
	if srv.Stores() != stores {
		t.Error("Stores() returned a different handle than Config.Stores")
	}

	// Criterion 3: a seeding path holding srv.Stores() (the write handle)
	// and a telemetry path holding srv.ReaderStores() (the read handle) both
	// compile against the same, real, running server, and the telemetry
	// path sees what the seeding path wrote. SFDI is the distinguishing
	// field: a Get returning a zero-valued record for a present id would
	// leave a bare err == nil check green.
	if err := srv.Stores().EndDevices.Create(context.Background(), "seeded-1", sep2.EndDevice{SFDI: "seeded-sfdi-2"}); err != nil {
		t.Fatalf("seed through the write handle: %v", err)
	}
	if got, err := srv.ReaderStores().EndDevices.Get(context.Background(), "seeded-1"); err != nil || got.SFDI != "seeded-sfdi-2" {
		t.Errorf("telemetry path (ReaderStores) does not see a device seeded through the write handle: got SFDI %q, err %v", got.SFDI, err)
	}

	if len(srv.Patterns()) == 0 {
		t.Error("Patterns() is empty")
	}
	if srv.Handler() == nil {
		t.Error("Handler() is nil")
	}

	addr := srv.Addr()
	if addr == "" || strings.HasSuffix(addr, ":0") {
		t.Fatalf("Addr() did not report a resolved port: %q", addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	client := ccmHTTPClient(material.clientTLS, 5*time.Second)
	resp := getWithRetry(t, client, "https://"+addr+"/dcap")
	if resp != http.StatusOK {
		t.Errorf("GET /dcap over mTLS: status %d, want 200", resp)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run returned %v after a cancelled context; a clean drain returns nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s of cancellation")
	}
}

// TestRunReportsListenerFailure asserts Run surfaces a listener failure rather
// than reporting it as a clean stop. A caller that reads nil as "asked to
// stop" would otherwise treat a broken listener as an orderly shutdown.
func TestRunReportsListenerFailure(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Close the listener out from under Run. Serve then returns a non-nil,
	// non-ErrServerClosed error, which is the failure path under test.
	if err := srv.listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	err = srv.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil for a failed listener; a failure must not look like a clean stop")
	}
	if !strings.Contains(err.Error(), "sep2server: serve") {
		t.Errorf("error %q is not identified as a serve failure", err.Error())
	}
}

// TestNewClosesTheListenerOnTLSFailure asserts a failed construction leaves no
// bound port behind. Without this, a retry loop around New leaks a socket per
// attempt and eventually cannot bind at all.
func TestNewClosesTheListenerOnTLSFailure(t *testing.T) {
	t.Parallel()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("close probe: %v", err)
	}

	dir := t.TempDir()
	_, err = New(Config{
		Addr:     addr,
		CertFile: filepath.Join(dir, "absent-cert.pem"),
		KeyFile:  filepath.Join(dir, "absent-key.pem"),
		CAFile:   filepath.Join(dir, "absent-ca.pem"),
		Auth:     DefaultAuthPolicy(),
	})
	if err == nil {
		t.Fatal("New succeeded with absent TLS material")
	}

	// The port must be free again. If New leaked the listener this rebind
	// fails with "address already in use".
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("New leaked its listener: rebinding %s failed: %v", addr, err)
	}
	_ = rebound.Close()
}

// --- helpers ---

type tlsMaterial struct {
	caFile   string
	certFile string
	keyFile  string

	clientTLS *gotls.Config

	wantSFDI string
	wantLFDI string
}

// writeTLSMaterial mints a CA, a server leaf and a device leaf, writes the
// three files New needs, and computes the SFDI/LFDI the server must report.
func writeTLSMaterial(t *testing.T) tlsMaterial {
	t.Helper()

	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "sep2server Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}

	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "sep2server Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "sep2server-test",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	m := tlsMaterial{
		caFile:   filepath.Join(dir, "ca.pem"),
		certFile: filepath.Join(dir, "server.pem"),
		keyFile:  filepath.Join(dir, "server-key.pem"),
	}
	for path, data := range map[string][]byte{
		m.caFile:   caCertPEM,
		m.certFile: serverCertPEM,
		m.keyFile:  serverKeyPEM,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	serverLeaf, err := certs.ParseCertificatePEM(serverCertPEM)
	if err != nil {
		t.Fatalf("parse server leaf: %v", err)
	}
	m.wantSFDI = sepTLS.SFDI(serverLeaf)
	m.wantLFDI = sepTLS.LFDI(serverLeaf)

	m.clientTLS, err = sepTLS.NewCCMClientConfigFromPEM(deviceCertPEM, deviceKeyPEM, caCertPEM)
	if err != nil {
		t.Fatalf("NewCCMClientConfigFromPEM: %v", err)
	}

	return m
}

// ccmHTTPClient wraps cfg in an *http.Client whose Transport dials through
// core's forked TLS stack (gotls), the only way to reach this package's
// CCM-8-only listener: net/http's own TLSClientConfig field only accepts a
// *tls.Config, which cannot negotiate CCM-8 at all.
func ccmHTTPClient(cfg *gotls.Config, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&gotls.Dialer{Config: cfg}).DialContext(ctx, network, addr)
			},
		},
	}
}

// getWithRetry absorbs the gap between Run being called and Serve accepting.
func getWithRetry(t *testing.T, client *http.Client, url string) int {
	t.Helper()

	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		resp, err := client.Get(url)
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s never succeeded: %v", url, lastErr)
	return 0
}
