package sep2server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
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
