package sep2capture

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withShortSSETimings shrinks the package's SSE deadline-extension and
// heartbeat vars for the duration of one test, restoring them on cleanup,
// so a test can prove the extension mechanism in milliseconds instead of
// the real minutes-scale defaults. Mirrors Store's own testBeforeWrite
// test-speed pattern.
func withShortSSETimings(t *testing.T, extension, heartbeat time.Duration) {
	t.Helper()
	oldExt, oldHB := sseWriteDeadlineExtension, sseHeartbeatInterval
	sseWriteDeadlineExtension, sseHeartbeatInterval = extension, heartbeat
	t.Cleanup(func() { sseWriteDeadlineExtension, sseHeartbeatInterval = oldExt, oldHB })
}

// sseEvent is one parsed "id: N\ndata: ...\n\n" block.
type sseEvent struct {
	id   uint64
	data string
}

// readSSEEvents reads exactly n SSE events (skipping blank lines and ":"
// comment/heartbeat lines) from r, or fails the test after deadline.
func readSSEEvents(t testing.TB, r *bufio.Reader, n int, deadline time.Time) []sseEvent {
	t.Helper()
	out := make([]sseEvent, 0, n)
	var pending sseEvent
	haveID := false
	for len(out) < n {
		if time.Now().After(deadline) {
			t.Fatalf("readSSEEvents: got %d of %d events before deadline", len(out), n)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("readSSEEvents: ReadString: %v (got %d of %d events)", err, len(out), n)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, ":"):
			// heartbeat/comment line, not part of any event
		case strings.HasPrefix(line, "id: "):
			id, perr := strconv.ParseUint(strings.TrimPrefix(line, "id: "), 10, 64)
			if perr != nil {
				t.Fatalf("readSSEEvents: bad id line %q: %v", line, perr)
			}
			pending.id = id
			haveID = true
		case strings.HasPrefix(line, "data: "):
			pending.data = strings.TrimPrefix(line, "data: ")
		case line == "" && haveID:
			out = append(out, pending)
			pending = sseEvent{}
			haveID = false
		}
	}
	return out
}

// TestHandlerStreamSurvivesPastServerWriteTimeout is acceptance 1: a
// stream held past a 10s WriteTimeout and a 30s one keeps delivering.
// Scaled down 100x (100ms, 300ms) so the test runs in well under a
// second; withShortSSETimings shrinks the deadline-extension mechanism
// proportionally so the same ratio (extension well above WriteTimeout,
// heartbeat well below it) holds at either scale.
//
// Mutant (handler_sse.go, handleStream/writeSSEEvent): removing every
// rc.SetWriteDeadline call makes this RED: the server's own WriteTimeout,
// set once before the handler runs, is never overridden, and the second
// write after sleeping past it fails.
func TestHandlerStreamSurvivesPastServerWriteTimeout(t *testing.T) {
	for _, writeTimeout := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond} {
		t.Run(writeTimeout.String(), func(t *testing.T) {
			withShortSSETimings(t, 5*writeTimeout, writeTimeout/2)

			st := newTestStore(t)
			ts := httptest.NewUnstartedServer(st.Handler())
			ts.Config.WriteTimeout = writeTimeout
			ts.Start()
			defer ts.Close()

			resp, err := http.Get(ts.URL + "/stream")
			if err != nil {
				t.Fatalf("GET /stream: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			r := bufio.NewReader(resp.Body)

			// Sleep past the server's original WriteTimeout, several
			// times over, with heartbeats ticking throughout. If the
			// deadline is not being extended, the connection is already
			// dead by the time this returns.
			time.Sleep(4 * writeTimeout)

			st.Record(makeExchange(1, 1, "client-a", 32, 32))
			events := readSSEEvents(t, r, 1, time.Now().Add(5*time.Second))
			if events[0].id != 1 {
				t.Errorf("event id: got %d, want 1", events[0].id)
			}
		})
	}
}

// TestHandlerStreamResumeDeliversExactlyMissedEvents is acceptance 2.
// after=0 asks for every already-indexed exchange (id > 0), once each;
// a later reconnect with Last-Event-ID gets exactly what was recorded
// after it, once each, in both cases with the store's own goroutine (not
// this test) doing the recording concurrently with the read. Omitting
// after= and Last-Event-ID entirely, which neither request here does,
// means "live only, no replay": the tab's history load is the separate
// bounded /exchanges route, not this one, so a plain GET /stream never
// has to hold the whole index in memory just to open the tab.
//
// Mutant (store_reader.go, summariesAfter): changing `id > afterID` to
// `id >= afterID` makes this RED on the reconnect: id 3 (already seen,
// named as Last-Event-ID) would be replayed a second time, and the two
// events this test reads after reconnecting would be [3, 4] instead of
// the wanted [4, 5].
//
// The duplicate-suppression path this route also depends on (an id that
// races onto both the history replay and the live subscribe channel,
// per the comment in handleStream) is not reachable deterministically
// from outside the package: store_reader.go's add-then-publish order
// guarantees the replay snapshot always already contains anything a
// concurrent write could also still be racing onto the channel, so
// forcing that overlap from a test would mean controlling the store's
// internal goroutine timing, not just its public API. That guard is
// proven directly instead, in TestReplaySetHasDistinguishesReplayedIDs.
func TestHandlerStreamResumeDeliversExactlyMissedEvents(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2, 3} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	// after=0: every already-indexed exchange, once each.
	resp1, err := http.Get(ts.URL + "/stream?after=0")
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	r1 := bufio.NewReader(resp1.Body)
	first := readSSEEvents(t, r1, 3, time.Now().Add(5*time.Second))
	_ = resp1.Body.Close()
	var firstIDs []uint64
	for _, e := range first {
		firstIDs = append(firstIDs, e.id)
	}
	if want := []uint64{1, 2, 3}; !idsEqual(firstIDs, want) {
		t.Fatalf("fresh connect ids: got %v, want %v", firstIDs, want)
	}

	// Missed window: recorded while nobody was connected.
	for _, id := range []uint64{4, 5} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	// Reconnect naming the last id the "client" saw: exactly the missed
	// two, no repeat of 1-3.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", "3")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream (resume): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	r2 := bufio.NewReader(resp2.Body)
	second := readSSEEvents(t, r2, 2, time.Now().Add(5*time.Second))
	var secondIDs []uint64
	for _, e := range second {
		secondIDs = append(secondIDs, e.id)
	}
	if want := []uint64{4, 5}; !idsEqual(secondIDs, want) {
		t.Fatalf("resumed ids: got %v, want %v", secondIDs, want)
	}
}

func idsEqual(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestHandlerStreamSlowReaderIsDroppedWithoutStoppingCapture is
// acceptance 5. subscriberBufferSize is shrunk to 1 and the client's TCP
// receive buffer to a few bytes, then the client stops reading right
// after the response headers: the server's own writes to that socket
// must eventually block, which is what proves this exercises the same
// backpressure a genuinely slow browser tab would cause, not just an
// idle one. Store.Record must keep succeeding throughout regardless.
//
// Mutant (segment_writer.go, publish): changing the `case ch <- sum:` /
// `default:` select to an unconditional blocking `ch <- sum` makes this
// RED: the test times out waiting for waitQueueDrained, because the
// writer goroutine would itself stall delivering to the stalled
// subscriber.
func TestHandlerStreamSlowReaderIsDroppedWithoutStoppingCapture(t *testing.T) {
	old := subscriberBufferSize
	subscriberBufferSize = 1
	t.Cleanup(func() { subscriberBufferSize = old })

	st := newTestStore(t)
	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(64)
	}

	if _, err := fmt.Fprintf(conn, "GET /stream HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.Host); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var buf bytes.Buffer
	for {
		line := readLine(t, conn, &buf)
		if line == "\r\n" {
			break // end of headers; stop reading, simulating a stalled reader
		}
	}

	// n is well within the store's own normal write-queue throughput
	// (writeChanCapacity, unrelated to this test) at this record size, so
	// a failure to index all of them points at the SSE subscriber, not
	// at unrelated backpressure between Record and the writer goroutine.
	const n = 200
	for i := uint64(1); i <= n; i++ {
		st.Record(makeExchange(i, i, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	stats := st.Stats()
	if stats.DroppedQueueFull != 0 {
		t.Fatalf("DroppedQueueFull: got %d, want 0 (the stalled SSE reader must never reach back into Record's own queue)", stats.DroppedQueueFull)
	}
	if stats.IndexEntries != n {
		t.Fatalf("IndexEntries: got %d, want %d (capture must keep recording despite a slow subscriber)", stats.IndexEntries, n)
	}
	if stats.SlowSubscribers == 0 {
		t.Fatal("SlowSubscribers: got 0, want > 0 (the stalled reader should have been dropped and counted)")
	}
}

// TestReplaySetHasDistinguishesReplayedIDs is the direct proof for
// handleStream's duplicate guard (handler_sse.go): an id added to the set
// during history replay reads back true, and one never added reads back
// false. See TestHandlerStreamResumeDeliversExactlyMissedEvents's doc for
// why the guard is proven here rather than by forcing the race it guards
// against.
//
// Mutant (handler_sse.go, replaySet.has): `return false` unconditionally
// makes this RED on the first assertion (a replayed id reading back as
// not replayed).
func TestReplaySetHasDistinguishesReplayedIDs(t *testing.T) {
	r := make(replaySet)
	r.add(1)
	r.add(2)

	if !r.has(1) {
		t.Error("has(1): got false, want true (1 was added)")
	}
	if !r.has(2) {
		t.Error("has(2): got false, want true (2 was added)")
	}
	if r.has(3) {
		t.Error("has(3): got true, want false (3 was never added)")
	}
}
