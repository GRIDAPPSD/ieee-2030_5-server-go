package sep2srv_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2cert"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv"
)

// ccmHTTPClient wraps cfg in an *http.Client whose Transport dials through
// core's forked TLS stack (gotls), the only way to reach this package's
// CCM-8-only listener: net/http's own TLSClientConfig field only accepts a
// *tls.Config, which cannot negotiate CCM-8 at all. A zero timeout leaves
// the client unbounded, matching http.Client's own zero-value default.
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

// testCertSet holds file paths for a generated CA, server cert, and device
// (client) cert, plus the parsed server leaf so tests can compute the
// expected Identity independently of the package under test.
type testCertSet struct {
	caFile     string
	serverCert string
	serverKey  string
	deviceCert string
	deviceKey  string
	serverLeaf *x509.Certificate
}

// newTestCertSet mints a CA, a server cert, and a device cert, and writes
// them to PEM files under t.TempDir(). Mirrors the pattern already proven in
// pkg/sep2tls/config_test.go.
func newTestCertSet(t *testing.T) testCertSet {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := sep2cert.GenerateCA(sep2cert.CAOptions{
		CommonName: "sep2srv Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, caKey := parseCACert(t, caCertPEM, caKeyPEM)

	serverCertPEM, serverKeyPEM, err := sep2cert.GenerateServerCert(caCert, caKey, sep2cert.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "sep2srv Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	deviceCertPEM, deviceKeyPEM, err := sep2cert.GenerateDeviceCert(caCert, caKey, sep2cert.DeviceCertOptions{
		DeviceType:  sep2cert.DeviceTypeGeneric,
		HWSerialNum: "SEP2SRV-TEST-001",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	block, _ := pem.Decode(serverCertPEM)
	if block == nil {
		t.Fatal("decode server cert PEM")
	}
	serverLeaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server leaf: %v", err)
	}

	return testCertSet{
		caFile:     writeTempFile(t, dir, "ca.pem", caCertPEM),
		serverCert: writeTempFile(t, dir, "server.pem", serverCertPEM),
		serverKey:  writeTempFile(t, dir, "server-key.pem", serverKeyPEM),
		deviceCert: writeTempFile(t, dir, "device.pem", deviceCertPEM),
		deviceKey:  writeTempFile(t, dir, "device-key.pem", deviceKeyPEM),
		serverLeaf: serverLeaf,
	}
}

func writeTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func parseCACert(t *testing.T, certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("failed to decode CA key PEM")
	}
	keyRaw, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	ecKey, ok := keyRaw.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("CA key is not ECDSA")
	}
	return cert, ecKey
}

// echoHandler builds an http.Handler exposing the derived Identity in the
// response body, so a test can assert the handler was actually reached
// (routed response), not merely that the TLS handshake completed.
func echoHandler(id sep2srv.Identity) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dcap", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "sfdi=%s lfdi=%s", id.SFDI, id.LFDI)
	})
	return mux
}

// TestNew_MTLSAcceptAndReject is the required mTLS accept/reject pair:
// a client presenting a valid device cert completes the handshake and
// receives a routed response; a client presenting no cert at all is
// rejected at the TLS handshake before any handler runs. It also proves
// Run exits cleanly on ctx cancellation within a bounded time, with no
// leaked listener goroutine.
func TestNew_MTLSAcceptAndReject(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:            "127.0.0.1:0",
		CertFile:        certs.serverCert,
		KeyFile:         certs.serverKey,
		CAFile:          certs.caFile,
		ShutdownTimeout: 500 * time.Millisecond,
	}, echoHandler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := srv.Addr()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	waitForDial(t, addr)

	// (a) Valid client cert: handshake completes, handler runs, response
	// carries the derived identity.
	clientTLSCfg, err := sepTLS.NewCCMClientConfigFromPEM(mustRead(t, certs.deviceCert), mustRead(t, certs.deviceKey), mustRead(t, certs.caFile))
	if err != nil {
		t.Fatalf("NewCCMClientConfigFromPEM: %v", err)
	}
	client := ccmHTTPClient(clientTLSCfg, 0)

	resp, err := client.Get("https://" + addr + "/dcap")
	if err != nil {
		t.Fatalf("GET with valid client cert: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	wantBody := fmt.Sprintf("sfdi=%s lfdi=%s", srv.Identity.SFDI, srv.Identity.LFDI)
	if string(body) != wantBody {
		t.Errorf("body = %q, want %q (proves the request was routed to the handler with the derived identity)", body, wantBody)
	}

	// (b) No client cert: rejected at the TLS handshake, never reaches the
	// handler.
	noCertCfg := &gotls.Config{ //nolint:gosec // test-only: server enforces RequireAnyClientCert regardless of what the client trusts
		InsecureSkipVerify: true,
		CipherSuites:       []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		MinVersion:         gotls.VersionTLS12,
		MaxVersion:         gotls.VersionTLS12,
	}
	noCertClient := ccmHTTPClient(noCertCfg, 0)
	_, err = noCertClient.Get("https://" + addr + "/dcap")
	if err == nil {
		t.Error("expected TLS handshake to fail without a client certificate, got nil error")
	}

	// (c) Clean shutdown: cancelling ctx must return Run within the
	// configured ShutdownTimeout, and the listener must actually stop
	// accepting connections afterward (no leaked accept loop). The
	// stop-accepting check polls with a bounded deadline rather than a
	// single-shot dial: OS-level socket teardown can lag Run's return by a
	// few milliseconds, and a single-shot probe races that lag.
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run returned %v after ctx cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation; possible goroutine leak")
	}

	waitForDialFailure(t, addr)
}

// TestNew_RefusesGCMOnlyClient is #709 fix round 1: nothing in the tree
// asserted the listener's own suite list rejects GCM, so an accidental
// re-addition would go undetected (every other cipher assertion dials with
// a client that offers CCM-8 alone, which proves CCM-8 is offered but not
// that nothing else is). A client offering only GCM, with a device cert the
// server would otherwise accept, must be refused at cipher negotiation,
// before client authentication is ever reached.
func TestNew_RefusesGCMOnlyClient(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:            "127.0.0.1:0",
		CertFile:        certs.serverCert,
		KeyFile:         certs.serverKey,
		CAFile:          certs.caFile,
		ShutdownTimeout: 500 * time.Millisecond,
	}, echoHandler)
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

	waitForDial(t, addr)

	cert, err := gotls.X509KeyPair(mustRead(t, certs.deviceCert), mustRead(t, certs.deviceKey))
	if err != nil {
		t.Fatalf("gotls.X509KeyPair: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(mustRead(t, certs.caFile)) {
		t.Fatal("failed to parse CA into root pool")
	}
	gcmOnlyCfg := &gotls.Config{
		RootCAs:      caPool,
		Certificates: []gotls.Certificate{cert},
		CipherSuites: []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		MinVersion:   gotls.VersionTLS12,
		MaxVersion:   gotls.VersionTLS12,
	}
	gcmClient := ccmHTTPClient(gcmOnlyCfg, 2*time.Second)
	if _, err := gcmClient.Get("https://" + addr + "/dcap"); err == nil {
		t.Error("GCM-only client was accepted; want a handshake failure (server must offer CCM-8 only)")
	}
}

// TestNew_RefusedHandshakeIsLogged is #709 fix round 1, item 4: a
// *gotls.Conn handshakes lazily on its first Read, which net/http's own
// "TLS handshake error" logging never fires for, so a refused handshake on
// this listener produced no log line before wrapMTLS wrapped it in
// sepTLS.WrapCCMListener. Not parallel: it redirects the process-wide
// default logger, which is what a nil errorLog resolves to.
func TestNew_RefusedHandshakeIsLogged(t *testing.T) {
	certs := newTestCertSet(t)

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:            "127.0.0.1:0",
		CertFile:        certs.serverCert,
		KeyFile:         certs.serverKey,
		CAFile:          certs.caFile,
		ShutdownTimeout: 500 * time.Millisecond,
	}, echoHandler)
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

	waitForDial(t, addr)

	// A plain bytes.Buffer races: the handshake goroutine writes to it
	// through log.Default() while this test reads it below, and both can
	// still be live in the Cleanup window (a late handshake attempt during
	// ctx cancellation). logBuf serializes both sides.
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

	// The handshake goroutine logs asynchronously; poll rather than reading
	// logBuf immediately after the client's Get returns.
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
	t.Logf("refused handshake logged %d bytes: %q", len(got), got)
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

func TestNew_CCM_IdentityAndLifecycle(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)
	wantSFDI := sepTLS.SFDI(certs.serverLeaf)
	wantLFDI := sepTLS.LFDI(certs.serverLeaf)

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:            "127.0.0.1:0",
		CertFile:        certs.serverCert,
		KeyFile:         certs.serverKey,
		CAFile:          certs.caFile,
		ShutdownTimeout: 500 * time.Millisecond,
	}, echoHandler)
	if err != nil {
		t.Fatalf("New (CCM): %v", err)
	}

	if srv.Identity.SFDI != wantSFDI || srv.Identity.LFDI != wantLFDI {
		t.Errorf("CCM identity = %+v, want SFDI=%q LFDI=%q", srv.Identity, wantSFDI, wantLFDI)
	}

	addr := srv.Addr()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	waitForDial(t, addr)

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run returned %v after ctx cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation (CCM); possible goroutine leak")
	}

	waitForDialFailure(t, addr)
}

// TestServer_Run_ShutdownTimeoutError proves Run's bounded-drain guarantee:
// a handler that outlives ShutdownTimeout makes Run return the wrapped
// shutdown error within bounds, rather than hanging on an unbounded
// Shutdown. This is the reachable half of the two Run branches previously
// (and wrongly) reported as defensive-only unreachable code; the stuck
// handler here drives Run's Shutdown(shutdownCtx) call past its deadline.
func TestServer_Run_ShutdownTimeoutError(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)

	handlerStarted := make(chan struct{})
	release := make(chan struct{})
	stuckHandler := func(sep2srv.Identity) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(handlerStarted)
			<-release // blocks well past ShutdownTimeout so Shutdown cannot drain gracefully
			w.WriteHeader(http.StatusOK)
		})
	}

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:            "127.0.0.1:0",
		CertFile:        certs.serverCert,
		KeyFile:         certs.serverKey,
		CAFile:          certs.caFile,
		ShutdownTimeout: 100 * time.Millisecond,
	}, stuckHandler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := srv.Addr()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	waitForDial(t, addr)

	clientTLSCfg, err := sepTLS.NewCCMClientConfigFromPEM(mustRead(t, certs.deviceCert), mustRead(t, certs.deviceKey), mustRead(t, certs.caFile))
	if err != nil {
		t.Fatalf("NewCCMClientConfigFromPEM: %v", err)
	}
	client := ccmHTTPClient(clientTLSCfg, 0)

	reqDone := make(chan error, 1)
	go func() {
		resp, getErr := client.Get("https://" + addr + "/dcap")
		if resp != nil {
			_ = resp.Body.Close()
		}
		reqDone <- getErr
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("stuck handler never started")
	}

	cancel()

	// Run must return within bounds even though the handler is still
	// blocked: this is the "process does not hang" assertion.
	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("Run returned nil for a stuck handler that outlives ShutdownTimeout, want a shutdown error")
		}
		if !strings.Contains(err.Error(), "sep2srv: shutdown:") {
			t.Errorf("Run error = %q, want it to wrap the shutdown timeout as \"sep2srv: shutdown: ...\"", err.Error())
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run error = %v, want errors.Is(err, context.DeadlineExceeded)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of a stuck handler outliving ShutdownTimeout; Run hung")
	}

	// Unblock the stuck handler so its goroutine and the client's pending
	// request do not outlive the test.
	close(release)
	select {
	case <-reqDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stuck request never completed after release; handler goroutine leaked")
	}
}

func TestNew_ValidationErrors(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)

	tests := []struct {
		name  string
		opts  sep2srv.Options
		build sep2srv.HandlerFunc
	}{
		{
			name:  "empty addr",
			opts:  sep2srv.Options{CertFile: certs.serverCert, KeyFile: certs.serverKey, CAFile: certs.caFile},
			build: echoHandler,
		},
		{
			name:  "nil build",
			opts:  sep2srv.Options{Addr: "127.0.0.1:0", CertFile: certs.serverCert, KeyFile: certs.serverKey, CAFile: certs.caFile},
			build: nil,
		},
		{
			name:  "missing cert file",
			opts:  sep2srv.Options{Addr: "127.0.0.1:0", CertFile: "/nonexistent/cert.pem", KeyFile: certs.serverKey, CAFile: certs.caFile},
			build: echoHandler,
		},
		{
			name:  "build returns nil handler",
			opts:  sep2srv.Options{Addr: "127.0.0.1:0", CertFile: certs.serverCert, KeyFile: certs.serverKey, CAFile: certs.caFile},
			build: func(sep2srv.Identity) http.Handler { return nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, err := sep2srv.New(tt.opts, tt.build)
			if err == nil {
				t.Fatalf("New(%s) succeeded, want error", tt.name)
			}
			if srv != nil {
				t.Errorf("New(%s) returned a non-nil Server alongside the error", tt.name)
			}
		})
	}
}

func TestNew_BadAddr_ListenError(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)
	_, err := sep2srv.New(sep2srv.Options{
		Addr:     "not-a-valid-addr:::",
		CertFile: certs.serverCert,
		KeyFile:  certs.serverKey,
		CAFile:   certs.caFile,
	}, echoHandler)
	if err == nil {
		t.Fatal("New with an invalid Addr succeeded, want a listen error")
	}
}

// waitForDial polls addr until a plain TCP dial succeeds, bounding the wait
// so tests fail fast instead of hanging if Run never starts serving.
func waitForDial(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never accepted a connection within the deadline", addr)
}

// getWithRetry absorbs the gap between Run being called and Serve accepting,
// the same way waitForDial does, but without waitForDial's bare TCP dial: a
// Capture-wrapped listener records that dial as its own short-lived, failed
// connection, which a test asserting on the recorded exchange set should not
// have to account for. Mirrors pkg/sep2server's helper of the same name.
func getWithRetry(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s never succeeded: %v", url, lastErr)
	return nil
}

// waitForDialFailure polls addr until a plain TCP dial fails, bounding the
// wait so the "listener stopped accepting" assertion is deterministic
// instead of racing OS-level socket teardown. A single-shot dial right
// after Run returns can observe the listener as still-open for a few
// milliseconds after Shutdown closed it; polling to a bounded deadline is
// the inverse of waitForDial and closes that race.
func waitForDialFailure(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener at %s was still accepting connections after the deadline; possible leaked accept loop", addr)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
