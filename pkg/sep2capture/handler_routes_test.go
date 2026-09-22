package sep2capture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestStore(t testing.TB) *Store {
	t.Helper()
	st, err := NewStore(StoreConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })
	return st
}

// TestHandlerNonGETMethodGives405 is acceptance 3's first half: any
// method other than GET on a registered route must be refused, per Q7
// item 1 ("GET only (any other method 405)"). This rests on every route
// being registered with a "GET " pattern prefix, which is what gives
// net/http's ServeMux its built-in 405 behaviour.
//
// Mutant (handler.go, Handler): registering "/clients" instead of
// "GET /clients" makes this RED: ServeMux then matches POST too, and the
// handler answers 200.
func TestHandlerNonGETMethodGives405(t *testing.T) {
	st := newTestStore(t)
	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/clients", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /clients: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /clients status: got %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// TestHandlerUnknownExchangeIDGives404 is Q7 item 4's error case: an id
// above the highest this Store has ever indexed.
//
// Mutant (handler.go, writeExchangeError): swapping the ErrNotFound case
// to also answer 410 makes this RED: an id nothing has ever indexed would
// read as "evicted" rather than "never existed".
func TestHandlerUnknownExchangeIDGives404(t *testing.T) {
	st := newTestStore(t)
	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges/999999")
	if err != nil {
		t.Fatalf("GET /exchanges/999999: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestHandlerEvictedExchangeIDGives410 is acceptance 3's second half: an
// id that was indexed and has since been evicted to stay under CapBytes.
//
// Mutant (handler.go, writeExchangeError): swapping the ErrEvicted case to
// answer 404 makes this RED: the test's evicted id, which this Store did
// once index, would read as "never existed" instead of "evicted".
func TestHandlerEvictedExchangeIDGives410(t *testing.T) {
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

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges/1")
	if err != nil {
		t.Fatalf("GET /exchanges/1: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusGone)
	}
}

// TestHandlerExchangeListRequiresClientAndValidatesParams is Q7 item 4's
// "bad parameters 400" case, across the three ways /exchanges can be
// asked wrong.
//
// Mutant (handler.go, handleExchangeList): removing the `client == ""`
// check makes the first subtest RED (200 with an empty list instead of
// 400).
func TestHandlerExchangeListRequiresClientAndValidatesParams(t *testing.T) {
	st := newTestStore(t)
	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	cases := []struct {
		name  string
		query string
	}{
		{"missing client", ""},
		{"non-numeric after", "client=c1&after=not-a-number"},
		{"non-numeric limit", "client=c1&limit=not-a-number"},
		{"zero limit", "client=c1&limit=0"},
		{"negative limit", "client=c1&limit=-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/exchanges?" + tc.query)
			if err != nil {
				t.Fatalf("GET /exchanges?%s: %v", tc.query, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

// TestHandlerExchangeListLimitIsBounded proves Q7 item 1's "bounded":
// even a limit far above maxExchangesLimit never returns more than
// maxExchangesLimit entries.
//
// Mutant (handler.go, parseLimit): removing the `n > maxExchangesLimit`
// clamp makes this RED: the handler would return all 1500 entries instead
// of stopping at maxExchangesLimit.
func TestHandlerExchangeListLimitIsBounded(t *testing.T) {
	st := newTestStore(t)
	for i := uint64(1); i <= maxExchangesLimit+500; i++ {
		st.Record(makeExchange(i, i, "client-1", 64, 64))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges?client=client-1&limit=1000000")
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
	if len(body.Exchanges) != maxExchangesLimit {
		t.Fatalf("len(exchanges): got %d, want %d (maxExchangesLimit)", len(body.Exchanges), maxExchangesLimit)
	}
}

// TestHandlerClientsAndStatsAndExchangeSummaryFieldValues is the JSON
// contract itself (Q7 item 4's "the JSON is a contract"), asserting
// actual field values per data-invariants rule 1, not just "200 and
// parses".
//
// Mutant (handler.go, toSummaryJSON): swapping ClientKey for Error makes
// this RED: clientKey would come back "" instead of the client's LFDI.
func TestHandlerClientsAndStatsAndExchangeSummaryFieldValues(t *testing.T) {
	st := newTestStore(t)
	st.Record(makeExchange(1, 10, "client-a", 64, 128))
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	t.Run("clients", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/clients")
		if err != nil {
			t.Fatalf("GET /clients: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Clients []clientJSON `json:"clients"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Clients) != 1 {
			t.Fatalf("len(clients): got %d, want 1", len(body.Clients))
		}
		if body.Clients[0].Key != "client-a" {
			t.Errorf("Key: got %q, want %q", body.Clients[0].Key, "client-a")
		}
		if body.Clients[0].ExchangeCount != 1 {
			t.Errorf("ExchangeCount: got %d, want 1", body.Clients[0].ExchangeCount)
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
		if sum.ID != 1 {
			t.Errorf("ID: got %d, want 1", sum.ID)
		}
		if sum.ConnID != 10 {
			t.Errorf("ConnID: got %d, want 10", sum.ConnID)
		}
		if sum.ClientKey != "client-a" {
			t.Errorf("ClientKey: got %q, want %q", sum.ClientKey, "client-a")
		}
		if sum.Method != "GET" {
			t.Errorf("Method: got %q, want %q", sum.Method, "GET")
		}
		if sum.Path != "/x" {
			t.Errorf("Path: got %q, want %q", sum.Path, "/x")
		}
		if sum.Status != 200 {
			t.Errorf("Status: got %d, want 200", sum.Status)
		}
		if sum.ReqStored != 64 || sum.RespStored != 128 {
			t.Errorf("ReqStored/RespStored: got %d/%d, want 64/128", sum.ReqStored, sum.RespStored)
		}
	})

	t.Run("stats", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/stats")
		if err != nil {
			t.Fatalf("GET /stats: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var stats statsJSON
		if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if stats.IndexEntries != 1 {
			t.Errorf("IndexEntries: got %d, want 1", stats.IndexEntries)
		}
	})
}

// TestHandlerExchangeListAfterFiltersToNewerIDs is a coverage-lane LOW:
// after= must actually exclude ids at or below it, not just accept the
// parameter.
//
// Mutant (handler.go, handleExchangeList): passing 0 instead of after to
// Store.Exchanges makes this RED: all three ids come back instead of just
// the one above after=2.
func TestHandlerExchangeListAfterFiltersToNewerIDs(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{1, 2, 3} {
		st.Record(makeExchange(id, id, "client-1", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges?client=client-1&after=2")
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
	if len(body.Exchanges) != 1 || body.Exchanges[0].ID != 3 {
		t.Fatalf("exchanges after=2: got %v, want exactly id 3", body.Exchanges)
	}
}

// TestHandlerExchangeListDefaultLimitIs200 is a coverage-lane LOW: the
// unstated default (no limit= at all) must be defaultExchangesLimit, not
// unbounded.
//
// Mutant (handler.go, parseLimit): returning 0 (Store.Exchanges' own
// meaning of unbounded) instead of defaultExchangesLimit when v == "" makes
// this RED: all 250 recorded exchanges come back instead of 200.
func TestHandlerExchangeListDefaultLimitIs200(t *testing.T) {
	st := newTestStore(t)
	for i := uint64(1); i <= 250; i++ {
		st.Record(makeExchange(i, i, "client-1", 32, 32))
	}
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges?client=client-1")
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
	if len(body.Exchanges) != defaultExchangesLimit {
		t.Fatalf("len(exchanges) with no limit=: got %d, want %d (defaultExchangesLimit)", len(body.Exchanges), defaultExchangesLimit)
	}
}

// TestHandlerStatsDropCountersAndSummaryTruncationFlagsAreNotSwapped is a
// coverage-lane LOW: droppedQueueFull/droppedWriteError and
// reqTruncated/respTruncated are each asserted with different values on
// their two sides, so a field swap in toStatsJSON or toSummaryJSON cannot
// pass silently by both sides matching.
//
// Mutant (handler.go, toStatsJSON): swapping DroppedQueueFull and
// DroppedWriteError makes this RED: droppedQueueFull reads 0 (the real
// DroppedWriteError value) instead of > 0.
func TestHandlerStatsDropCountersAndSummaryTruncationFlagsAreNotSwapped(t *testing.T) {
	st := newTestStore(t)

	// A request-only-truncated exchange next to a response-only-truncated
	// one, so a flag swap cannot pass by both sides reading identically.
	now := time.Now()
	st.Record(Exchange{
		ID: 1, ConnID: 1, ClientLFDI: "client-1", Started: now, Ended: now,
		Request:  Direction{Bytes: []byte("GET /x HTTP/1.1\r\n\r\n"), TrueLen: 20, Truncated: true},
		Response: Direction{Bytes: []byte("HTTP/1.1 200 OK\r\n\r\n"), TrueLen: 20},
		Mark:     MarkHandled, HandlerRuns: 1,
	})
	waitQueueDrained(t, st)

	// Stalls the writer (store_stall_test.go's pattern) so a burst of
	// records overflows the byte-bounded queue: DroppedQueueFull moves,
	// DroppedWriteError (a disk failure) never does here.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	st.testBeforeWrite = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	t.Cleanup(func() { close(release) })

	st.Record(makeExchange(2, 2, "client-1", 1000, 1000))
	<-entered
	const bigDirection = 3 * 1024 * 1024
	for i := 3; i <= 40 && st.Stats().DroppedQueueFull == 0; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", bigDirection, bigDirection))
	}
	if st.Stats().DroppedQueueFull == 0 {
		t.Fatal("DroppedQueueFull: got 0, want > 0 (test setup needs the queue to overflow)")
	}

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	statsResp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer func() { _ = statsResp.Body.Close() }()
	var stats statsJSON
	if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.DroppedQueueFull == 0 {
		t.Error("droppedQueueFull: got 0, want > 0")
	}
	if stats.DroppedWriteError != 0 {
		t.Errorf("droppedWriteError: got %d, want 0 (nothing here fails a disk write)", stats.DroppedWriteError)
	}

	sumResp, err := http.Get(ts.URL + "/exchanges/1")
	if err != nil {
		t.Fatalf("GET /exchanges/1: %v", err)
	}
	defer func() { _ = sumResp.Body.Close() }()
	var sum summaryJSON
	if err := json.NewDecoder(sumResp.Body).Decode(&sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if !sum.ReqTruncated {
		t.Error("reqTruncated: got false, want true")
	}
	if sum.RespTruncated {
		t.Error("respTruncated: got true, want false")
	}
}

// TestHandlerSkippedIDBelowHighWaterMarkGives410NotFalse404 is a
// coverage-lane LOW: an id below the highest ever seen but never indexed
// (an idle keep-alive dropped upstream before Record, handler.go's own
// 404/410 doc) reads as evicted (410), the same as a truly evicted one,
// never as "never existed" (404).
//
// Mutant (handler.go, writeExchangeError): swapping the ErrEvicted case to
// answer 404 makes this RED, same as TestHandlerEvictedExchangeIDGives410,
// but for a skipped id rather than a capacity-evicted one.
func TestHandlerSkippedIDBelowHighWaterMarkGives410NotFalse404(t *testing.T) {
	st := newTestStore(t)
	st.Record(makeExchange(1, 1, "client-1", 32, 32))
	st.Record(makeExchange(3, 3, "client-1", 32, 32)) // id 2 never recorded
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges/2")
	if err != nil {
		t.Fatalf("GET /exchanges/2: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusGone)
	}
}
