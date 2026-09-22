package sep2capture

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// startCaptureServerWithStore mirrors recording_test.go's
// startCaptureServer, but records into a real Store instead of MemorySink,
// so TestExchangeBytesRoundTrip exercises the whole path Q7 item 3's
// second acceptance names: capture listener, Recorder, and this PR's Sink.
func startCaptureServerWithStore(t *testing.T, m material, handler http.Handler) (addr string, st *Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	tcpLn := listenTCP(t)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(t, m))
	ln := NewListener(tlsLn, nil)
	srv := &http.Server{Handler: handler}
	rec := NewRecorder(st, nil)
	wrapped := rec.Attach(srv, ln)
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { closeRecorder(t, rec) })
	t.Cleanup(func() { _ = srv.Close() })
	return tcpLn.Addr().String(), st
}

// TestExchangeBytesRoundTrip is Q7 item 3's second acceptance: bytes read
// back through Exchange equal the bytes a real client sent and received
// through a real Attach'd server, over the whole listener-Recorder-Store
// path, not just Store.Record called directly.
//
// Mutant (store_reader.go, Exchange): removing the
// `+ int64(recordHeaderLen)` from the ReadAt offset makes this RED, because
// Exchange then reads starting at each record's own header bytes instead
// of its payload.
func TestExchangeBytesRoundTrip(t *testing.T) {
	m := newMaterial(t)
	addr, st := startCaptureServerWithStore(t, m, okHandler("round trip body"))
	conn := rawDial(t, m, addr)

	reqs := []string{
		"GET /a HTTP/1.1\r\nHost: t\r\n\r\n",
		"GET /b HTTP/1.1\r\nHost: t\r\n\r\n",
		"GET /c HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n",
	}
	rawResps := make([][]byte, len(reqs))
	for i, req := range reqs {
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write request %d: %v", i, err)
		}
		rawResps[i] = readRawHTTPMessage(t, conn)
	}

	sums := waitForExchangeCountAnyClient(t, st, len(reqs))
	if len(sums) != len(reqs) {
		t.Fatalf("got %d exchange summaries, want exactly %d", len(sums), len(reqs))
	}

	for i, sum := range sums {
		ex, err := st.Exchange(sum.ID)
		if err != nil {
			t.Fatalf("Exchange(%d): %v", sum.ID, err)
		}
		if !bytes.Equal(ex.Request.Bytes, []byte(reqs[i])) {
			t.Errorf("exchange %d request: got %q, want %q", i, ex.Request.Bytes, reqs[i])
		}
		if !bytes.Equal(ex.Response.Bytes, rawResps[i]) {
			t.Errorf("exchange %d response: got %q, want %q", i, ex.Response.Bytes, rawResps[i])
		}
		if ex.Mark != MarkHandled {
			t.Errorf("exchange %d mark: got %v, want handled", i, ex.Mark)
		}
	}

	clients := st.Clients()
	if len(clients) != 1 {
		t.Fatalf("Clients(): got %d, want 1", len(clients))
	}
	if clients[0].ExchangeCount != uint64(len(reqs)) {
		t.Errorf("client ExchangeCount: got %d, want %d", clients[0].ExchangeCount, len(reqs))
	}
}

// waitForExchangeCountAnyClient waits for st to hold n exchanges under
// whatever single client key the test's one connection was recorded
// under, then returns them via Clients()+Exchanges() rather than assuming
// the key in advance (the client's LFDI is derived by the recorder from
// the handshake, not chosen by this test).
func waitForExchangeCountAnyClient(t testing.TB, st *Store, n int) []Summary {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, c := range st.Clients() {
			if sums := st.Exchanges(c.Key, 0, 0); len(sums) >= n {
				return sums
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForExchangeCountAnyClient: no client reached %d exchanges after 5s", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
