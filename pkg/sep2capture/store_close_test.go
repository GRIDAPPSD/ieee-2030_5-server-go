package sep2capture

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestStoreCloseIsIdempotent is Q7 item 3's close requirement: safe to
// call twice. Mutant (store.go, Close): removing closeOnce.Do's guard (so
// both close(s.writeCh) calls run directly) makes this RED with a panic
// ("close of closed channel") instead of two nil returns.
func TestStoreCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st.Record(makeExchange(1, 1, "client-1", 100, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestStoreCloseBoundedByContextCountsAbandoned proves Close is bounded by
// its context even when the writer is stalled, and that what it could not
// flush is counted rather than silently lost.
//
// Mutant (store.go, Close): replacing the `case <-ctx.Done():` branch with
// nothing (so Close always waits for writerDone) makes this RED: Close
// blocks past the test's own timeout instead of returning within ctx's
// budget.
func TestStoreCloseBoundedByContextCountsAbandoned(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	st.testBeforeWrite = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	// Release and fully close (bounded) rather than just unblocking and
	// leaving the writer goroutine to finish on its own time: t.TempDir()
	// removes its directory once every earlier-registered cleanup has
	// run, and an in-flight write racing that removal is exactly the
	// noisy-but-harmless log line this ordering avoids.
	t.Cleanup(func() {
		close(release)
		closeStore(t, st)
	})

	st.Record(makeExchange(1, 1, "client-1", 100, 100))
	<-entered
	st.Record(makeExchange(2, 2, "client-1", 100, 100))
	st.Record(makeExchange(3, 3, "client-1", 100, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = st.Close(ctx)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("Close took %v with the writer stalled, want it bounded near ctx's 150ms budget", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error: got %v, want context.DeadlineExceeded", err)
	}
	if got := st.Stats().Abandoned; got == 0 {
		t.Error("Abandoned: got 0, want > 0 (records 2 and 3 were still queued behind the stalled write)")
	}
}

// TestStoreCloseEndsOpenSubscriptions is item 4's Store-level acceptance:
// a GET /stream subscription must not outlive Store.Close (PR 620 review,
// MEDIUM: Close left subscribers connected, so nothing but the reader's
// own disconnect ever ended a stream).
//
// Mutant (store.go, Close): removing the closeAllSubscribers() call makes
// this RED: ch never closes, so the receive below blocks until the test's
// own 2s timeout instead of observing open == false.
func TestStoreCloseEndsOpenSubscriptions(t *testing.T) {
	st := newTestStore(t)
	ch := st.Subscribe(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("Subscribe channel: got a value, want it closed by Store.Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe channel: still open 2s after Store.Close")
	}
}

// TestHTTPServerShutdownReturnsPromptlyOnceStoreCloseEndsTheStream is item
// 4's HTTP-level acceptance: a real http.Server serving Store.Handler()
// with one open GET /stream must not hold Shutdown past its budget once
// Store.Close ends the stream (PR 620 review, MEDIUM: without this,
// Shutdown would wait out its whole context budget with the handler still
// blocked in its select loop).
func TestHTTPServerShutdownReturnsPromptlyOnceStoreCloseEndsTheStream(t *testing.T) {
	st := newTestStore(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := &http.Server{Handler: st.Handler()}
	go func() { _ = srv.Serve(ln) }()

	u, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /stream HTTP/1.1\r\nHost: " + u.Host + "\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	// Give Shutdown a moment to start waiting on the still-open handler
	// before Store.Close ends it, so this exercises "Shutdown is already
	// blocked" rather than a shutdown that never had anything to wait for.
	time.Sleep(50 * time.Millisecond)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := st.Close(closeCtx); err != nil {
		t.Fatalf("Store.Close: %v", err)
	}

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("srv.Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("srv.Shutdown: still blocked 2s after Store.Close ended the open stream")
	}
}
