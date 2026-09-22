package sep2capture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
