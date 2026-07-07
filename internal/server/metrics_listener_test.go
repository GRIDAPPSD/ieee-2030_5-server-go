package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// TestStartMetricsServerServesMetrics asserts the dedicated metrics listener
// serves GET /metrics with 200 + Prometheus exposition, mounts ONLY that
// route, and shuts down cleanly. Internal test so it can reach the
// unexported helper and its handler directly.
func TestStartMetricsServerServesMetrics(t *testing.T) {
	errCh := make(chan error, 1)
	srv, err := startMetricsServer("127.0.0.1:0", errCh)
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	mux := srv.Handler

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "# TYPE ") {
		t.Errorf("GET /metrics body is not Prometheus exposition:\n%s", rec.Body.String())
	}

	// Only /metrics is mounted: any other path 404s.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("GET / on metrics mux = %d, want 404 (only /metrics mounted)", rec2.Code)
	}

	// GET-only mount: POST /metrics must not return 200.
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec3.Code == http.StatusOK {
		t.Errorf("POST /metrics = 200; want method rejection (GET-only mount)")
	}

	select {
	case err := <-errCh:
		t.Fatalf("metrics server errored before shutdown: %v", err)
	case <-time.After(50 * time.Millisecond):
		// healthy: still serving
	}
}

// TestStartMetricsServerBadAddr asserts a bad bind address returns a wrapped
// error rather than panicking or silently succeeding.
func TestStartMetricsServerBadAddr(t *testing.T) {
	errCh := make(chan error, 1)
	_, err := startMetricsServer("256.256.256.256:99999", errCh)
	if err == nil {
		t.Fatal("startMetricsServer with bad addr returned nil error")
	}
	if !strings.Contains(err.Error(), "metrics listen") {
		t.Errorf("error not wrapped with context: %v", err)
	}
}

// L1: the metrics listener is default-OFF. Run starts it only when
// cfg.MetricsAddr != "" (after resolution). This pins the gate signal:
// an empty SEP2_METRICS_ADDR resolves to "" so Run skips startMetricsServer
// entirely — no listener, no unauthenticated /metrics surface by default.
func TestMetricsListenerDefaultOff(t *testing.T) {
	t.Parallel()
	if got := config.ResolveMetricsBind(""); got != "" {
		t.Errorf("ResolveMetricsBind(\"\") = %q, want \"\" (empty addr must leave the listener disabled)", got)
	}
}
