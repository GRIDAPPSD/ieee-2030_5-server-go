// Package activity records when each authenticated device last made a
// request to the protocol listener, so an admin surface can say whether a
// device is talking to the server. It is in-memory only: after a restart no
// device has been seen.
package activity

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Comms is a device's communication state, derived from its last request.
type Comms string

const (
	// Online: the last request is younger than the offline threshold.
	Online Comms = "online"
	// Offline: the last request is at least the threshold old.
	Offline Comms = "offline"
	// NotSeen: no request from this device since the server started.
	NotSeen Comms = "not_seen"
	// Unknown: nothing records requests (no Recorder is wired).
	Unknown Comms = "unknown"
)

// DefaultOfflineAfter is the silence after which a device reads Offline when
// the operator has not set a threshold.
const DefaultOfflineAfter = 5 * time.Minute

// Recorder keeps the last request time and a request count per LFDI. The
// map holds only LFDIs that passed identity and the ACL, so it is bounded by
// the device fleet, not by request volume. The zero value is not usable; a
// nil *Recorder is, and reports Unknown.
type Recorder struct {
	now func() time.Time

	mu      sync.Mutex
	devices map[string]entry
}

type entry struct {
	last  time.Time
	count uint64
}

// NewWithClock is New with a caller-supplied clock, for tests.
func NewWithClock(now func() time.Time) *Recorder {
	return &Recorder{now: now, devices: make(map[string]entry)}
}

// New returns a Recorder that reads the wall clock.
func New() *Recorder { return NewWithClock(time.Now) }

// Record notes a request from lfdi. It is a no-op on a nil Recorder or a
// blank lfdi.
func (r *Recorder) Record(lfdi string) {
	if r == nil || lfdi == "" {
		return
	}
	at := r.now()
	r.mu.Lock()
	e := r.devices[lfdi]
	e.last, e.count = at, e.count+1
	r.devices[lfdi] = e
	r.mu.Unlock()
}

// Last returns the last request time and request count for lfdi, and false
// when none was recorded.
func (r *Recorder) Last(lfdi string) (time.Time, uint64, bool) {
	if r == nil {
		return time.Time{}, 0, false
	}
	r.mu.Lock()
	e, ok := r.devices[lfdi]
	r.mu.Unlock()
	return e.last, e.count, ok
}

// State classifies lfdi at now: Online while the last request is younger
// than offlineAfter, Offline from exactly offlineAfter on, NotSeen when
// there is none, and Unknown on a nil Recorder.
func (r *Recorder) State(lfdi string, now time.Time, offlineAfter time.Duration) Comms {
	if r == nil {
		return Unknown
	}
	last, _, ok := r.Last(lfdi)
	switch {
	case !ok:
		return NotSeen
	case now.Sub(last) >= offlineAfter:
		return Offline
	default:
		return Online
	}
}

// Middleware records each request that reaches it, under the LFDI identity
// reports. Place it inside the identity and ACL layers: a request they
// refused never reaches it, so a refused request does not count as the
// device being in contact. A request with no identity passes through
// unrecorded.
func (r *Recorder) Middleware(identity func(ctx context.Context) (lfdi, sfdi string, ok bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if lfdi, _, ok := identity(req.Context()); ok {
				r.Record(lfdi)
			}
			next.ServeHTTP(w, req)
		})
	}
}
