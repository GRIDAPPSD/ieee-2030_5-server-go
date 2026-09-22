package sep2capture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// TestHandlerStreamClosesOnFirstDropSoResumeFillsTheGap is items 1 and 2's
// acceptance together: a subscriber that cannot absorb one more event has
// its stream ended, not left open to miss events silently (item 1), and
// reconnecting with the id: value it last saw picks up exactly what it
// missed even though the missed exchanges publish after the one already
// delivered (item 2). sseSubscribedHook forces the overflow deterministically,
// before handleStream's own goroutine ever reads from the channel, rather
// than racing the scheduler for it.
//
// The request carries a bounded context (PR 620 review, coverage LOW: a
// close-on-drop regression used to hang the whole package instead of
// failing this one test, since neither this read nor readSSEEvents' own
// ReadString had a deadline): a real fix still returns well inside it, and
// a regression now fails with a named error instead of a package-wide
// "panic: test timed out".
//
// Mutant (segment_writer.go, publish): reverting to the bare `default:
// s.slowSubscribers.Add(1)` with no closeSubscription call makes this RED:
// r.ReadByte() returns the bounded context's error instead of io.EOF.
func TestHandlerStreamClosesOnFirstDropSoResumeFillsTheGap(t *testing.T) {
	old := subscriberBufferSize
	subscriberBufferSize = 1
	t.Cleanup(func() { subscriberBufferSize = old })

	st := newTestStore(t)
	sseSubscribedHook = func() {
		for _, id := range []uint64{1, 2, 3} {
			st.Record(makeExchange(id, id, "client-a", 32, 32))
		}
		waitQueueDrained(t, st)
	}
	t.Cleanup(func() { sseSubscribedHook = nil })

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	r := bufio.NewReader(resp.Body)

	// Buffer size 1: exactly one event lands before the second publish
	// finds it full and ends the subscription, so exactly one event is
	// delivered and the connection then closes instead of continuing.
	got := readSSEEvents(t, r, 1, time.Now().Add(5*time.Second))
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("stream after the drop: got err %v, want io.EOF (the connection must close, not continue)", err)
	}
	_ = resp.Body.Close()

	if stats := st.Stats(); stats.SlowSubscribers != 1 {
		t.Errorf("SlowSubscribers: got %d, want 1", stats.SlowSubscribers)
	}

	req, err = http.NewRequest(http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatUint(got[0].id, 10))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream (resume): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	r2 := bufio.NewReader(resp2.Body)
	resumed := readSSEEvents(t, r2, 2, time.Now().Add(5*time.Second))

	ids := make([]uint64, len(resumed))
	for i, e := range resumed {
		var body summaryJSON
		if err := json.Unmarshal([]byte(e.data), &body); err != nil {
			t.Fatalf("unmarshal resumed event: %v", err)
		}
		ids[i] = body.ID
	}
	if want := []uint64{2, 3}; !idsEqual(ids, want) {
		t.Fatalf("resumed exchange ids: got %v, want %v (the two dropped by the buffer overflow, once each)", ids, want)
	}
}

// TestHandlerStreamResumeSurvivesOutOfOrderConnections is item 2's
// acceptance: exchange ids assigned at connection open can complete, and
// so reach Record, in the opposite order (index.go's Summary.Seq doc);
// resume must still deliver every exchange exactly once. Record simulates
// the out-of-order arrival directly, the same way
// TestExchangesAfterIDAndLimitSurviveOutOfOrderArrival does: what is under
// test is summariesAfter's ordering, not id assignment upstream of Record.
//
// Mutant (store_reader.go, summariesAfter): reverting to filter and sort
// by ID instead of Seq makes this RED: the reconnect misses exchange id 1,
// whose Seq is higher than id 2's even though its own id is lower.
func TestHandlerStreamResumeSurvivesOutOfOrderConnections(t *testing.T) {
	st := newTestStore(t)
	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp1, err := http.Get(ts.URL + "/stream")
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	r1 := bufio.NewReader(resp1.Body)

	// Exchange id 2 completes (reaches Record) before exchange id 1: the
	// later-opened connection finished first.
	st.Record(makeExchange(2, 2, "client-a", 32, 32))
	first := readSSEEvents(t, r1, 1, time.Now().Add(5*time.Second))
	lastSeen := first[0].id
	_ = resp1.Body.Close()

	st.Record(makeExchange(1, 1, "client-a", 32, 32))
	waitQueueDrained(t, st)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatUint(lastSeen, 10))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream (resume): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	r2 := bufio.NewReader(resp2.Body)
	second := readSSEEvents(t, r2, 1, time.Now().Add(5*time.Second))

	var body summaryJSON
	if err := json.Unmarshal([]byte(second[0].data), &body); err != nil {
		t.Fatalf("unmarshal resumed event: %v", err)
	}
	if body.ID != 1 {
		t.Fatalf("resumed exchange id: got %d, want 1 (it completed after id 2 but must still be delivered, not skipped)", body.ID)
	}
}

// TestHandlerStreamLogsWriteDeadlineFailureInsteadOfDiscardingIt is item
// 3's acceptance: a ResponseWriter that cannot support a write deadline
// (httptest.ResponseRecorder implements neither SetWriteDeadline nor
// Unwrap, so http.ResponseController answers http.ErrNotSupported, per
// GOROOT responsecontroller.go) must reach the error log, not be discarded
// by `_ = rc.SetWriteDeadline(...)`.
//
// Mutant (handler_sse.go, handleStream): reverting the first
// SetWriteDeadline call back to `_ = rc.SetWriteDeadline(...)` makes this
// RED: logBuf never gets anything written to it, and the test times out.
func TestHandlerStreamLogsWriteDeadlineFailureInsteadOfDiscardingIt(t *testing.T) {
	var logBuf syncBuffer
	st, err := NewStore(StoreConfig{Dir: t.TempDir(), ErrorLog: log.New(&logBuf, "", 0)})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		st.handleStream(rec, req)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for logBuf.Len() == 0 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("write-deadline failure never reached the error log")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if got := logBuf.String(); !strings.Contains(got, "write-deadline") {
		t.Fatalf("error log: got %q, want it to mention the write-deadline failure", got)
	}
}

// TestHandlerStreamWithNoResumeParamIsLiveOnly is coverage-lane MEDIUM 4:
// a plain GET /stream (no after=, no Last-Event-ID) must never replay
// pre-existing history, only what publishes after the connection opens.
//
// Mutant (handler_sse.go, parseResume): returning &0 instead of nil when
// v == "" makes this RED: the two exchanges recorded before the request
// would be replayed, and the first event read would carry exchange id 1
// instead of 3.
func TestHandlerStreamWithNoResumeParamIsLiveOnly(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stream")
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	st.Record(makeExchange(3, 3, "client-a", 32, 32))
	events := readSSEEvents(t, r, 1, time.Now().Add(5*time.Second))

	var body summaryJSON
	if err := json.Unmarshal([]byte(events[0].data), &body); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if body.ID != 3 {
		t.Fatalf("first event exchange id: got %d, want 3 (pre-existing ids 1 and 2 must not be replayed)", body.ID)
	}
}

// TestHandlerStreamResumeAcrossEvictionDeliversSurvivors is coverage-lane
// MEDIUM 5: a resume point whose own exchange has since been evicted must
// still deliver every surviving exchange recorded after it, per the route
// doc's "an evicted id is silently skipped" (handler.go).
//
// Asserts the exact ordered Seq list, not just how many events came back
// (PR 620 review, coverage LOW, round 2): a replay returning the right
// count of the wrong entries, or the right entries out of order, used to
// pass this test.
func TestHandlerStreamResumeAcrossEvictionDeliversSurvivors(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: 64 * 1024, SegmentBytes: 8 * 1024})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for i := uint64(1); i <= 200; i++ {
		st.Record(makeExchange(i, i, "client-1", 512, 512))
	}
	waitQueueDrained(t, st)
	if st.Stats().EvictedSegments == 0 {
		t.Fatal("EvictedSegments: got 0, want > 0 (test setup needs eviction to have happened)")
	}
	if _, err := st.Exchange(1); err != ErrEvicted {
		t.Fatalf("Store.Exchange(1): got %v, want ErrEvicted (test setup needs id 1 evicted)", err)
	}

	survivors := st.summariesAfter(0)
	wantSeqs := make([]uint64, len(survivors))
	for i, sum := range survivors {
		wantSeqs[i] = sum.Seq
	}

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	// Seq 1 (exchange id 1's, long since evicted): every surviving
	// exchange must still come back, none silently lost because the
	// resume point itself no longer exists in the index.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream (resume): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	events := readSSEEvents(t, r, len(wantSeqs), time.Now().Add(5*time.Second))
	gotSeqs := make([]uint64, len(events))
	for i, e := range events {
		gotSeqs[i] = e.id
	}
	if !idsEqual(gotSeqs, wantSeqs) {
		t.Fatalf("replayed seqs: got %v, want %v (every surviving indexed exchange, in Seq order, none substituted)", gotSeqs, wantSeqs)
	}
}

// TestHandlerStreamDuplicateGuardCoversTheRaceWithHistoryReplay is
// coverage-lane MEDIUM 3: an id already delivered during history replay
// must never be delivered again even when it also arrives on the live
// subscribe channel, exactly the race handleStream's own comment on
// replaySet describes (an exchange indexed in the window between
// Subscribe and the history read reaches both). subscribeHook and
// replayHook inject that race directly and in order, since forcing it
// through real timing is not deterministic from outside the package.
//
// Exchange ids 7 and 8, not 1 and 2 (PR 620 review, coverage MEDIUM 2,
// round 2): with Seq starting fresh at 1, an id equal to its own Seq
// cannot tell "keyed on Seq" apart from "keyed on ID" here.
//
// Mutant (handler_sse.go, handleStream): removing the
// `if replayed.has(sum.Seq) { continue }` guard makes this RED: the
// resumed stream delivers exchange 7's summary twice instead of once.
// Mutant (handler_sse.go, handleStream): changing `replayed.add(sum.Seq)`
// to `replayed.add(sum.ID)` also makes this RED: the second event is
// exchange 7 again (the undetected duplicate), not exchange 8.
func TestHandlerStreamDuplicateGuardCoversTheRaceWithHistoryReplay(t *testing.T) {
	st := newTestStore(t)
	st.Record(makeExchange(7, 7, "client-a", 32, 32))
	waitQueueDrained(t, st)
	firstSeq := st.summariesAfter(0)[0].Seq

	subscribeHook = func(ch chan Summary) {
		// The documented race: exchange 7's own summary also lands on
		// the live channel, exactly as publish would, before the
		// history read below has even run.
		ch <- st.summariesAfter(0)[0]
	}
	t.Cleanup(func() { subscribeHook = nil })
	replayHook = func() {
		// Fires after `replayed` already holds firstSeq: exchange 8 is
		// now genuinely new and only ever reaches the client live.
		st.Record(makeExchange(8, 8, "client-a", 32, 32))
		waitQueueDrained(t, st)
	}
	t.Cleanup(func() { replayHook = nil })

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	events := readSSEEvents(t, r, 2, time.Now().Add(5*time.Second))
	if events[0].id != firstSeq {
		t.Fatalf("first event id: got %d, want %d (the history replay)", events[0].id, firstSeq)
	}
	var second summaryJSON
	if err := json.Unmarshal([]byte(events[1].data), &second); err != nil {
		t.Fatalf("unmarshal second event: %v", err)
	}
	if second.ID != 8 {
		t.Fatalf("second event exchange id: got %d, want 8 (exchange 7's replay must not repeat: with the guard broken this is exchange 7's duplicate instead)", second.ID)
	}
}

// TestHandlerStreamLastEventIDWinsOverAfterQueryParam is a coverage-lane
// LOW: per the route doc, the Last-Event-ID header takes precedence over
// after= when both are present.
//
// Mutant (handler_sse.go, parseResume): swapping the header and query
// checks makes this RED: the resumed stream would replay from after=1
// (missing nothing) instead of from Last-Event-ID's 2 (replaying only
// exchange 3).
func TestHandlerStreamLastEventIDWinsOverAfterQueryParam(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2, 3} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/stream?after=1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	events := readSSEEvents(t, r, 1, time.Now().Add(5*time.Second))
	var body summaryJSON
	if err := json.Unmarshal([]byte(events[0].data), &body); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if body.ID != 3 {
		t.Fatalf("resumed exchange id: got %d, want 3 (Last-Event-ID=2 must win over after=1)", body.ID)
	}
}

// TestSummaryJSONCarriesSeqAcrossExchangesAndStream is item 1's JSON
// contract (PR 620 review, silent-failure MEDIUM 3, round 2): /exchanges'
// list, /exchanges/{id}, and /stream's "data:" line all carry the same
// "seq" field, the value /stream resumes on. This only proves the field
// is populated everywhere the shape appears; the handoff test below
// proves the resume semantics.
//
// Mutant (handler.go, summaryOf): dropping `Seq: ex.Seq,` makes the
// "exchange summary" subtest RED (seq reads 0): summaryOf, not
// Store.Exchanges, is what /exchanges/{id} goes through, and only
// Store.Exchange needed its own Seq wired in from the index.
func TestSummaryJSONCarriesSeqAcrossExchangesAndStream(t *testing.T) {
	st := newTestStore(t)
	st.Record(makeExchange(1, 1, "client-a", 32, 32))
	waitQueueDrained(t, st)
	wantSeq := st.summariesAfter(0)[0].Seq
	if wantSeq == 0 {
		t.Fatal("test setup: wantSeq is 0, want > 0")
	}

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	t.Run("exchanges list", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/exchanges?client=client-a")
		if err != nil {
			t.Fatalf("GET /exchanges: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Exchanges []summaryJSON `json:"exchanges"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Exchanges) != 1 || body.Exchanges[0].Seq != wantSeq {
			t.Fatalf("exchanges[0]: got %+v, want seq %d", body.Exchanges, wantSeq)
		}
	})

	t.Run("exchange summary", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/exchanges/1")
		if err != nil {
			t.Fatalf("GET /exchanges/1: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var sum summaryJSON
		if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if sum.Seq != wantSeq {
			t.Errorf("seq: got %d, want %d", sum.Seq, wantSeq)
		}
	})

	t.Run("stream event", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/stream?after=0")
		if err != nil {
			t.Fatalf("GET /stream: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		r := bufio.NewReader(resp.Body)
		events := readSSEEvents(t, r, 1, time.Now().Add(5*time.Second))
		var body summaryJSON
		if err := json.Unmarshal([]byte(events[0].data), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if body.Seq != wantSeq {
			t.Errorf("data seq: got %d, want %d", body.Seq, wantSeq)
		}
		if events[0].id != wantSeq {
			t.Errorf("id: line: got %d, want %d", events[0].id, wantSeq)
		}
	})
}

// TestHandlerStreamHandoffFromExchangesListLosesNothing is item 1's
// acceptance: a client that loads /exchanges then opens /stream using the
// list's own "seq" field (never "id") sees every exchange recorded after
// the list was taken, with no gap and no repeat of what the list already
// covered.
func TestHandlerStreamHandoffFromExchangesListLosesNothing(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2, 3} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges?client=client-a")
	if err != nil {
		t.Fatalf("GET /exchanges: %v", err)
	}
	var listBody struct {
		Exchanges []summaryJSON `json:"exchanges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if len(listBody.Exchanges) != 3 {
		t.Fatalf("exchanges list: got %d, want 3", len(listBody.Exchanges))
	}
	lastSeq := listBody.Exchanges[len(listBody.Exchanges)-1].Seq

	streamResp, err := http.Get(ts.URL + "/stream?after=" + strconv.FormatUint(lastSeq, 10))
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer func() { _ = streamResp.Body.Close() }()
	r := bufio.NewReader(streamResp.Body)

	for _, id := range []uint64{4, 5} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	events := readSSEEvents(t, r, 2, time.Now().Add(5*time.Second))
	gotIDs := make([]uint64, len(events))
	for i, e := range events {
		var body summaryJSON
		if err := json.Unmarshal([]byte(e.data), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		gotIDs[i] = body.ID
	}
	if want := []uint64{4, 5}; !idsEqual(gotIDs, want) {
		t.Fatalf("handoff from /exchanges list to /stream: got exchange ids %v, want %v (no gap, no repeat of 1-3)", gotIDs, want)
	}
}

// TestHandlerStreamRejectsAResumePointBeyondAnyPublishedSeq is item 1's
// other half (PR 620 review, silent-failure MEDIUM 3, round 2): after=
// (or Last-Event-ID) naming a value higher than any Seq this Store has
// ever issued is a loud 400, not a silent empty replay: the shape a
// client hits if it hands /stream an exchange id from /exchanges instead
// of the seq field (handler.go's route doc).
//
// Mutant (handler_sse.go, handleStream): removing the maxPublishSeq check
// makes this RED: a resume point that has never existed gets a 200 with
// an empty replay instead of a 400.
func TestHandlerStreamRejectsAResumePointBeyondAnyPublishedSeq(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2, 3} {
		st.Record(makeExchange(id, id, "client-a", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stream?after=999999")
	if err != nil {
		t.Fatalf("GET /stream?after=999999: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d (999999 exceeds every Seq this Store has ever issued)", resp.StatusCode, http.StatusBadRequest)
	}
}
