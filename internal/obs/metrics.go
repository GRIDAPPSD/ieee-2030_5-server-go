// Package obs holds the Prometheus instrumentation for sep2server: the
// collector definitions, the HTTP middleware that records request count and
// latency, and the connection-state and subscription/notification counters
// wired in at their respective call sites.
//
// The collectors register against prometheus.DefaultRegisterer (via
// promauto) so promhttp.Handler() exposes them on the dedicated plain-HTTP
// metrics listener (see cmd/sep2server + internal/server). Metrics are
// served ONLY on that listener — never on the mTLS protocol listener or the
// auth-gated admin listener.
package obs

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "sep2"

var (
	// httpRequests counts protocol HTTP requests, labeled by IEEE 2030.5
	// function set (the first path segment, e.g. "edev", "dcap"), method,
	// and response status. Labeling by function set rather than the raw
	// path keeps cardinality bounded — the path carries resource IDs
	// (/edev/{id}, /upt/{uptId}/mr/...) that would otherwise explode the
	// series count.
	httpRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total protocol HTTP requests by function set, method, and status.",
		},
		[]string{"function_set", "method", "status"},
	)

	// httpDuration observes protocol HTTP request latency in seconds,
	// labeled by function set and method. Default buckets cover the
	// sub-millisecond-to-multi-second range typical of a LAN SEP2 device.
	httpDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Protocol HTTP request latency in seconds by function set and method.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"function_set", "method"},
	)

	// tlsHandshakes counts protocol-listener TLS connection-state
	// transitions, labeled by http.ConnState string (new, active, idle,
	// hijacked, closed). A handshake failure surfaces as a connection that
	// reaches StateNew/StateClosed without ever going StateActive; the
	// per-state counts let an operator chart that ratio.
	tlsHandshakes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "tls",
			Name:      "connection_state_total",
			Help:      "Protocol-listener TLS connection-state transitions by state.",
		},
		[]string{"state"},
	)

	// notifications counts subscription notification delivery outcomes,
	// labeled by outcome: "success", "client_error" (receiver 4xx),
	// "queue_full" (bounded worker queue dropped the task before send).
	notifications = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "subscription",
			Name:      "notifications_total",
			Help:      "Subscription notification delivery outcomes.",
		},
		[]string{"outcome"},
	)
)

// Notification outcome label values. Exported so the subscription manager
// records the same strings the collector declares — no stringly-typed drift.
const (
	OutcomeSuccess     = "success"
	OutcomeClientError = "client_error"
	OutcomeQueueFull   = "queue_full"
)

// Handler returns the Prometheus exposition handler. Mount it on the
// dedicated metrics listener at GET /metrics ONLY.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware wraps next so every served request increments the request
// counter and observes its latency, labeled by function set, method, and
// (for the counter) status. The function set is derived from the first
// path segment; an empty/"/" path maps to "root".
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs := functionSet(r.URL.Path)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		start := time.Now()
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start).Seconds()

		httpRequests.WithLabelValues(fs, r.Method, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(fs, r.Method).Observe(elapsed)
	})
}

// RecordConnState records a protocol-listener connection-state transition.
// Wired into http.Server.ConnState on the protocol server.
func RecordConnState(state string) {
	tlsHandshakes.WithLabelValues(state).Inc()
}

// RecordNotification records a subscription notification delivery outcome.
// outcome MUST be one of the Outcome* constants.
func RecordNotification(outcome string) {
	notifications.WithLabelValues(outcome).Inc()
}

// functionSet extracts the IEEE 2030.5 function-set label from a request
// path: the first non-empty path segment, lowercased. "/edev/3" → "edev",
// "/dcap" → "dcap", "/" or "" → "root". Bounding the label to the first
// segment keeps series cardinality low (resource IDs live in deeper
// segments).
func functionSet(path string) string {
	p := strings.TrimPrefix(path, "/")
	if p == "" {
		return "root"
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return strings.ToLower(p)
}

// statusRecorder captures the response status code so the request counter
// can label by it. Defaults to 200 when WriteHeader is never called (Go's
// implicit 200 on first Write).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}
