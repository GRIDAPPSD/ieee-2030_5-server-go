package obs

// Test note: the obs collectors register against the package-global
// prometheus.DefaultRegisterer, which persists across tests in this package.
// These tests therefore assert on the DELTA of a counter/histogram around the
// operation under test (before/after), never on an absolute value — that keeps
// them order-independent and immune to series accumulated by sibling tests.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestHandlerServesExposition asserts the /metrics handler returns 200 and
// Prometheus exposition output (a HELP line for one of our collectors).
func TestHandlerServesExposition(t *testing.T) {
	// Touch a collector so it appears in the exposition with a non-zero
	// series (a freshly-registered CounterVec exposes no series until a
	// label combination is observed).
	httpRequests.WithLabelValues("dcap", http.MethodGet, "200").Inc()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# HELP sep2_http_requests_total") {
		t.Errorf("exposition missing sep2_http_requests_total HELP line; body:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE sep2_http_requests_total counter") {
		t.Errorf("exposition missing sep2_http_requests_total TYPE line")
	}
}

// TestMiddlewareIncrementsRequestCounter asserts the middleware increments
// the request counter with the correct function_set/method/status labels —
// not merely that it doesn't panic (per data-invariants: assert values).
func TestMiddlewareIncrementsRequestCounter(t *testing.T) {
	const fs, method, status = "edev", http.MethodGet, "201"

	before := testutil.ToFloat64(httpRequests.WithLabelValues(fs, method, status))
	dCountBefore := histogramCount(t, fs, method)

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated) // 201 → exercises statusRecorder
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(method, "/edev/3", nil) // function_set = "edev"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("downstream status not propagated: got %d, want 201", rec.Code)
	}
	after := testutil.ToFloat64(httpRequests.WithLabelValues(fs, method, status))
	if after-before != 1 {
		t.Errorf("request counter delta = %v, want 1 (labels fs=%s method=%s status=%s)",
			after-before, fs, method, status)
	}

	// L2: assert the histogram recorded exactly one observation for THIS
	// fs/method label vector (its _count), not merely that some series
	// exists. Read the _count sample off the exposition handler (the Observer
	// returned by WithLabelValues is not a Collector, so ToFloat64 can't be
	// used on it directly).
	dCountAfter := histogramCount(t, fs, method)
	if dCountAfter-dCountBefore != 1 {
		t.Errorf("duration histogram _count delta for fs=%s method=%s = %v, want 1",
			fs, method, dCountAfter-dCountBefore)
	}
}

// histogramCount reads the sep2_http_request_duration_seconds_count sample for
// a specific {function_set,method} label vector off the /metrics exposition.
// Returns 0 when the series has no observations yet.
func histogramCount(t *testing.T, fs, method string) float64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	prefix := `sep2_http_request_duration_seconds_count{function_set="` + fs +
		`",method="` + method + `"}`
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parse histogram _count %q: %v", fields[len(fields)-1], err)
		}
		return v
	}
	return 0
}

// TestMiddlewareDefaultStatus asserts a handler that never calls WriteHeader
// is recorded as status 200 (Go's implicit 200-on-first-Write).
func TestMiddlewareDefaultStatus(t *testing.T) {
	before := testutil.ToFloat64(httpRequests.WithLabelValues("tm", http.MethodGet, "200"))

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("now"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/tm", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	after := testutil.ToFloat64(httpRequests.WithLabelValues("tm", http.MethodGet, "200"))
	if after-before != 1 {
		t.Errorf("default-status counter delta = %v, want 1", after-before)
	}
}

func TestRecordConnState(t *testing.T) {
	before := testutil.ToFloat64(tlsHandshakes.WithLabelValues("active"))
	RecordConnState("active")
	after := testutil.ToFloat64(tlsHandshakes.WithLabelValues("active"))
	if after-before != 1 {
		t.Errorf("conn-state counter delta = %v, want 1", after-before)
	}
}

func TestRecordNotification(t *testing.T) {
	cases := []string{OutcomeSuccess, OutcomeClientError, OutcomeQueueFull}
	for _, outcome := range cases {
		before := testutil.ToFloat64(notifications.WithLabelValues(outcome))
		RecordNotification(outcome)
		after := testutil.ToFloat64(notifications.WithLabelValues(outcome))
		if after-before != 1 {
			t.Errorf("notification counter[%s] delta = %v, want 1", outcome, after-before)
		}
	}
}

func TestFunctionSet(t *testing.T) {
	cases := map[string]string{
		"/edev/3":       "edev",
		"/dcap":         "dcap",
		"/upt/1/mr/2/r": "upt",
		"/":             "root",
		"":              "root",
		"/EDEV":         "edev", // lowercased
		// Copilot: strip ALL leading slashes — the "//edev/3" empty-label
		// case used to yield "" (an empty function_set label).
		"//edev/3": "edev",
		"///dcap":  "dcap",
		// Copilot: bound to the known protocol function sets — arbitrary or
		// probe paths fold to "other", capping cardinality.
		"/wp-admin":                    "other",
		"/etc/passwd":                  "other",
		"/edevil":                      "other", // not "edev": full first segment must match
		"/" + strings.Repeat("x", 200): "other", // long junk segment folds, no new series
		"/der":                         "other", // der is a sub-resource, never a first segment
		"/rsps/1/rsp":                  "rsps",  // known multi-segment stays bounded to first
	}
	for path, want := range cases {
		if got := functionSet(path); got != want {
			t.Errorf("functionSet(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestRecordNotificationBoundsUnknownOutcome asserts an outcome string outside
// the known Outcome* set is folded to OutcomeOther rather than minting a new
// time series (Copilot cardinality guard). Asserts BOTH that "other" moves and
// that the arbitrary label produced no series of its own.
func TestRecordNotificationBoundsUnknownOutcome(t *testing.T) {
	const bogus = "totally-made-up-outcome"

	otherBefore := testutil.ToFloat64(notifications.WithLabelValues(OutcomeOther))
	RecordNotification(bogus)
	otherAfter := testutil.ToFloat64(notifications.WithLabelValues(OutcomeOther))

	if otherAfter-otherBefore != 1 {
		t.Errorf("OutcomeOther delta = %v, want 1 (unknown outcome must fold to other)",
			otherAfter-otherBefore)
	}
	// The bogus label must NOT have created its own series.
	if got := testutil.ToFloat64(notifications.WithLabelValues(bogus)); got != 0 {
		t.Errorf("bogus outcome %q minted a series with value %v, want 0", bogus, got)
	}
}

// TestFunctionSetBoundsUnknownPath asserts unknown first-segments fold to
// "other" through the Middleware path (end-to-end), so a probe sweep cannot
// expand the function_set label cardinality.
func TestFunctionSetBoundsUnknownPath(t *testing.T) {
	before := testutil.ToFloat64(httpRequests.WithLabelValues("other", http.MethodGet, "200"))

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/wp-login.php", nil))

	after := testutil.ToFloat64(httpRequests.WithLabelValues("other", http.MethodGet, "200"))
	if after-before != 1 {
		t.Errorf("unknown-path function_set=other delta = %v, want 1", after-before)
	}
}

// TestStatusRecorderPreservesOptionalInterfaces asserts the wrapper restores
// http.Flusher / http.Hijacker / io.ReaderFrom from the embedded writer
// (Dutch M1). A response writer that implements Flusher must remain a Flusher
// through the wrapper, else streaming handlers silently break.
func TestStatusRecorderPreservesOptionalInterfaces(t *testing.T) {
	// httptest.ResponseRecorder implements http.Flusher; assert it survives
	// the wrapper as a type assertion.
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	if _, ok := interface{}(sr).(http.Flusher); !ok {
		t.Fatal("statusRecorder does not satisfy http.Flusher")
	}
	if _, ok := interface{}(sr).(http.Hijacker); !ok {
		t.Fatal("statusRecorder does not satisfy http.Hijacker")
	}
	if _, ok := interface{}(sr).(io.ReaderFrom); !ok {
		t.Fatal("statusRecorder does not satisfy io.ReaderFrom")
	}

	// Flush must delegate to the embedded recorder (recorder.Flushed flips).
	sr.Flush()
	if !rec.Flushed {
		t.Error("statusRecorder.Flush did not delegate to the embedded Flusher")
	}

	// ReadFrom against a recorder copies the source into the body and marks
	// the status as written (implicit 200 bookkeeping).
	n, err := sr.ReadFrom(strings.NewReader("stream-bytes"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("stream-bytes")) {
		t.Errorf("ReadFrom n = %d, want %d", n, len("stream-bytes"))
	}
	if !sr.wroteHeader {
		t.Error("ReadFrom did not mark the response as written")
	}
	if rec.Body.String() != "stream-bytes" {
		t.Errorf("ReadFrom body = %q, want %q", rec.Body.String(), "stream-bytes")
	}
}
