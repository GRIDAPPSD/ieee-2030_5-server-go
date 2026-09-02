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
	"bufio"
	"io"
	"net"
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

	// adminAuthFailures counts presented-and-wrong admin credentials, labeled
	// by admission path. Closes the asymmetry where a benign loopback
	// admission was logged on every request but a remote key guess left no
	// record anywhere (#413).
	adminAuthFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "admin",
			Name:      "auth_failures_total",
			Help:      "Failed admin credential presentations by admission path.",
		},
		[]string{"admission_path"},
	)
)

// Notification outcome label values. Exported so the subscription manager
// records the same strings the collector declares — no stringly-typed drift.
const (
	OutcomeSuccess     = "success"
	OutcomeClientError = "client_error"
	OutcomeQueueFull   = "queue_full"

	// OutcomeOther is the bounded catch-all for any outcome string that is
	// not one of the known Outcome* values above. RecordNotification maps
	// unknown input here so an arbitrary caller-supplied string can never
	// mint a new time series (cardinality guard).
	OutcomeOther = "other"
)

// validOutcomes is the closed set of notification outcome labels. Anything
// outside it is folded to OutcomeOther by RecordNotification.
var validOutcomes = map[string]struct{}{
	OutcomeSuccess:     {},
	OutcomeClientError: {},
	OutcomeQueueFull:   {},
	OutcomeOther:       {},
}

// Admin credential admission-path label values. Exported so auth and server
// record the same strings the collector declares, avoiding stringly-typed drift.
const (
	AdminAdmissionPathForm   = "form"
	AdminAdmissionPathBearer = "bearer"
	AdminAdmissionPathMTLS   = "mtls"

	// AdminAdmissionPathOther is the bounded catch-all for an admission-path
	// string outside the known set above. RecordAdminAuthFailure folds
	// unknown input here so a caller mistake can never mint a new series.
	AdminAdmissionPathOther = "other"
)

// validAdminAdmissionPaths is the closed set of admission-path labels.
// Anything outside it is folded to AdminAdmissionPathOther.
var validAdminAdmissionPaths = map[string]struct{}{
	AdminAdmissionPathForm:   {},
	AdminAdmissionPathBearer: {},
	AdminAdmissionPathMTLS:   {},
	AdminAdmissionPathOther:  {},
}

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
// outcome SHOULD be one of the Outcome* constants; any value outside the
// known set is folded to OutcomeOther so an arbitrary string cannot expand
// the label's cardinality.
func RecordNotification(outcome string) {
	if _, ok := validOutcomes[outcome]; !ok {
		outcome = OutcomeOther
	}
	notifications.WithLabelValues(outcome).Inc()
}

// RecordAdminAuthFailure records a presented-and-wrong admin credential,
// labeled by admission path. admissionPath SHOULD be one of the
// AdminAdmissionPath* constants; any value outside the known set is folded
// to AdminAdmissionPathOther so an arbitrary string cannot expand the
// label's cardinality.
func RecordAdminAuthFailure(admissionPath string) {
	if _, ok := validAdminAdmissionPaths[admissionPath]; !ok {
		admissionPath = AdminAdmissionPathOther
	}
	adminAuthFailures.WithLabelValues(admissionPath).Inc()
}

// knownFunctionSets is the closed set of IEEE 2030.5 protocol-listener
// first-path-segments derived from the routes mounted on the protocol mux
// (assembly.BuildProtocolRouter via internal/server/assembly_seam.go).
// functionSet folds anything outside this set to "other" so an
// arbitrary or malicious path cannot expand the function_set label's
// cardinality. Sub-resources (der, fsa, sub, frq, frp, cfg, ...) live under
// /edev/{id}/... and never surface as a first segment, so they are NOT in
// this set by design. Keep in sync with the protocol router's prefix list.
var knownFunctionSets = map[string]struct{}{
	"dcap": {}, // DeviceCapability
	"tm":   {}, // Time
	"sdev": {}, // SelfDevice
	"edev": {}, // EndDevice (and all device-scoped sub-resources)
	"mup":  {}, // MirrorUsagePoint
	"dc":   {}, // DERCurve (global)
	"upt":  {}, // UsagePoint
	"rt":   {}, // ReadingType (global)
	"msg":  {}, // Messaging
	"rsps": {}, // ResponseSet
	"root": {}, // "/" or "" — discovery root
}

// functionSet extracts the IEEE 2030.5 function-set label from a request
// path: the first non-empty path segment, lowercased, then bounded to the
// known protocol function sets (knownFunctionSets). "/edev/3" → "edev",
// "/dcap" → "dcap", "/" or "" → "root", "//edev/3" → "edev" (all leading
// slashes stripped), and anything outside the known set (e.g. a probe at
// "/wp-admin") → "other". Bounding to the first segment AND to a closed set
// keeps series cardinality fixed regardless of arbitrary input.
func functionSet(path string) string {
	p := strings.TrimLeft(path, "/")
	if p == "" {
		return "root"
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	p = strings.ToLower(p)
	if _, ok := knownFunctionSets[p]; !ok {
		return "other"
	}
	return p
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

// The wrappers below restore the optional http interfaces that the embedded
// http.ResponseWriter may implement. Embedding alone does NOT promote them
// for a type assertion: a caller doing `w.(http.Flusher)` against the
// statusRecorder fails even when the underlying writer is a Flusher, which
// would silently break streaming/SSE responses (and hand a chunk-buffering
// 500 to any handler that relies on Flush). Each passthrough delegates only
// when the embedded writer actually implements the interface.

// Flush implements http.Flusher when the embedded writer does, so streaming
// handlers (e.g. SSE) can force a chunk to the client.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker when the embedded writer does, so
// connection-upgrade handlers (WebSocket, raw TCP) keep working through the
// wrapper.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ReadFrom implements io.ReaderFrom when the embedded writer does, preserving
// the zero-copy sendfile fast path for large file responses. The status is
// marked written because a successful ReadFrom emits the body (implicit 200
// if WriteHeader was never called), matching the Write path's bookkeeping.
func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		s.wroteHeader = true
		return rf.ReadFrom(src)
	}
	// Fallback: copy through Write so the contract still holds when the
	// embedded writer is not a ReaderFrom.
	s.wroteHeader = true
	return io.Copy(struct{ io.Writer }{s.ResponseWriter}, src)
}
