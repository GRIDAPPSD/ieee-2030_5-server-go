// Package activity records when each authenticated device last made a
// request to the protocol listener, so an admin surface can say whether a
// device is talking to the server. It is in-memory only: after a restart no
// device has been seen.
package activity

import (
	"strings"
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

// Recorder keeps the last request time and a request count per LFDI. LFDIs
// are compared uppercase, the form a certificate yields, so a record that
// stores another case still matches. The map gains one entry per distinct
// certificate that chains to a trusted CA and passes the ACL, registered or
// not, and entries are never removed. The zero value is not usable; a nil
// *Recorder is, and reports Unknown.
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
	lfdi = strings.ToUpper(lfdi)
	// The clock is read under the lock so a slower caller cannot store an
	// older time over a newer one.
	r.mu.Lock()
	e := r.devices[lfdi]
	e.last, e.count = r.now(), e.count+1
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
	e, ok := r.devices[strings.ToUpper(lfdi)]
	r.mu.Unlock()
	return e.last, e.count, ok
}

// LastRFC3339 returns the last request time for lfdi as an RFC 3339 UTC
// string, or nil when none was recorded or the Recorder is nil. It is the one
// formatter behind every JSON field that reports a last-seen time.
func (r *Recorder) LastRFC3339(lfdi string) *string {
	at, _, ok := r.Last(lfdi)
	if !ok {
		return nil
	}
	v := at.UTC().Format(time.RFC3339)
	return &v
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
