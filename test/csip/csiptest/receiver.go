// Package csiptest — Notification receiver.
//
// NotificationReceiver is an in-process HTTPS-not-required HTTP server
// that records every Notification body POSTed to it. Built for the CSIP
// V1.2 conformance harness's subscription/notification surface
// (CORE-018/019, AGG-001, MAINT-004/005/006, ERR-002) so test bodies do
// not have to re-implement the "stand up a tiny listener, race-safe
// append into a slice, drain on cleanup" pattern five times.
//
// The receiver is plain http (httptest.NewServer). The IEEE 2030.5
// notification POST path runs over the same TLS as the rest of the
// protocol surface in production, but for harness use we don't need to
// re-prove TLS; the value here is to capture the server's outgoing
// Notification body deterministically. Tests asserting TLS conformance
// of the notification path will need a different receiver and can build
// one on this skeleton.
//
// Lifecycle is bound to t via t.Cleanup; callers must not call Close
// themselves. Concurrent Notification POSTs from multiple workers are
// safe: every receive path takes a mutex before mutating the captured
// slice.
//
// Companion to: csiptest.BootServer (server side), subscription.Manager
// (notification fan-out).
//
// IEEE-087.

package csiptest

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// ReceivedNotification is the captured record of a single POSTed
// Notification. Body is the raw request body verbatim — tests that need
// to assert wire shape (Content-Type, namespace prefix, etc.) read this.
// Notification is the parsed sep2 envelope and is non-nil only when the
// body unmarshalled cleanly; tests asserting structural malformation can
// inspect Body and ignore Notification.
type ReceivedNotification struct {
	// Body is the raw request body. Cheap to keep — Notifications are
	// small (a few hundred bytes), test cardinality is small.
	Body []byte
	// ContentType is the request's Content-Type header verbatim. The
	// server emits "application/sep+xml"; an unexpected value would be
	// a regression.
	ContentType string
	// Notification is the parsed body. Nil when the body did not
	// unmarshal as a sep2.Notification — tests can branch on this.
	Notification *sep2.Notification
	// ReceivedAt is wall-clock time the receiver finished reading the
	// body. Useful for relative-ordering assertions across multiple
	// subscriptions delivered to the same receiver.
	ReceivedAt time.Time
}

// NotificationReceiver is an HTTP listener that records POSTed
// Notifications. The zero value is not usable; construct via
// NewNotificationReceiver.
//
// Methods are safe for concurrent use; the underlying httptest.Server
// runs its handler on its own goroutine, and tests typically poll Wait
// from the test goroutine while the booted server's notifier delivers
// on a worker goroutine.
type NotificationReceiver struct {
	srv *httptest.Server

	mu       sync.Mutex
	received []ReceivedNotification
}

// NewNotificationReceiver boots an in-process HTTP listener and
// registers t.Cleanup to tear it down at test end. Every POST against
// the returned URL records the request body. Non-POST methods receive
// 405 — the receiver is not a general-purpose listener, and a stray GET
// from a misconfigured test should fail loudly.
//
// The handler returns 200 OK on every recorded POST. Tests that need
// the receiver to return a specific status (e.g. 503 for ERR-002 fault
// injection, 400 for delete-on-400 semantics) should use the
// WithStatusCode option.
func NewNotificationReceiver(t *testing.T, opts ...ReceiverOption) *NotificationReceiver {
	t.Helper()

	cfg := receiverCfg{status: http.StatusOK}
	for _, opt := range opts {
		opt(&cfg)
	}

	rcv := &NotificationReceiver{}

	rcv.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "receiver accepts POST only", http.StatusMethodNotAllowed)
			return
		}
		// LimitReader to bound the read; spec Notifications are well
		// under a KB but a misconfigured test should not pin memory.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		rec := ReceivedNotification{
			Body:        body,
			ContentType: r.Header.Get("Content-Type"),
			ReceivedAt:  time.Now(),
		}
		// Best-effort parse; an unparseable body is still recorded so
		// the test can assert on the malformed wire shape. The
		// underlying csip flow only emits well-formed Notifications,
		// so a Notification==nil case is informational, not fatal.
		var n sep2.Notification
		if uerr := xml.Unmarshal(body, &n); uerr == nil {
			rec.Notification = &n
		}

		rcv.mu.Lock()
		rcv.received = append(rcv.received, rec)
		rcv.mu.Unlock()

		w.WriteHeader(cfg.status)
	}))
	t.Cleanup(rcv.srv.Close)
	return rcv
}

// URL returns the receiver's base URL (e.g. http://127.0.0.1:PORT).
// Pass this verbatim as a Subscription's NotificationURI.
func (r *NotificationReceiver) URL() string {
	return r.srv.URL
}

// Count returns the number of Notifications received so far. Cheap;
// callers can poll this in a loop without contention.
func (r *NotificationReceiver) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.received)
}

// Snapshot returns a copy of every Notification recorded so far. The
// returned slice is independent of the receiver's internal state —
// further deliveries do not mutate it.
func (r *NotificationReceiver) Snapshot() []ReceivedNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ReceivedNotification, len(r.received))
	copy(out, r.received)
	return out
}

// Wait blocks until at least n Notifications have been received or
// timeout elapses, whichever is first. Returns the captured slice (a
// snapshot) on success. On timeout it returns the slice it has and ok
// false — callers t.Fatal at their preferred message.
//
// Polling interval is 5ms — fast enough that a 100ms timeout still
// leaves headroom against scheduler jitter, slow enough that a healthy
// run doesn't burn CPU spinning.
func (r *NotificationReceiver) Wait(n int, timeout time.Duration) (snap []ReceivedNotification, ok bool) {
	deadline := time.Now().Add(timeout)
	for {
		got := r.Snapshot()
		if len(got) >= n {
			return got, true
		}
		if time.Now().After(deadline) {
			return got, false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Reset clears the captured slice. Useful when a test reuses the same
// receiver across phases (e.g. CORE-019's parallel/cancel/malformed
// progression) and wants to assert each phase in isolation.
func (r *NotificationReceiver) Reset() {
	r.mu.Lock()
	r.received = nil
	r.mu.Unlock()
}

// receiverCfg holds the resolved receiver configuration. Internal — not
// exported.
type receiverCfg struct {
	status int
}

// ReceiverOption configures NewNotificationReceiver. Apply via the
// functional-options pattern.
type ReceiverOption func(*receiverCfg)

// WithStatusCode sets the HTTP status code the receiver returns on every
// recorded POST. Use this when a test needs to drive the server's
// receiver-side fault path (e.g. 4xx for delete-on-400 semantics, 503
// for ERR-002 transient-failure semantics). Default: 200 OK.
func WithStatusCode(code int) ReceiverOption {
	return func(c *receiverCfg) { c.status = code }
}
