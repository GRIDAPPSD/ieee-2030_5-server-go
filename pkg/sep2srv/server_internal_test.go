package sep2srv

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestDeriveIdentity_EmptyChain and TestDeriveIdentity_MalformedLeaf cover
// the two deriveIdentity error branches that are not reachable through the
// public New API: a valid tls.Certificate always carries a non-empty,
// parseable Certificates[0].Certificate once LoadX509KeyPair has succeeded.
// These remain the only genuinely unreachable-via-New branches in this
// package; Run's shutdown-error and errCh-return branches are exercised
// below and in server_test.go's stuck-handler test.
func TestDeriveIdentity_EmptyChain(t *testing.T) {
	t.Parallel()
	_, err := deriveIdentity(nil)
	if err == nil {
		t.Fatal("deriveIdentity(nil) succeeded, want error")
	}
}

func TestDeriveIdentity_MalformedLeaf(t *testing.T) {
	t.Parallel()
	_, err := deriveIdentity([][]byte{[]byte("not a certificate")})
	if err == nil {
		t.Fatal("deriveIdentity(malformed) succeeded, want error")
	}
}

// TestServer_Run_ReturnsServeErrorWhenListenerFailsBeforeCancel exercises
// Run's errCh branch (Serve fails before ctx is cancelled) for a plain
// non-ErrServerClosed failure, the sibling of the ctx.Done branch already
// covered end-to-end by the public mTLS lifecycle tests. Closing the
// listener out from under Serve is the simplest deterministic way to
// trigger that branch without a live client.
func TestServer_Run_ReturnsServeErrorWhenListenerFailsBeforeCancel(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv := &Server{
		listener: listener,
		httpSrv: &http.Server{
			Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		},
		shutdownTimeout: time.Second,
	}

	// Close the listener before Run ever calls Serve, so Serve fails
	// immediately with a "use of closed network connection" error rather
	// than the http.ErrServerClosed that a Shutdown-triggered close
	// produces. Run's ctx is never cancelled in this test: only the errCh
	// branch should fire.
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("listener.Close: %v", closeErr)
	}

	err = srv.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil after Serve failed on a pre-closed listener, want error")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Run returned http.ErrServerClosed, want a plain listener-closed error (Shutdown was never called)")
	}
}

// TestServer_Run_ReturnsErrServerClosedFromErrCh covers Run's other errCh
// outcome: Serve returning http.ErrServerClosed itself (rather than a plain
// listener-closed error) arrives on the errCh case, not the ctx.Done case,
// whenever the server is closed by something other than Run's own
// ctx-triggered Shutdown call. Closing httpSrv directly before Serve ever
// runs reproduces that ordering deterministically: Close marks the server
// as shutting down, so Serve's first Accept fails and it returns
// ErrServerClosed immediately.
func TestServer_Run_ReturnsErrServerClosedFromErrCh(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	httpSrv := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	srv := &Server{
		listener:        listener,
		httpSrv:         httpSrv,
		shutdownTimeout: time.Second,
	}

	if closeErr := httpSrv.Close(); closeErr != nil {
		t.Fatalf("httpSrv.Close: %v", closeErr)
	}

	// ctx is never cancelled: Run must reach the errCh case, not ctx.Done.
	err = srv.Run(context.Background())
	if !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Run returned %v, want errors.Is(err, http.ErrServerClosed) via the errCh branch", err)
	}
}
