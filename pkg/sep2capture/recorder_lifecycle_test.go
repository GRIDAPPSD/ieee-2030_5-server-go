package sep2capture

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestShutdownRecordsInFlightExchange is Q5 item 1: the exchange open when
// srv.Shutdown is called must still reach the sink, because Recorder.Close
// runs after Shutdown and waits for it (stage 1), rather than the listener
// stopping dispatch the instant Shutdown closes it.
func TestShutdownRecordsInFlightExchange(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)

	handlerStarted := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: handler}
	sink := NewMemorySink()
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()

	conn := rawDial(t, m, tcpLn.Addr().String())
	if _, err := conn.Write([]byte("GET /slow HTTP/1.1\r\nHost: t\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-handlerStarted

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := rec.Close(closeCtx); err != nil {
		t.Fatalf("Recorder.Close: %v", err)
	}

	exchanges := sink.All()
	if len(exchanges) != 1 {
		t.Fatalf("recorded exchanges: got %d, want 1", len(exchanges))
	}
	if got := rec.Dropped(); got != 0 {
		t.Errorf("Dropped: got %d, want 0", got)
	}
	if !bytes.HasPrefix(exchanges[0].Request.Bytes, []byte("GET ")) {
		t.Errorf("request bytes: got %q, want a %q prefix", exchanges[0].Request.Bytes, "GET ")
	}
}

// TestHungSinkDoesNotBlockServerShutdownOrClose is Q5 item 2: neither
// srv.Shutdown nor Recorder.Close may wait on a Sink that never returns.
// One exchange is stuck inside Sink.Record when both are called; three more
// are handed directly to the shared queue (bypassing a real connection) so
// the scenario does not depend on timing four TLS dials against the gate.
func TestHungSinkDoesNotBlockServerShutdownOrClose(t *testing.T) {
	m := newMaterial(t)
	sink := &gatedSink{gate: make(chan struct{})}
	t.Cleanup(func() { close(sink.gate) })

	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{Handler: okHandler("ok")}
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()

	conn := rawDial(t, m, tcpLn.Addr().String())
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.callsSoFar() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sink.callsSoFar() == 0 {
		t.Fatal("sink never entered Record; test proves nothing")
	}

	// Three more finished exchanges, queued behind the one Record is
	// stuck on, without needing three more real dials.
	att := newAttachment(rec, srv)
	filler := newConnRecorder(att, 999, "test-conn:filler")
	for i := 0; i < 3; i++ {
		b := &building{id: uint64(i), handlerRuns: 1}
		b.req.append([]byte("x"))
		filler.finish(b)
	}

	shutdownStart := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(shutdownStart); elapsed >= 1*time.Second {
		t.Errorf("Shutdown took %s, want under 1s", elapsed)
	}

	if _, err := net.DialTimeout("tcp", tcpLn.Addr().String(), 500*time.Millisecond); err == nil {
		t.Error("dial after Shutdown succeeded, want the listener refused")
	}

	closeStart := time.Now()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer closeCancel()
	closeErr := rec.Close(closeCtx)
	if elapsed := time.Since(closeStart); elapsed >= 1*time.Second {
		t.Errorf("Close took %s, want under 1s", elapsed)
	}
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close error: got %v, want context.DeadlineExceeded", closeErr)
	}
	if got := rec.Dropped(); got != 4 {
		t.Errorf("Dropped after Close: got %d, want 4 (3 queued + 1 abandoned)", got)
	}

	late := &building{id: 100, handlerRuns: 1}
	late.req.append([]byte("late"))
	filler.finish(late)
	if got := rec.Dropped(); got != 5 {
		t.Errorf("Dropped after a finish following Close: got %d, want 5", got)
	}
}

// TestDroppedCountsQueueFullAndPanic is Q5 item 3's first proof, isolated
// from any real connection or listener: the drop counter itself, both for a
// queue at capacity and for a Sink that panics.
func TestDroppedCountsQueueFullAndPanic(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	r := NewRecorder(sink, nil)
	// Registered so cleanup's LIFO order releases the gate before Close
	// runs: dispatch is left stuck inside Record for the whole test body,
	// and Close should not have to wait out its own ctx to prove that.
	t.Cleanup(func() { closeRecorder(t, r) })
	t.Cleanup(func() { close(sink.gate) })
	rec := newTestConnRecorder(r)

	// Prime dispatch into Record, and confirm it, before racing the loop
	// below against the channel: without this, dispatch's own first
	// receive can free a slot mid-loop and let more than sinkQueueCapacity
	// exchanges through, undercounting Dropped (caught by -race,
	// count=3: 2 dropped instead of the wanted 1).
	primer := &building{id: 0, handlerRuns: 1}
	primer.req.append([]byte("x"))
	rec.finish(primer)
	primed := time.Now().Add(2 * time.Second)
	for sink.callsSoFar() == 0 && time.Now().Before(primed) {
		time.Sleep(time.Millisecond)
	}
	if sink.callsSoFar() == 0 {
		t.Fatal("sink never entered Record; test proves nothing")
	}

	for i := 0; i < sinkQueueCapacity; i++ {
		b := &building{id: uint64(i + 1), handlerRuns: 1}
		b.req.append([]byte("x"))
		rec.finish(b)
	}
	const overflow = 1
	for i := 0; i < overflow; i++ {
		b := &building{id: uint64(1000 + i), handlerRuns: 1}
		b.req.append([]byte("x"))
		rec.finish(b)
	}
	if got := r.Dropped(); got != overflow {
		t.Fatalf("Dropped after filling the queue: got %d, want %d", got, overflow)
	}

	panicSink := &panicOnceSink{}
	r2 := NewRecorder(panicSink, log.New(io.Discard, "", 0))
	t.Cleanup(func() { closeRecorder(t, r2) })
	rec2 := newTestConnRecorder(r2)
	b := &building{id: 1, handlerRuns: 1}
	b.req.append([]byte("x"))
	rec2.finish(b)

	deadline := time.Now().Add(2 * time.Second)
	for r2.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r2.Dropped(); got != 1 {
		t.Errorf("Dropped after a panicking Record: got %d, want 1", got)
	}
}

// TestRecorderCloseIsIdempotentAndStopsDispatch is Q5 item 3's second
// proof: Close is safe to call more than once, and it actually stops the
// dispatch goroutine rather than merely returning. The baseline is taken
// before NewRecorder, not before Attach, since NewRecorder is what starts
// the one goroutine Close must stop.
func TestRecorderCloseIsIdempotentAndStopsDispatch(t *testing.T) {
	baseline := runtime.NumGoroutine()

	sink := NewMemorySink()
	r := NewRecorder(sink, nil)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	if err := r.Close(ctx1); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := r.Close(ctx2); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	assertGoroutinesSettle(t, baseline)
}

// TestAttachHandshakeBoundFollowsServerTimeouts is Q5 item 4 (Q3): the
// handshake bound this package drives must follow srv's own timeouts, the
// same net/http applies to a bare *tls.Conn, not the fixed
// defaultHandshakeTimeout PR 1's own Listener uses.
func TestAttachHandshakeBoundFollowsServerTimeouts(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	bare := tls.NewListener(tcpLn, gcmServerConfig(t, m)) // not pre-handshaken
	srv := &http.Server{
		Handler:           okHandler("ok"),
		ReadHeaderTimeout: 300 * time.Millisecond,
	}
	sink := NewMemorySink()
	var logBuf syncBuffer
	rec := NewRecorder(sink, log.New(&logBuf, "", 0))
	wrapped := rec.Attach(srv, bare)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.DialTimeout("tcp", tcpLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// A silent peer: no TLS bytes sent at all.

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	if readErr == nil {
		t.Fatal("read returned data, want the peer closed at the handshake bound")
	}
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatal("read timed out instead of erroring: srv.ReadHeaderTimeout (300ms) was not applied as the handshake bound")
	}

	deadline := time.Now().Add(1 * time.Second)
	for !strings.Contains(logBuf.String(), "TLS handshake error") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "TLS handshake error") {
		t.Errorf("want a logged handshake-error refusal, got: %s", logBuf.String())
	}
}

// TestHandshakeBoundForFollowsSmallestPositiveServerTimeout is item 4's
// table test for the bound function itself: all-zero falls back to
// defaultHandshakeTimeout, and otherwise the smallest positive of the three
// server timeouts wins.
func TestHandshakeBoundForFollowsSmallestPositiveServerTimeout(t *testing.T) {
	cases := []struct {
		name                    string
		readHeader, read, write time.Duration
		want                    time.Duration
	}{
		{"all zero falls back to the default", 0, 0, 0, defaultHandshakeTimeout},
		{"only WriteTimeout set", 0, 0, 5 * time.Second, 5 * time.Second},
		{"smallest of three wins", 10 * time.Second, 30 * time.Second, 30 * time.Second, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &http.Server{ReadHeaderTimeout: tc.readHeader, ReadTimeout: tc.read, WriteTimeout: tc.write}
			if got := handshakeBoundFor(srv); got != tc.want {
				t.Errorf("handshakeBoundFor: got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestPlaintextListenerLeavesRequestTLSNil is Q5 item 5 (Q4): a handler
// behind Attach must see the same r.TLS a bare net/http server would for a
// plaintext listener, nil, not the non-nil zero value net/http would
// populate if this package's own ConnectionState method existed
// unconditionally on every wrapped connection. TestIdentityRecordedThroughAttach
// is this test's control: its GCM and CCM subtests still see a non-nil
// r.TLS through Attach, over a TLS-capable listener.
func TestPlaintextListenerLeavesRequestTLSNil(t *testing.T) {
	tcpLn := listenTCP(t)
	var sawNil bool
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawNil = r.TLS == nil
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: handler}
	sink := NewMemorySink()
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, tcpLn)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", tcpLn.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	req := "GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	mu.Lock()
	got := sawNil
	mu.Unlock()
	if !got {
		t.Error("handler saw a non-nil r.TLS behind a plaintext listener")
	}

	exchanges := waitForExchanges(t, sink, 1)
	if !bytes.Equal(exchanges[0].Request.Bytes, []byte(req)) {
		t.Errorf("request bytes: got %q, want %q", exchanges[0].Request.Bytes, req)
	}
	if !bytes.Equal(exchanges[0].Response.Bytes, raw) {
		t.Errorf("response bytes: got %q, want %q", exchanges[0].Response.Bytes, raw)
	}
}
