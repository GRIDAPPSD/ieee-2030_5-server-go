package sep2server

import (
	"bytes"
	"context"
	"crypto/x509"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// TestNewUnderCCM asserts the CCM-8 construction path derives the correct
// server identity from the leaf cert and binds a usable listener.
//
// The identity assertion is the point: CCM runs through core's forked
// crypto/tls, a separate config-building path with its own certificate
// plumbing, and #1 was exactly a case where identity derivation on this
// path served empty SFDI/LFDI.
func TestNewUnderCCM(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	ccmSrv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if ccmSrv.Identity().SFDI != material.wantSFDI || ccmSrv.Identity().LFDI != material.wantLFDI {
		t.Errorf("CCM identity: got %+v, want SFDI %q LFDI %q",
			ccmSrv.Identity(), material.wantSFDI, material.wantLFDI)
	}
	if ccmSrv.Addr() == "" {
		t.Error("CCM server reported no bound address")
	}
	if len(ccmSrv.Patterns()) == 0 {
		t.Error("CCM server reported no mounted patterns")
	}

	// Drain so the listener is not left bound past the test.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ccmSrv.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v on a cancelled context", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Run did not return within 10s")
	}
}

// TestRunHonoursShutdownTimeout asserts a bounded drain is actually applied.
//
// Zero means unbounded, which is what this server has always done, so the
// bounded branch is the one an embedder opts into and the one that has never
// been exercised. A handler is held open past the bound and Run must still
// return rather than waiting on it forever.
func TestRunHonoursShutdownTimeout(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })

	inFlight := make(chan struct{}, 1)

	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
		Middleware: func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case inFlight <- struct{}{}:
				default:
				}
				<-release
				w.WriteHeader(http.StatusOK)
			})
		},
		ShutdownTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	client := ccmHTTPClient(material.clientTLS, 30*time.Second)
	go func() {
		resp, reqErr := client.Get("https://" + srv.Addr() + "/dcap")
		if reqErr == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the held handler was never reached")
	}

	// Cancel while a request is deliberately stuck. An unbounded drain would
	// block here until the handler released; the bound must cut it loose.
	cancel()
	select {
	case err := <-runErr:
		// Shutdown reports the deadline it could not meet. What matters is
		// that Run RETURNED rather than waiting on the stuck handler.
		if err == nil {
			t.Log("drain completed within the bound")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s despite a 250ms ShutdownTimeout: the bound is not applied")
	}

	once.Do(func() { close(release) })
}

// TestConnStateHookFires asserts Config.ConnState observes real connection
// transitions on the protocol listener.
//
// This is the wire the standalone server's TLS-handshake metric rides on. A
// hook that was accepted and then never called would leave that metric flat at
// zero, which reads as "no connections" rather than as "not wired", so it is
// worth an assertion rather than trust.
func TestConnStateHookFires(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	var (
		mu     sync.Mutex
		states []http.ConnState
	)

	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
		ConnState: func(_ net.Conn, state http.ConnState) {
			mu.Lock()
			states = append(states, state)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	client := ccmHTTPClient(material.clientTLS, 5*time.Second)
	if code := getWithRetry(t, client, "https://"+srv.Addr()+"/dcap"); code != http.StatusOK {
		t.Fatalf("GET /dcap: status %d, want 200", code)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(states) == 0 {
		t.Fatal("ConnState hook was never called for a completed request")
	}
	var sawNew, sawActive bool
	for _, s := range states {
		switch s {
		case http.StateNew:
			sawNew = true
		case http.StateActive:
			sawActive = true
		}
	}
	if !sawNew || !sawActive {
		t.Errorf("ConnState saw %v; expected at least StateNew and StateActive", states)
	}
}

// TestNew_RefusesNonCCM8Suites is #711: sep2srv's identically named test
// pins the property that this package's own wrapMTLS shares, but sep2srv is
// the twin internal/server never constructs. Each case offers exactly one
// non-CCM-8 suite with a device cert the server would otherwise accept, and
// must be refused at cipher negotiation before client authentication is ever
// reached. The final case is the control: the same client offering CCM-8
// must be accepted, so a defect that made the listener refuse everything
// would not read as this guard passing.
func TestNew_RefusesNonCCM8Suites(t *testing.T) {
	t.Parallel()

	material := writeTLSMaterial(t)

	srv, err := New(Config{
		Addr:            "127.0.0.1:0",
		CertFile:        material.certFile,
		KeyFile:         material.keyFile,
		CAFile:          material.caFile,
		Auth:            DefaultAuthPolicy(),
		ShutdownTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := srv.Addr()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return within 2s of ctx cancellation")
		}
	})

	cert, err := gotls.X509KeyPair(material.deviceCertPEM, material.deviceKeyPEM)
	if err != nil {
		t.Fatalf("gotls.X509KeyPair: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(material.caCertPEM) {
		t.Fatal("failed to parse CA into root pool")
	}

	dial := func(t *testing.T, suite uint16) error {
		t.Helper()
		cfg := &gotls.Config{
			RootCAs:      caPool,
			Certificates: []gotls.Certificate{cert},
			CipherSuites: []uint16{suite},
			MinVersion:   gotls.VersionTLS12,
			MaxVersion:   gotls.VersionTLS12,
		}
		client := ccmHTTPClient(cfg, 2*time.Second)
		_, err := client.Get("https://" + addr + "/dcap")
		return err
	}

	for _, tc := range []struct {
		name  string
		suite uint16
	}{
		{"AES-128-GCM", gotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		{"AES-256-GCM", gotls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384},
		{"AES-128-CBC", gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := dial(t, tc.suite); err == nil {
				t.Errorf("%s-only client was accepted; want a handshake failure (server must offer CCM-8 only)", tc.name)
			}
		})
	}

	t.Run("every suite this package can name except CCM-8 is refused at once", func(t *testing.T) {
		// Enumerating three suites is the defect #711 records: a fourth,
		// unlisted suite added to the vendored config would pass the
		// three cases above untested. Offering every ID gotls advertises
		// (its full catalog minus CCM-8, which appears in neither list)
		// closes that gap: any single suite the config gains beyond
		// CCM-8 alone is somewhere in this set, so the assertion holds
		// against an addition this test's author did not anticipate, not
		// only against the three named above.
		var suites []uint16
		for _, c := range gotls.CipherSuites() {
			suites = append(suites, c.ID)
		}
		for _, c := range gotls.InsecureCipherSuites() {
			suites = append(suites, c.ID)
		}
		cfg := &gotls.Config{
			RootCAs:      caPool,
			Certificates: []gotls.Certificate{cert},
			CipherSuites: suites,
			MinVersion:   gotls.VersionTLS12,
			MaxVersion:   gotls.VersionTLS12,
		}
		client := ccmHTTPClient(cfg, 2*time.Second)
		if _, err := client.Get("https://" + addr + "/dcap"); err == nil {
			t.Error("a client offering every known non-CCM-8 suite was accepted; want a handshake failure")
		}
	})

	t.Run("control: CCM-8 is still accepted", func(t *testing.T) {
		if err := dial(t, gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8); err != nil {
			t.Errorf("CCM-8 client was refused: %v; want acceptance (this proves the guard rejects on suite, not on every dial)", err)
		}
	})
}

// TestNew_RefusedHandshakeIsLogged is #709 fix round 2, item 1: the coverage
// lane found that this package's own wrapMTLS call (server.go, the sibling of
// sep2srv's wrapMTLS) had no test proving it, even though it is the copy
// internal/server.Run actually starts. sep2srv/server_test.go's test of the
// same name covers only the sep2srv package's listener; this pins the one
// that ships. See TestNew_RefusedHandshakeIsLogged in
// github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv for why the
// assertion is on a log line rather than the client error: a *gotls.Conn
// handshakes lazily on its first Read, which net/http's own "TLS handshake
// error" logging never fires for.
func TestNew_RefusedHandshakeIsLogged(t *testing.T) {
	material := writeTLSMaterial(t)

	srv, err := New(Config{
		Addr:            "127.0.0.1:0",
		CertFile:        material.certFile,
		KeyFile:         material.keyFile,
		CAFile:          material.caFile,
		Auth:            DefaultAuthPolicy(),
		ShutdownTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := srv.Addr()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return within 2s of ctx cancellation")
		}
	})

	// Not t.Parallel(): this redirects the process-wide default logger, which
	// is what a nil errorLog resolves to.
	logBuf := &syncBuffer{}
	prevOut := log.Default().Writer()
	prevFlags := log.Default().Flags()
	log.Default().SetOutput(logBuf)
	log.Default().SetFlags(0)
	t.Cleanup(func() {
		log.Default().SetOutput(prevOut)
		log.Default().SetFlags(prevFlags)
	})

	noCertCfg := &gotls.Config{ //nolint:gosec // test-only: server enforces RequireAnyClientCert regardless of what the client trusts
		InsecureSkipVerify: true,
		CipherSuites:       []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		MinVersion:         gotls.VersionTLS12,
		MaxVersion:         gotls.VersionTLS12,
	}
	noCertClient := ccmHTTPClient(noCertCfg, 2*time.Second)
	if _, err := noCertClient.Get("https://" + addr + "/dcap"); err == nil {
		t.Fatal("expected the handshake to fail without a client certificate")
	}

	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = logBuf.String()
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) == 0 {
		t.Fatal("refused handshake produced 0 bytes of log output; want a logged TLS handshake error")
	}
	if !strings.Contains(got, "TLS handshake error") {
		t.Errorf("log output = %q, want it to contain %q", got, "TLS handshake error")
	}
}

// syncBuffer is a bytes.Buffer safe for one writer goroutine (the handshake
// logger) and one reader goroutine (the test's poll loop) at once. The
// standard library type is not: -race flags the unguarded case.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
