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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// startCaptureServer serves handler over a fresh TLS listener with a
// Recorder attached, and returns the address to dial and the sink it was
// given. The listener handshakes before Accept returns (this package's own
// Listener over a bare tls.Listener); Attach does not require that, but
// most tests here still use the pre-handshaken shape since it is what most
// of this file predates.
func startCaptureServer(t testing.TB, m material, handler http.Handler) (addr string, sink *MemorySink) {
	t.Helper()
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{Handler: handler}
	sink = NewMemorySink()
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })
	return tcpLn.Addr().String(), sink
}

// closeRecorder is the shared test cleanup for Recorder.Close: cleanup
// itself is not the property under test, so a bounded context stands in for
// the deadline a real caller would supply, and a failure is logged rather
// than failing the test outright, so a slow-draining cleanup on an
// unrelated test does not mask the assertion that actually mattered.
func closeRecorder(t testing.TB, r *Recorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Logf("Recorder.Close in cleanup: %v", err)
	}
}

func rawDial(t testing.TB, m material, addr string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, gcmClientConfig(t, m))
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// waitForExchangesSettle is how long waitForExchanges keeps watching after
// it first sees n exchanges, to catch a further one landing late.
const waitForExchangesSettle = 150 * time.Millisecond

// waitForExchanges polls sink until it holds at least n exchanges, then
// holds waitForExchangesSettle to check that exactly n eventually landed:
// returning the instant n was reached let a further, unexpected exchange
// arrive unseen (coverage review at 65d5d5c, recording_test.go:182, :219).
// Attach hands each exchange to the sink off the connection's own goroutine,
// so a test cannot read sink.All() the instant its client read returns; it
// has to wait for that handoff to land.
func waitForExchanges(t testing.TB, sink *MemorySink, n int) []Exchange {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ex := sink.All()
		if len(ex) >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForExchanges: got %d exchanges, want %d, after 5s", len(ex), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	settle := time.Now().Add(waitForExchangesSettle)
	for time.Now().Before(settle) {
		time.Sleep(5 * time.Millisecond)
	}
	ex := sink.All()
	if len(ex) != n {
		t.Fatalf("waitForExchanges: got %d exchanges after settling, want exactly %d", len(ex), n)
	}
	return ex
}

func readByte(t testing.TB, conn net.Conn, buf *bytes.Buffer) byte {
	t.Helper()
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		t.Fatalf("read: %v", err)
	}
	buf.WriteByte(b[0])
	return b[0]
}

func readLine(t testing.TB, conn net.Conn, buf *bytes.Buffer) string {
	t.Helper()
	var line bytes.Buffer
	for {
		line.WriteByte(readByte(t, conn, buf))
		if bytes.HasSuffix(line.Bytes(), []byte("\r\n")) {
			return line.String()
		}
	}
}

// readRawHTTPMessage reads one HTTP message directly off conn, one byte at
// a time for the status line and headers so nothing is buffered ahead of
// what framing requires, then exactly its body per Content-Length or
// chunked framing. The returned bytes are exactly what this one message
// carried on the wire: this is the client-side ground truth every
// assertion in this file compares a recorded Exchange against.
func readRawHTTPMessage(t testing.TB, conn net.Conn) []byte {
	t.Helper()
	var buf bytes.Buffer
	readLine(t, conn, &buf) // status line
	contentLength := -1
	chunked := false
	for {
		line := readLine(t, conn, &buf)
		if line == "\r\n" {
			break
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "content-length:"):
			v := strings.TrimSpace(line[len("content-length:"):])
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatalf("bad content-length %q: %v", v, err)
			}
			contentLength = n
		case strings.HasPrefix(lower, "transfer-encoding:") && strings.Contains(lower, "chunked"):
			chunked = true
		}
	}
	switch {
	case chunked:
		for {
			sizeLine := readLine(t, conn, &buf)
			sizeHex := strings.TrimSpace(strings.SplitN(sizeLine, ";", 2)[0])
			size, err := strconv.ParseInt(sizeHex, 16, 64)
			if err != nil {
				t.Fatalf("bad chunk size %q: %v", sizeLine, err)
			}
			if size == 0 {
				for {
					if readLine(t, conn, &buf) == "\r\n" {
						return buf.Bytes()
					}
				}
			}
			chunk := make([]byte, size)
			if _, err := io.ReadFull(conn, chunk); err != nil {
				t.Fatalf("read chunk: %v", err)
			}
			buf.Write(chunk)
			var crlf [2]byte
			if _, err := io.ReadFull(conn, crlf[:]); err != nil {
				t.Fatalf("read chunk trailer: %v", err)
			}
			buf.Write(crlf[:])
		}
	case contentLength > 0:
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(conn, body); err != nil {
			t.Fatalf("read body: %v", err)
		}
		buf.Write(body)
		return buf.Bytes()
	default:
		return buf.Bytes()
	}
}

func okHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// TestThreeKeepAliveRequestsProduceThreeExchanges is acceptance 1: the
// realistic keep-alive pattern (write a request, read its full response,
// then write the next). TestCarryOverMovesAnEarlyByteToTheNextExchange
// below is the "break it and watch it go RED" proof for the carry-over
// mechanism this depends on; sequential keep-alive on loopback usually
// resolves ConnState's StateIdle before the client's next request arrives,
// so this test alone would rarely exercise that race.
func TestThreeKeepAliveRequestsProduceThreeExchanges(t *testing.T) {
	m := newMaterial(t)
	addr, sink := startCaptureServer(t, m, okHandler("ok"))
	conn := rawDial(t, m, addr)

	reqs := []string{
		"GET /a HTTP/1.1\r\nHost: t\r\n\r\n",
		"GET /b HTTP/1.1\r\nHost: t\r\n\r\n",
		"GET /c HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n",
	}
	rawResps := make([][]byte, 3)
	for i, req := range reqs {
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write request %d: %v", i, err)
		}
		rawResps[i] = readRawHTTPMessage(t, conn)
	}

	exchanges := waitForExchanges(t, sink, 3)
	if len(exchanges) != 3 {
		t.Fatalf("got %d exchanges, want exactly 3", len(exchanges))
	}
	for i, ex := range exchanges {
		if !bytes.Equal(ex.Request.Bytes, []byte(reqs[i])) {
			t.Errorf("exchange %d request: got %q, want %q", i, ex.Request.Bytes, reqs[i])
		}
		if !bytes.Equal(ex.Response.Bytes, rawResps[i]) {
			t.Errorf("exchange %d response: got %q, want %q", i, ex.Response.Bytes, rawResps[i])
		}
		if ex.Mark != MarkHandled {
			t.Errorf("exchange %d mark: got %v, want handled", i, ex.Mark)
		}
		if ex.HandlerRuns != 1 {
			t.Errorf("exchange %d handler runs: got %d, want 1", i, ex.HandlerRuns)
		}
	}
}

// TestMalformedRequestLineMarkedRejectedBeforeHandler is acceptance 2.
func TestMalformedRequestLineMarkedRejectedBeforeHandler(t *testing.T) {
	m := newMaterial(t)
	addr, sink := startCaptureServer(t, m, okHandler("ok"))
	conn := rawDial(t, m, addr)

	garbage := "this is not a request line\r\n\r\n"
	if _, err := conn.Write([]byte(garbage)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// net/http's own 400 reply carries Connection: close and no
	// Content-Length; it ends only when the server closes the connection.
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	exchanges := waitForExchanges(t, sink, 1)
	ex := exchanges[0]
	if ex.Mark != MarkRejectedBeforeHandler {
		t.Errorf("mark: got %v, want rejected before handler", ex.Mark)
	}
	if !bytes.Equal(ex.Request.Bytes, []byte(garbage)) {
		t.Errorf("request: got %q, want %q", ex.Request.Bytes, garbage)
	}
	if !bytes.Equal(ex.Response.Bytes, raw) {
		t.Errorf("response: got %q, want %q", ex.Response.Bytes, raw)
	}
	if ex.HandlerRuns != 0 {
		t.Errorf("handler runs: got %d, want 0", ex.HandlerRuns)
	}
}

// TestChunkedResponseByteExact is acceptance 3.
func TestChunkedResponseByteExact(t *testing.T) {
	m := newMaterial(t)
	chunks := []string{"first chunk ", "second chunk ", "third"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
			flusher.Flush()
		}
	})
	addr, sink := startCaptureServer(t, m, handler)
	conn := rawDial(t, m, addr)

	req := "GET /stream HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := readRawHTTPMessage(t, conn)
	if !bytes.Contains(raw, []byte("Transfer-Encoding: chunked")) {
		t.Fatalf("response not chunked: %q", raw)
	}

	exchanges := waitForExchanges(t, sink, 1)
	ex := exchanges[0]
	if ex.Mark != MarkHandled {
		t.Errorf("mark: got %v (error=%q), want handled", ex.Mark, ex.Error)
	}
	if !bytes.Equal(ex.Response.Bytes, raw) {
		t.Errorf("response bytes (chunk framing must match exactly): got %q, want %q", ex.Response.Bytes, raw)
	}
}

// TestExpectContinueStaysOneExchange is acceptance 4.
func TestExpectContinueStaysOneExchange(t *testing.T) {
	m := newMaterial(t)
	var gotBody []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler: read body: %v", err)
		}
		gotBody = b
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	addr, sink := startCaptureServer(t, m, handler)
	conn := rawDial(t, m, addr)

	head := "POST /echo HTTP/1.1\r\nHost: t\r\nContent-Length: 5\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(head)); err != nil {
		t.Fatalf("write head: %v", err)
	}
	interim := readRawHTTPMessage(t, conn)
	if !bytes.Contains(interim, []byte("100 Continue")) {
		t.Fatalf("expected 100 Continue interim, got %q", interim)
	}
	body := "hello"
	if _, err := conn.Write([]byte(body)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	final := readRawHTTPMessage(t, conn)

	exchanges := waitForExchanges(t, sink, 1)
	if len(exchanges) != 1 {
		t.Fatalf("got %d exchanges, want exactly 1 (100-continue must not open a second one)", len(exchanges))
	}
	ex := exchanges[0]
	if string(gotBody) != body {
		t.Fatalf("handler body: got %q, want %q", gotBody, body)
	}
	if !bytes.Equal(ex.Request.Bytes, []byte(head+body)) {
		t.Errorf("request bytes: got %q, want %q", ex.Request.Bytes, head+body)
	}
	if !bytes.Equal(ex.Response.Bytes, append(append([]byte{}, interim...), final...)) {
		t.Errorf("response bytes: got %q, want interim+final", ex.Response.Bytes)
	}
}

// TestConnStateHookSetBeforeAttachStillFires is acceptance 5.
func TestConnStateHookSetBeforeAttachStillFires(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{Handler: okHandler("ok")}

	var previousCalls atomic.Int64
	srv.ConnState = func(net.Conn, http.ConnState) { previousCalls.Add(1) }

	sink := NewMemorySink()
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })

	conn := rawDial(t, m, tcpLn.Addr().String())
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = readRawHTTPMessage(t, conn)
	waitForExchanges(t, sink, 1)

	if previousCalls.Load() == 0 {
		t.Error("ConnState hook set before Attach never fired")
	}
}

// TestMessageOverCapIsTruncated is acceptance 6.
func TestMessageOverCapIsTruncated(t *testing.T) {
	m := newMaterial(t)
	body := bytes.Repeat([]byte("A"), perDirectionCap+1024)
	addr, sink := startCaptureServer(t, m, okHandler(string(body)))
	conn := rawDial(t, m, addr)

	if _, err := conn.Write([]byte("GET /big HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := readRawHTTPMessage(t, conn)
	if len(raw) <= perDirectionCap {
		t.Fatalf("test setup: raw response (%d bytes) does not exceed the cap (%d)", len(raw), perDirectionCap)
	}

	exchanges := waitForExchanges(t, sink, 1)
	ex := exchanges[0]
	if !ex.Response.Truncated {
		t.Error("response not marked truncated")
	}
	if ex.Response.TrueLen != int64(len(raw)) {
		t.Errorf("true length: got %d, want %d", ex.Response.TrueLen, len(raw))
	}
	if len(ex.Response.Bytes) != perDirectionCap {
		t.Errorf("stored length: got %d, want the cap (%d)", len(ex.Response.Bytes), perDirectionCap)
	}
	if !bytes.Equal(ex.Response.Bytes, raw[:perDirectionCap]) {
		t.Error("stored bytes are not the wire's own first perDirectionCap bytes")
	}
}

// TestGrowBufferAppendExactlyAtCapIsNotTruncated and
// TestGrowBufferAppendOneByteOverCapIsTruncated pin the cap boundary
// itself: growBuffer.append compares len(b) against room with `>`, so a
// message that exactly fills the remaining room must not be flagged
// truncated. A `>=` swap would truncate a message that fit exactly.
func TestGrowBufferAppendExactlyAtCapIsNotTruncated(t *testing.T) {
	var g growBuffer
	g.append(bytes.Repeat([]byte("A"), perDirectionCap))

	if g.truncated {
		t.Error("truncated: got true, want false (the message exactly filled the cap)")
	}
	if g.trueLen != perDirectionCap {
		t.Errorf("trueLen: got %d, want %d", g.trueLen, perDirectionCap)
	}
	if len(g.bytes) != perDirectionCap {
		t.Errorf("stored length: got %d, want %d", len(g.bytes), perDirectionCap)
	}
}

func TestGrowBufferAppendOneByteOverCapIsTruncated(t *testing.T) {
	var g growBuffer
	g.append(bytes.Repeat([]byte("A"), perDirectionCap+1))

	if !g.truncated {
		t.Error("truncated: got false, want true (the message was one byte over the cap)")
	}
	if g.trueLen != perDirectionCap+1 {
		t.Errorf("trueLen: got %d, want %d", g.trueLen, perDirectionCap+1)
	}
	if len(g.bytes) != perDirectionCap {
		t.Errorf("stored length: got %d, want the cap (%d)", len(g.bytes), perDirectionCap)
	}
}

func newTestConnRecorder(r *Recorder) *connRecorder {
	return newConnRecorder(newAttachment(r, nil), 1, "test-conn:1")
}

// TestWaitForExchangesCatchesALateExtraExchange proves the settle window
// added to waitForExchanges actually does something: it runs the helper
// against a fresh *testing.T (never told to t.Run, so its own Fatalf cannot
// abort or fail this test) on a sink that gets its (n+1)th exchange shortly
// after the nth. A waitForExchanges that only checked "at least n" would
// leave that fake T passing; this one must fail it, because len(sink.All())
// is n+1 once the settle window closes. waitForExchanges calls t.FailNow
// internally, which needs its own goroutine to unwind without killing this
// test's.
func TestWaitForExchangesCatchesALateExtraExchange(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	b1 := &building{id: 1, handlerRuns: 1}
	b1.req.append([]byte("x"))
	rec.finish(b1)

	go func() {
		time.Sleep(waitForExchangesSettle / 2)
		b2 := &building{id: 2, handlerRuns: 1}
		b2.req.append([]byte("y"))
		rec.finish(b2)
		rec.closeFinal()
	}()

	fakeT := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForExchanges(fakeT, sink, 1)
	}()
	<-done

	if !fakeT.Failed() {
		t.Error("waitForExchanges(sink, 1) did not catch the second exchange arriving during its settle window")
	}
}

// TestCarryOverMovesAnEarlyByteToTheNextExchange is the deterministic proof
// the carry-over rule in recordInbound and rollover is load-bearing.
// TestThreeKeepAliveRequestsProduceThreeExchanges exercises the same code
// in a real server, but on loopback the background peek usually does not
// return before StateIdle already advanced the pointer, so that test alone
// would rarely go red if carry-over were removed. This one drives the
// exact sequence net/http produces (peek, then rollover) directly: change
// recordInbound's isPeek branch to append straight into rec.current.req
// instead of pendingCarry, and this test fails, because "G" would stay on
// exchange 1's request instead of moving to exchange 2's.
func TestCarryOverMovesAnEarlyByteToTheNextExchange(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	rec.open(1)
	rec.recordInbound([]byte("GET /a HTTP/1.1\r\n\r\n"), false)
	rec.markHandlerRan()
	rec.recordOutbound([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	rec.recordInbound([]byte("G"), true) // the background peek, before StateIdle
	rec.rollover(2)
	rec.recordInbound([]byte("ET /b HTTP/1.1\r\n\r\n"), false)
	rec.markHandlerRan()
	rec.recordOutbound([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 2)
	if got, want := string(exchanges[0].Request.Bytes), "GET /a HTTP/1.1\r\n\r\n"; got != want {
		t.Errorf("exchange 1 request: got %q, want %q (the peeked byte must not stay here)", got, want)
	}
	if got, want := string(exchanges[1].Request.Bytes), "GET /b HTTP/1.1\r\n\r\n"; got != want {
		t.Errorf("exchange 2 request: got %q, want %q (the peeked byte must be prepended here)", got, want)
	}
}

// TestCloseFinalKeepsAPendingCarryByteOnTheClosingExchange: when the
// connection closes right after a background peek captured a byte, there is
// no next exchange for rollover to move it to. closeFinal must fold it into
// the exchange that is actually closing instead of dropping it.
func TestCloseFinalKeepsAPendingCarryByteOnTheClosingExchange(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	rec.open(1)
	rec.recordInbound([]byte("GET /a HTTP/1.1\r\n\r\n"), false)
	rec.markHandlerRan()
	rec.recordOutbound([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	rec.recordInbound([]byte("G"), true) // the background peek, right before the client closes for good
	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 1)
	want := "GET /a HTTP/1.1\r\n\r\nG"
	if got := string(exchanges[0].Request.Bytes); got != want {
		t.Errorf("request: got %q, want %q (the pending carry byte must not be dropped at final close)", got, want)
	}
}

// forget must drop the closing connection's own entry, but never a
// different, newer connRecorder that has since taken its remote address (a
// closing goroutine racing behind a port-reuse wrap).

func TestForgetRemovesTheClosedConnectionsEntry(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	att := newAttachment(r, nil)
	rec := newConnRecorder(att, 1, "test-conn:reuse")
	att.mu.Lock()
	att.byAddr[rec.remoteAddr] = rec
	att.mu.Unlock()

	att.forget(rec)

	if got := att.byRemoteAddr(rec.remoteAddr); got != nil {
		t.Error("forget did not remove the closed connection's own entry")
	}
	rec.closeFinal()
}

func TestForgetLeavesANewerConnectionAtTheSameAddressAlone(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	att := newAttachment(r, nil)
	addr := "test-conn:reuse"
	oldRec := newConnRecorder(att, 1, addr)
	newRec := newConnRecorder(att, 2, addr)
	att.mu.Lock()
	att.byAddr[addr] = newRec // simulates port reuse: a new connection already claimed this address
	att.mu.Unlock()

	att.forget(oldRec) // the old connection's own close path, racing behind the new one's wrap

	if got := att.byRemoteAddr(addr); got != newRec {
		t.Errorf("forget removed the newer connection's entry: got %v, want the newer recorder", got)
	}
	oldRec.closeFinal()
	newRec.closeFinal()
}

// classify unit tests: direct, deterministic proof that a non-timeout
// error is never discarded just because a handler already ran or a
// response was already written, whatever the error's own text looks like.

func TestClassifyRecordsAPlainResetAfterHandlerRan(t *testing.T) {
	resetErr := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
	mark, errText := classify(1, true, resetErr)
	if mark != MarkConnectionError {
		t.Errorf("mark: got %v, want connection error", mark)
	}
	if errText == "" {
		t.Error("error text discarded")
	}
}

// The exact shape crypto/tls and core's gotls fork both produce for a
// mid-stream decrypt failure (conn.go's sendAlertLocked): a net.OpError
// whose Op is "local error" or "remote error" and whose Err is a TLS alert,
// wrapped so Error() reads "local error: tls: bad record MAC". The old
// isTLSError checked only for a literal "tls: " prefix on the whole string,
// which this shape never has.
func TestClassifyRecordsATLSAlertShapedErrorAfterHandlerRan(t *testing.T) {
	tlsErr := &net.OpError{Op: "local error", Err: errors.New("tls: bad record MAC")}
	mark, errText := classify(1, true, tlsErr)
	if mark != MarkConnectionError {
		t.Errorf("mark: got %v (error=%q), want connection error", mark, errText)
	}
	if errText == "" {
		t.Error("error text discarded")
	}
}

func TestClassifyStillPrefersIncompleteForATimeout(t *testing.T) {
	mark, errText := classify(1, true, timeoutErr{})
	if mark != MarkIncomplete {
		t.Errorf("mark: got %v, want incomplete", mark)
	}
	if errText == "" {
		t.Error("error text discarded")
	}
}

// classify's connection-error case fires on handlerRuns>0 OR wroteAny, not
// both at once: the two tests above always pass both flags true, which
// would not catch an `||` to `&&` swap. These isolate each flag.

func TestClassifyRecordsConnectionErrorFromHandlerRunAloneWithNoWrite(t *testing.T) {
	err := errors.New("connection reset by peer")
	mark, errText := classify(1, false, err)
	if mark != MarkConnectionError {
		t.Errorf("mark: got %v, want connection error", mark)
	}
	if errText != err.Error() {
		t.Errorf("error text: got %q, want %q", errText, err.Error())
	}
}

// TestClassifyRecordsConnectionErrorFromAWriteAloneWithNoHandlerRun is the
// "write error" case: bytes were written to the client (net/http's own
// automatic error reply, say) before any handler ran, and the write itself
// then failed.
func TestClassifyRecordsConnectionErrorFromAWriteAloneWithNoHandlerRun(t *testing.T) {
	err := errors.New("write: broken pipe")
	mark, errText := classify(0, true, err)
	if mark != MarkConnectionError {
		t.Errorf("mark: got %v, want connection error", mark)
	}
	if errText != err.Error() {
		t.Errorf("error text: got %q, want %q", errText, err.Error())
	}
}

// TestClassifyRecordsErrorTextOnNoResponse covers the remaining classify
// branch: no handler ran, nothing was written, and the client is simply
// gone. Mark is MarkNoResponse, and the error text must still be kept, not
// swallowed the way MarkHandled's "" is for the no-error case.
func TestClassifyRecordsErrorTextOnNoResponse(t *testing.T) {
	err := errors.New("EOF")
	mark, errText := classify(0, false, err)
	if mark != MarkNoResponse {
		t.Errorf("mark: got %v, want no response", mark)
	}
	if errText != err.Error() {
		t.Errorf("error text: got %q, want %q", errText, err.Error())
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "test: i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestCorruptTLSRecordDuringHandlerIsRecordedAsConnectionError is an
// end-to-end reproduction: a real corrupt TLS record, arriving on the
// background one-byte peek while the handler is still running, must be
// recorded on the still-open exchange rather than discarded. Before the
// fix, classify silently turned this into MarkHandled with no error, and
// the isPeek branch in recordingConn.Read discarded the error before it
// ever reached classify at all.
func TestCorruptTLSRecordDuringHandlerIsRecordedAsConnectionError(t *testing.T) {
	m := newMaterial(t)
	proceed := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-proceed
		w.WriteHeader(http.StatusOK)
	})
	addr, sink := startCaptureServer(t, m, handler)
	conn := rawDial(t, m, addr)

	if _, err := conn.Write([]byte("GET /a HTTP/1.1\r\nHost: t\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give the server time to parse the request and arm its background
	// peek (startBackgroundRead, called before the handler runs for a
	// bodyless request) before the corrupt record below arrives.
	time.Sleep(50 * time.Millisecond)

	// A TLS record header (type 23 application_data, version 0x0303, the
	// compat value TLS 1.3 still uses) followed by ciphertext that will
	// never authenticate under any key. Written on the raw connection,
	// bypassing the client's own TLS framing, so the server's decrypt
	// genuinely fails rather than the client refusing to send it.
	garbage := []byte{0x17, 0x03, 0x03, 0x00, 0x10}
	garbage = append(garbage, bytes.Repeat([]byte{0xAA}, 16)...)
	if _, err := conn.NetConn().Write(garbage); err != nil {
		t.Fatalf("write garbage record: %v", err)
	}
	// The background peek's decrypt failure and this goroutine unblocking
	// the handler are otherwise unsynchronized: give the peek goroutine
	// time to actually fail and call noteError before the handler
	// finishes and the exchange rolls over.
	time.Sleep(100 * time.Millisecond)
	close(proceed)

	exchanges := waitForExchanges(t, sink, 1)
	ex := exchanges[0]
	if ex.Mark != MarkConnectionError {
		t.Errorf("mark: got %v (error=%q), want connection error", ex.Mark, ex.Error)
	}
	if ex.Error == "" {
		t.Error("error text discarded")
	}
}

// TestClientResetDuringPOSTBodyIsRecordedAsConnectionError is an
// end-to-end reproduction for a reset arriving mid-request-body. handlerRuns
// is already 1 by the time the handler starts reading the body (annotate
// runs before the handler), so this also proves the fix does not depend on
// the response having been written yet.
func TestClientResetDuringPOSTBodyIsRecordedAsConnectionError(t *testing.T) {
	m := newMaterial(t)
	bodyErr := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		bodyErr <- err
		w.WriteHeader(http.StatusOK)
	})
	addr, sink := startCaptureServer(t, m, handler)
	conn := rawDial(t, m, addr)

	req := "POST /echo HTTP/1.1\r\nHost: t\r\nContent-Length: 1000000\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if _, err := conn.Write([]byte("partial body")); err != nil {
		t.Fatalf("write partial body: %v", err)
	}

	tcp, ok := conn.NetConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("underlying conn is %T, not *net.TCPConn", conn.NetConn())
	}
	if err := tcp.SetLinger(0); err != nil {
		t.Fatalf("SetLinger: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case err := <-bodyErr:
		if err == nil {
			t.Fatal("test setup: handler's body read did not fail after the client reset")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never observed the reset")
	}

	exchanges := waitForExchanges(t, sink, 1)
	ex := exchanges[0]
	if ex.Mark != MarkConnectionError {
		t.Errorf("mark: got %v (error=%q), want connection error", ex.Mark, ex.Error)
	}
	if ex.Error == "" {
		t.Error("error text discarded")
	}
}

// stubConn is a net.Conn whose Read is entirely test-controlled, so
// recordingConn.Read's own filtering (isPeek and isTimeout) can be tested
// directly without needing a real timeout or a real reset to occur. Every
// other net.Conn method is unused by these tests and left as a nil
// embedded interface.
type stubConn struct {
	net.Conn
	read  func(p []byte) (int, error)
	write func(p []byte) (int, error)
}

func (s *stubConn) Read(p []byte) (int, error) { return s.read(p) }

func (s *stubConn) Write(p []byte) (int, error) { return s.write(p) }

// TestRecordingConnReadIgnoresOnlyTheAbortedPeekTimeout and
// TestRecordingConnReadRecordsANonTimeoutErrorOnAOneByteRead are the
// unit-level proof, exercising recordingConn.Read directly rather than
// through classify: the filtering this fixes lives entirely in Read, since
// noteError itself records whatever it is given unconditionally.
func TestRecordingConnReadIgnoresOnlyTheAbortedPeekTimeout(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)
	rec.open(1)

	stub := &stubConn{read: func(p []byte) (int, error) { return 0, timeoutErr{} }}
	rc := &recordingConn{recordingCore: recordingCore{Conn: stub, rec: rec}}
	if _, err := rc.Read(make([]byte, 1)); err == nil {
		t.Fatal("test setup: stub Read did not return an error")
	}

	rec.markHandlerRan()
	rec.recordOutbound([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 1)
	if exchanges[0].Mark != MarkHandled {
		t.Errorf("mark: got %v (error=%q), want handled (a peek timeout must stay silent)", exchanges[0].Mark, exchanges[0].Error)
	}
}

func TestRecordingConnReadRecordsANonTimeoutErrorOnAOneByteRead(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)
	rec.open(1)
	rec.recordInbound([]byte("x"), false) // real bytes, so finish does not skip this exchange as empty

	stub := &stubConn{read: func(p []byte) (int, error) { return 0, errors.New("read tcp: connection reset by peer") }}
	rc := &recordingConn{recordingCore: recordingCore{Conn: stub, rec: rec}}
	if _, err := rc.Read(make([]byte, 1)); err == nil {
		t.Fatal("test setup: stub Read did not return an error")
	}

	rec.markHandlerRan()
	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 1)
	if exchanges[0].Mark != MarkConnectionError {
		t.Errorf("mark: got %v (error=%q), want connection error (a reset on a 1-byte read must not be silently dropped)", exchanges[0].Mark, exchanges[0].Error)
	}
}

// TestRecordingConnWriteRecordsAWriteError is the write-side counterpart:
// recordingConn.Write must pass a failed Write's error to noteError
// unconditionally, the same as Read does for a real (non-peek) error.
func TestRecordingConnWriteRecordsAWriteError(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)
	rec.open(1)
	rec.recordInbound([]byte("GET / HTTP/1.1\r\n\r\n"), false)
	rec.markHandlerRan()

	stub := &stubConn{write: func(p []byte) (int, error) { return 0, errors.New("write: broken pipe") }}
	rc := &recordingConn{recordingCore: recordingCore{Conn: stub, rec: rec}}
	if _, err := rc.Write([]byte("HTTP/1.1 200 OK\r\n\r\n")); err == nil {
		t.Fatal("test setup: stub Write did not return an error")
	}

	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 1)
	if exchanges[0].Mark != MarkConnectionError {
		t.Errorf("mark: got %v (error=%q), want connection error (a write error must not be silently dropped)", exchanges[0].Mark, exchanges[0].Error)
	}
	if exchanges[0].Error == "" {
		t.Error("error text discarded")
	}
}

// handshakeFailingConn implements handshaker and fails its handshake
// unconditionally, tracking how many times Close was called, so
// completeHandshake's refusal path can be exercised directly without a
// real TLS peer.
type handshakeFailingConn struct {
	net.Conn
	closeCalls *int32
}

func (c *handshakeFailingConn) HandshakeContext(context.Context) error {
	return errors.New("test: handshake always fails")
}

func (c *handshakeFailingConn) Close() error {
	atomic.AddInt32(c.closeCalls, 1)
	return c.Conn.Close()
}

var _ handshaker = (*handshakeFailingConn)(nil)

// TestCompleteHandshakeClosesOnFailure is the unit-level proof that
// completeHandshake must close the underlying connection when its handshake
// fails. A black-box dial against a garbage TLS peer does not discriminate
// this in the current design: net/http's own serve loop makes its next read
// return the wrapped *tls.Conn's cached handshake error immediately and
// tears the connection down through its own c.close(), whether or not this
// method's own Close() call runs too (checked directly: the dial-based
// TestAttachRefusesAHandshakeFailure still passed with this Close() call
// removed). This test calls completeHandshake directly against a stub that
// only fails its handshake, so only this method's own Close() call can make
// it pass.
func TestCompleteHandshakeClosesOnFailure(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, log.New(io.Discard, "", 0))
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	var closeCalls int32
	fc := &handshakeFailingConn{Conn: server, closeCalls: &closeCalls}
	rc := &recordingConn{recordingCore: recordingCore{Conn: fc, rec: rec}}

	rc.completeHandshake()

	if got := atomic.LoadInt32(&closeCalls); got == 0 {
		t.Error("Close: want the connection closed after a failed handshake, got 0 calls")
	}
}

// A stalled or panicking Sink must never stall or crash the client.

// gatedSink blocks every Record call until gate is closed, so a test can
// prove the hand-off into finished is non-blocking regardless of how slow
// or stuck the sink itself is.
type gatedSink struct {
	gate chan struct{}
	mu   sync.Mutex
	got  []Exchange
	// inFlight counts Record calls that have started but not yet returned
	// (blocked on gate), so a test can prove the sink was actually entered
	// without waiting for a call it deliberately never unblocks.
	inFlight int
}

func (s *gatedSink) Record(ex Exchange) {
	s.mu.Lock()
	s.inFlight++
	s.mu.Unlock()
	<-s.gate
	s.mu.Lock()
	s.inFlight--
	s.got = append(s.got, ex)
	s.mu.Unlock()
}

func (s *gatedSink) all() []Exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Exchange, len(s.got))
	copy(out, s.got)
	return out
}

// callsSoFar reports how many times Record has been entered (including the
// one currently blocked on the gate), without waiting for any to return.
func (s *gatedSink) callsSoFar() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got) + s.inFlight
}

// TestSinkStallDoesNotBlockTheConnectionGoroutine proves finish's hand-off
// to the dispatch goroutine never blocks, even once a stalled Sink has let
// the shared queue fill: the exchanges past the queue's capacity are
// dropped and counted, not queued without bound and not left to stall the
// caller (recorder.go's finish is called from the same goroutine net/http
// uses to read from and write to the client).
func TestSinkStallDoesNotBlockTheConnectionGoroutine(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	const capacity = 256
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < capacity+2; i++ {
			b := &building{id: uint64(i), handlerRuns: 1}
			b.req.append([]byte("x"))
			rec.finish(b)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("finish blocked on a stalled sink; the hand-off must be non-blocking")
	}

	close(sink.gate)
	rec.closeFinal()
	// Let the dispatch goroutine drain whatever made it into the queue
	// before it exits (closeFinal closes finished once the last exchange
	// is handed off).
	deadline := time.Now().Add(2 * time.Second)
	for len(sink.all()) < capacity && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := r.Dropped(); got == 0 {
		t.Errorf("dropped: got 0, want > 0 after exceeding the %d-slot queue", capacity)
	}
}

// panicOnceSink panics on its first Record call and records normally after
// that, so a test can prove the dispatch goroutine survives a panic and
// keeps handing later exchanges to Record.
type panicOnceSink struct {
	mu       sync.Mutex
	calls    int
	recorded []Exchange
}

func (s *panicOnceSink) Record(ex Exchange) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		panic("sep2capture test: sink panic")
	}
	s.mu.Lock()
	s.recorded = append(s.recorded, ex)
	s.mu.Unlock()
}

func (s *panicOnceSink) all() []Exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Exchange, len(s.recorded))
	copy(out, s.recorded)
	return out
}

func TestPanickingSinkDoesNotStopTheDispatchGoroutine(t *testing.T) {
	sink := &panicOnceSink{}
	r := NewRecorder(sink, log.New(io.Discard, "", 0))
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	b1 := &building{id: 1, handlerRuns: 1}
	b1.req.append([]byte("x"))
	rec.finish(b1)

	b2 := &building{id: 2, handlerRuns: 1}
	b2.req.append([]byte("y"))
	rec.finish(b2)

	rec.closeFinal()

	deadline := time.Now().Add(2 * time.Second)
	for len(sink.all()) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("recorded after the panic: got %d, want 1 (the second exchange)", len(got))
	}
	if got[0].ID != 2 {
		t.Errorf("recorded exchange ID: got %d, want 2", got[0].ID)
	}
	if r.Dropped() == 0 {
		t.Error("dropped: got 0, want > 0 for the panicking call")
	}
}

// Identity must be recorded through Attach, in both cipher modes, on
// every listener the server really uses: this package's
// own pre-handshaking Listener (the first two subtests), core's
// sepTLS.WrapCCMListener, a bare gotls.NewListener (the server's real
// CCM-8 listener, sep2server.wrapMTLS), and a bare crypto/tls.NewListener
// (the GCM equivalent). Attach must refuse a connection whose handshake
// genuinely fails rather than recording an empty identity for it (see
// TestAttachRefusesAHandshakeFailure).

func TestIdentityRecordedThroughAttach(t *testing.T) {
	t.Run("GCM", func(t *testing.T) {
		m := newMaterial(t)
		addr, sink := startCaptureServer(t, m, okHandler("ok"))
		conn := rawDial(t, m, addr)
		if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = readRawHTTPMessage(t, conn)

		exchanges := waitForExchanges(t, sink, 1)
		wantLFDI := sepTLS.LFDI(m.deviceLeaf)
		wantSFDI := sepTLS.SFDI(m.deviceLeaf)
		if exchanges[0].ClientLFDI != wantLFDI {
			t.Errorf("ClientLFDI: got %q, want %q", exchanges[0].ClientLFDI, wantLFDI)
		}
		if exchanges[0].ClientSFDI != wantSFDI {
			t.Errorf("ClientSFDI: got %q, want %q", exchanges[0].ClientSFDI, wantSFDI)
		}
	})

	t.Run("CCM", func(t *testing.T) {
		m := newMaterial(t)
		tcpLn := listenTCP(t)
		gotlsLn := gotls.NewListener(tcpLn, ccmServerConfig(t, m))
		ln := NewListener(gotlsLn, nil)
		srv := &http.Server{Handler: okHandler("ok")}
		sink := NewMemorySink()
		rec := NewRecorder(sink, nil)
		wrapped := rec.Attach(srv, ln)
		go func() { _ = srv.Serve(wrapped) }()
		t.Cleanup(func() { closeRecorder(t, rec) })
		t.Cleanup(func() { _ = srv.Close() })

		client := ccmHTTPClient(t, m)
		resp, err := client.Get("https://" + tcpLn.Addr().String() + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()

		exchanges := waitForExchanges(t, sink, 1)
		wantLFDI := sepTLS.LFDI(m.deviceLeaf)
		wantSFDI := sepTLS.SFDI(m.deviceLeaf)
		if exchanges[0].ClientLFDI != wantLFDI {
			t.Errorf("ClientLFDI: got %q, want %q", exchanges[0].ClientLFDI, wantLFDI)
		}
		if exchanges[0].ClientSFDI != wantSFDI {
			t.Errorf("ClientSFDI: got %q, want %q", exchanges[0].ClientSFDI, wantSFDI)
		}
	})

	t.Run("CCM WrapCCMListener", func(t *testing.T) {
		m := newMaterial(t)
		tcpLn := listenTCP(t)
		inner := gotls.NewListener(tcpLn, ccmServerConfig(t, m))
		preHandshaken := sepTLS.WrapCCMListener(inner, log.New(io.Discard, "", 0))

		var seen observedTLS
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.set(r.TLS)
			w.WriteHeader(http.StatusOK)
		})
		srv := &http.Server{Handler: handler}
		sink := NewMemorySink()
		rec := NewRecorder(sink, nil)
		served := rec.Attach(srv, preHandshaken)
		go func() { _ = srv.Serve(served) }()
		t.Cleanup(func() { closeRecorder(t, rec) })
		t.Cleanup(func() { _ = srv.Close() })

		client := ccmHTTPClient(t, m)
		resp, err := client.Get("https://" + tcpLn.Addr().String() + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()

		assertPeerIsLeaf(t, seen.get(), m.deviceLeaf)
		exchanges := waitForExchanges(t, sink, 1)
		wantLFDI := sepTLS.LFDI(m.deviceLeaf)
		wantSFDI := sepTLS.SFDI(m.deviceLeaf)
		if exchanges[0].ClientLFDI != wantLFDI {
			t.Errorf("ClientLFDI: got %q, want %q", exchanges[0].ClientLFDI, wantLFDI)
		}
		if exchanges[0].ClientSFDI != wantSFDI {
			t.Errorf("ClientSFDI: got %q, want %q", exchanges[0].ClientSFDI, wantSFDI)
		}
	})

	t.Run("CCM bare gotls.NewListener", func(t *testing.T) {
		m := newMaterial(t)
		tcpLn := listenTCP(t)
		// Not pre-handshaken: the server's real CCM-8 listener
		// (sep2server.wrapMTLS) builds exactly this.
		bare := gotls.NewListener(tcpLn, ccmServerConfig(t, m))

		var seen observedTLS
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.set(r.TLS)
			w.WriteHeader(http.StatusOK)
		})
		srv := &http.Server{Handler: handler}
		sink := NewMemorySink()
		rec := NewRecorder(sink, nil)
		served := rec.Attach(srv, bare)
		go func() { _ = srv.Serve(served) }()
		t.Cleanup(func() { closeRecorder(t, rec) })
		t.Cleanup(func() { _ = srv.Close() })

		client := ccmHTTPClient(t, m)
		resp, err := client.Get("https://" + tcpLn.Addr().String() + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()

		assertPeerIsLeaf(t, seen.get(), m.deviceLeaf)
		exchanges := waitForExchanges(t, sink, 1)
		wantLFDI := sepTLS.LFDI(m.deviceLeaf)
		wantSFDI := sepTLS.SFDI(m.deviceLeaf)
		if exchanges[0].ClientLFDI != wantLFDI {
			t.Errorf("ClientLFDI: got %q, want %q", exchanges[0].ClientLFDI, wantLFDI)
		}
		if exchanges[0].ClientSFDI != wantSFDI {
			t.Errorf("ClientSFDI: got %q, want %q", exchanges[0].ClientSFDI, wantSFDI)
		}
	})

	t.Run("GCM bare crypto/tls.NewListener", func(t *testing.T) {
		m := newMaterial(t)
		tcpLn := listenTCP(t)
		bare := tls.NewListener(tcpLn, gcmServerConfig(t, m)) // not pre-handshaken

		var seen observedTLS
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.set(r.TLS)
			w.WriteHeader(http.StatusOK)
		})
		srv := &http.Server{Handler: handler}
		sink := NewMemorySink()
		rec := NewRecorder(sink, nil)
		served := rec.Attach(srv, bare)
		go func() { _ = srv.Serve(served) }()
		t.Cleanup(func() { closeRecorder(t, rec) })
		t.Cleanup(func() { _ = srv.Close() })

		conn := rawDial(t, m, tcpLn.Addr().String())
		if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = readRawHTTPMessage(t, conn)

		assertPeerIsLeaf(t, seen.get(), m.deviceLeaf)
		exchanges := waitForExchanges(t, sink, 1)
		wantLFDI := sepTLS.LFDI(m.deviceLeaf)
		wantSFDI := sepTLS.SFDI(m.deviceLeaf)
		if exchanges[0].ClientLFDI != wantLFDI {
			t.Errorf("ClientLFDI: got %q, want %q", exchanges[0].ClientLFDI, wantLFDI)
		}
		if exchanges[0].ClientSFDI != wantSFDI {
			t.Errorf("ClientSFDI: got %q, want %q", exchanges[0].ClientSFDI, wantSFDI)
		}
	})
}

// TestAttachRefusesAHandshakeFailure is the acceptance-level proof that a
// connection whose handshake fails never produces an exchange and gets
// closed promptly end to end. It is not, on its own, the proof that
// completeHandshake's own Close() call is what does the closing: checked by
// mutation, this test still passes with that Close() call removed, because
// net/http's own next read on the wrapped *tls.Conn returns its cached
// handshake error immediately and net/http's serve loop tears the
// connection down through its own c.close() regardless. A dial that only
// times out would not distinguish "closed" from "left hanging" either way,
// so this still reads the raw TCP socket directly and requires a prompt EOF
// or reset, not a timeout, as the end-to-end behavior this package must
// keep. TestCompleteHandshakeClosesOnFailure below is the test that is
// actually RED without completeHandshake's own Close() call.
func TestAttachRefusesAHandshakeFailure(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	bare := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	srv := &http.Server{Handler: okHandler("ok")}
	sink := NewMemorySink()
	var logBuf syncBuffer
	rec := NewRecorder(sink, log.New(&logBuf, "", 0))
	wrapped := rec.Attach(srv, bare)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })

	raw, err := net.Dial("tcp", tcpLn.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	// Not a TLS record at all: the server's handshake parse fails
	// immediately rather than waiting out any handshake deadline.
	if _, err := raw.Write([]byte("not a tls client hello\r\n\r\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	if err := raw.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 16)
	n, readErr := raw.Read(buf)
	if readErr == nil {
		t.Fatalf("read %d bytes, want the server to close after a failed handshake, not respond", n)
	}
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatal("read timed out instead of erroring: a dial that only times out does not prove the connection was closed")
	}

	if len(sink.All()) != 0 {
		t.Error("a refused connection must never produce a recorded exchange")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logBuf.String(), "TLS handshake error") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "TLS handshake error") {
		t.Errorf("want a logged handshake-error refusal, got: %s", logBuf.String())
	}
}

// Calling Attach twice on the same server must not panic.

func TestAttachTwiceDoesNotPanicOnConnectionClose(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)

	var logBuf syncBuffer
	srv := &http.Server{
		Handler:  okHandler("ok"),
		ErrorLog: log.New(&logBuf, "", 0),
	}

	sink1 := NewMemorySink()
	rec1 := NewRecorder(sink1, nil)
	_ = rec1.Attach(srv, ln) // chains onto srv.ConnState and srv.Handler; its own listener is never served

	sink2 := NewMemorySink()
	rec2 := NewRecorder(sink2, nil)
	served := rec2.Attach(srv, ln)

	go func() { _ = srv.Serve(served) }()
	t.Cleanup(func() { closeRecorder(t, rec1) })
	t.Cleanup(func() { closeRecorder(t, rec2) })
	t.Cleanup(func() { _ = srv.Close() })

	conn := rawDial(t, m, tcpLn.Addr().String())
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = readRawHTTPMessage(t, conn)

	waitForExchanges(t, sink2, 1)
	// Give the panic-recovery log line time to land if the bug is present.
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(logBuf.String(), "panic") {
		t.Errorf("connection handling panicked (see server log): %s", logBuf.String())
	}
}

// Operator decisions, 2026-09-21.

// TestKeepAliveClientClosingWhileIdleLeavesNoEmptyExchange: a keep-alive
// client that closes while idle (the ordinary way a well-behaved client
// ends a connection) must not leave a second, empty "no response" exchange
// behind, which would show every normal client as a failing one.
func TestKeepAliveClientClosingWhileIdleLeavesNoEmptyExchange(t *testing.T) {
	m := newMaterial(t)
	addr, sink := startCaptureServer(t, m, okHandler("ok"))
	conn := rawDial(t, m, addr)

	if _, err := conn.Write([]byte("GET /a HTTP/1.1\r\nHost: t\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = readRawHTTPMessage(t, conn)

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// waitForExchanges only proves a lower bound; poll for the full
	// window instead, so a delayed empty exchange cannot sneak in after
	// an early return.
	deadline := time.Now().Add(2 * time.Second)
	var exchanges []Exchange
	for time.Now().Before(deadline) {
		exchanges = sink.All()
		time.Sleep(20 * time.Millisecond)
	}
	if len(exchanges) != 1 {
		t.Fatalf("got %d exchanges, want exactly 1 (no empty idle-close exchange)", len(exchanges))
	}
}

// TestFinishSkipsAnExchangeWithNoBytesEitherDirection is the unit-level
// proof behind the acceptance test above: finish must never hand an empty
// building to the sink at all.
func TestFinishSkipsAnExchangeWithNoBytesEitherDirection(t *testing.T) {
	sink := NewMemorySink()
	r := NewRecorder(sink, nil)
	t.Cleanup(func() { closeRecorder(t, r) })
	rec := newTestConnRecorder(r)

	rec.finish(&building{id: 1})
	rec.closeFinal()

	// The shared dispatch goroutine drains its queue asynchronously, so
	// give it a window before trusting a zero count.
	deadline := time.Now().Add(300 * time.Millisecond)
	var got int
	for time.Now().Before(deadline) {
		got = len(sink.All())
		time.Sleep(10 * time.Millisecond)
	}
	if got != 0 {
		t.Errorf("got %d exchanges from an empty building, want 0", got)
	}
}

// TestAttachWithNilHandlerFallsBackToDefaultServeMux: Attach wraps
// srv.Handler unconditionally, so a nil Handler (a caller that has not set
// one, relying on net/http's own DefaultServeMux fallback) must not turn
// into a request-time panic. net/http itself substitutes DefaultServeMux
// only when srv.Handler is nil at serve time (server.go's serverHandler);
// annotate must do the same rather than calling ServeHTTP on a nil
// http.Handler.
func TestAttachWithNilHandlerFallsBackToDefaultServeMux(t *testing.T) {
	m := newMaterial(t)
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{} // no Handler set
	sink := NewMemorySink()
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })

	conn := rawDial(t, m, tcpLn.Addr().String())
	if _, err := conn.Write([]byte("GET /nowhere HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("got no response at all: a nil Handler panicked instead of falling back to DefaultServeMux")
	}
	if !bytes.Contains(raw, []byte(" 404 ")) {
		t.Errorf("response: got %q, want DefaultServeMux's 404 (no route registered)", raw)
	}

	exchanges := waitForExchanges(t, sink, 1)
	if exchanges[0].HandlerRuns != 1 {
		t.Errorf("handler runs: got %d, want 1 (DefaultServeMux itself is the handler that ran)", exchanges[0].HandlerRuns)
	}
}

// TestOptionsStarBypassesTheHandlerAndIsMarkedRejectedBeforeHandler covers a
// GOROOT net/http/server.go behavior: serverHandler.ServeHTTP swaps in
// globalOptionsHandler for "OPTIONS *" before calling srv.Handler, which is
// Attach's own annotate wrapper. annotate therefore never runs for this
// request, handlerRuns stays 0, and classify already sorts a
// written-but-unhandled response into MarkRejectedBeforeHandler like any
// other one net/http answers without calling into srv.Handler. No
// production code change is needed; this test is the missing coverage.
func TestOptionsStarBypassesTheHandlerAndIsMarkedRejectedBeforeHandler(t *testing.T) {
	m := newMaterial(t)
	addr, sink := startCaptureServer(t, m, okHandler("ok"))
	conn := rawDial(t, m, addr)
	if _, err := conn.Write([]byte("OPTIONS * HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = readRawHTTPMessage(t, conn)

	exchanges := waitForExchanges(t, sink, 1)
	if exchanges[0].HandlerRuns != 0 {
		t.Errorf("HandlerRuns: got %d, want 0 (OPTIONS * never reaches srv.Handler, so annotate never runs)", exchanges[0].HandlerRuns)
	}
	if exchanges[0].Mark != MarkRejectedBeforeHandler {
		t.Errorf("Mark: got %v, want %v", exchanges[0].Mark, MarkRejectedBeforeHandler)
	}
}

// TestHungSinkKeepsGoroutinesBoundedAcrossManyConnections is the
// reproduction for a per-connection sink queue design that would let a hung
// Sink leak one dispatch goroutine per connection, unbounded in the number
// of connections, with nothing counted in Dropped until each connection's
// own queue happened to fill on its own. One shared queue and a single
// dispatch goroutine per Recorder means the goroutine count stays flat
// regardless of how many connections stall behind the hung Sink.
func TestHungSinkKeepsGoroutinesBoundedAcrossManyConnections(t *testing.T) {
	m := newMaterial(t)
	sink := &gatedSink{gate: make(chan struct{})}

	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{Handler: okHandler("ok")}
	rec := NewRecorder(sink, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() {
		_ = srv.Close()
	})

	// Let the server's own long-lived goroutines (Serve's accept loop,
	// this package's own Listener.acceptLoop) settle before the baseline,
	// the same way assertGoroutinesSettle does in listener_test.go.
	deadlineSettle := time.Now().Add(2 * time.Second)
	baseline := runtime.NumGoroutine()
	for time.Now().Before(deadlineSettle) {
		time.Sleep(20 * time.Millisecond)
		baseline = runtime.NumGoroutine()
	}

	const connections = 30
	for i := 0; i < connections; i++ {
		conn := rawDial(t, m, tcpLn.Addr().String())
		if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatalf("write on connection %d: %v", i, err)
		}
		_ = readRawHTTPMessage(t, conn)
	}

	// Give every connection's ConnState hooks and finish() calls time to
	// run and hand their one exchange each to the shared, gated queue.
	time.Sleep(300 * time.Millisecond)

	afterConnections := runtime.NumGoroutine()
	// One dispatch goroutine total, however many connections stalled
	// behind the hung Sink: a generous margin (not exact equality) keeps
	// this robust to unrelated runtime goroutines, while still catching
	// the old per-connection growth (it would add roughly `connections`
	// goroutines here, not a handful).
	if got, want := afterConnections-baseline, 10; got > want {
		t.Errorf("goroutines grew by %d (baseline %d, after %d), want <= %d: a hung Sink must not leak one goroutine per connection", got, baseline, afterConnections, want)
	}
	if sink.callsSoFar() == 0 {
		t.Fatal("sink never received the first stalled call; the test proves nothing")
	}

	close(sink.gate)
	// srv.Close alone no longer stops the dispatch goroutine: only
	// recordingListener's own accept loop, since recordingListener.Close
	// (Q1) touches nothing but the inner listener. The caller contract is
	// srv stopped, then rec.Close, so this settle check waits on both,
	// rather than on t.Cleanup, to prove neither leaves anything running
	// afterward, per connection or otherwise.
	_ = srv.Close()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := rec.Close(closeCtx); err != nil {
		t.Fatalf("Recorder.Close: %v", err)
	}
	assertGoroutinesSettle(t, baseline)
}
