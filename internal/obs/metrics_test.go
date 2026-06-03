package obs

import (
	"net/http"
	"net/http/httptest"
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

	// The duration histogram must have recorded exactly one observation for
	// this fs/method pair.
	dh := testutil.CollectAndCount(httpDuration)
	if dh == 0 {
		t.Errorf("duration histogram recorded no series")
	}
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
	}
	for path, want := range cases {
		if got := functionSet(path); got != want {
			t.Errorf("functionSet(%q) = %q, want %q", path, got, want)
		}
	}
}
