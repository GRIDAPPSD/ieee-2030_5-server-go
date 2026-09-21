package sep2capture

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startCaptureServer serves handler over a fresh TLS listener with Attach
// installed, and returns the address to dial and the sink Attach was given.
func startCaptureServer(t testing.TB, m material, handler http.Handler) (addr string, sink *MemorySink) {
	t.Helper()
	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	srv := &http.Server{Handler: handler}
	sink = NewMemorySink()
	wrapped := Attach(srv, tlsLn, sink)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { _ = srv.Close() })
	return tcpLn.Addr().String(), sink
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

// waitForExchanges polls sink until it holds at least n exchanges. Attach
// hands each exchange to the sink off the connection's own goroutine
// (design Q5), so a test cannot read sink.All() the instant its client
// read returns; it has to wait for that handoff to land.
func waitForExchanges(t testing.TB, sink *MemorySink, n int) []Exchange {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ex := sink.All()
		if len(ex) >= n {
			return ex
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForExchanges: got %d exchanges, want %d, after 5s", len(ex), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	srv := &http.Server{Handler: okHandler("ok")}

	var previousCalls atomic.Int64
	srv.ConnState = func(net.Conn, http.ConnState) { previousCalls.Add(1) }

	sink := NewMemorySink()
	wrapped := Attach(srv, tlsLn, sink)
	go func() { _ = srv.Serve(wrapped) }()
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

func newTestConnRecorder(rs *recorderSet) *connRecorder {
	return newConnRecorder(rs, 1, "test-conn:1")
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
	rs := newRecorderSet(sink)
	rec := newTestConnRecorder(rs)

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

// TestCarryOverFoldsBackWhenTheSameExchangeKeepsReading proves the peeked
// byte is not lost if a further read shows it was not, after all, the next
// request's start: recordInbound folds it back into the still-open
// exchange, in the order it arrived.
func TestCarryOverFoldsBackWhenTheSameExchangeKeepsReading(t *testing.T) {
	sink := NewMemorySink()
	rs := newRecorderSet(sink)
	rec := newTestConnRecorder(rs)

	rec.open(1)
	rec.recordInbound([]byte("x"), true)
	rec.recordInbound([]byte("yz"), false)
	rec.closeFinal()

	exchanges := waitForExchanges(t, sink, 1)
	if got, want := string(exchanges[0].Request.Bytes), "xyz"; got != want {
		t.Errorf("request: got %q, want %q", got, want)
	}
}
