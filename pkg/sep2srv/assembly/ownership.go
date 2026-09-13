package assembly

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ownershipGate is a routeRegistrar that binds path {id} to the authenticated
// caller on every route under /edev except the two in ownershipExempt.
//
// It gates at registration, from the pattern string, so a route added to any
// register*Routes helper later is covered without its author doing anything,
// and handlers that know nothing about EndDevices stay that way. The request
// is checked after the mux matched, through r.PathValue, so the request path
// is never re-parsed.
//
// Access is decided on the STORED EndDevice record, never on the path segment,
// because {id} is an opaque server-chosen index, not an identity. The caller
// is admitted when the record carries the caller's LFDI, or, on a delegable
// pattern, when the management store names the caller as the record's
// manager. Refusals are logged within a bounded budget; see denialLog.
type ownershipGate struct {
	next           routeRegistrar
	devices        store.EndDeviceStore
	devicesAbsent  bool
	managers       store.EndDeviceManagementStore
	managersAbsent bool
	identity       func(ctx context.Context) (lfdi, sfdi string, ok bool)
	identityAbsent bool
	denials        *denialLog
}

// ownershipExempt holds the only /edev patterns served without the gate.
var ownershipExempt = map[string]bool{
	// No {id} exists before registration. The handler requires an identity
	// and takes SFDI and LFDI from the certificate, never from the body.
	"POST /edev": true,
	// The collection. Its handler lists only the caller's own EndDevice and
	// those it manages.
	"GET /edev": true,
}

func newOwnershipGate(next routeRegistrar, devices store.EndDeviceStore, managers store.EndDeviceManagementStore, identity func(ctx context.Context) (lfdi, sfdi string, ok bool)) *ownershipGate {
	g := &ownershipGate{
		next:           next,
		devices:        devices,
		devicesAbsent:  store.IsAbsent(devices),
		managers:       managers,
		managersAbsent: store.IsAbsent(managers),
		identity:       identity,
		identityAbsent: identity == nil,
		denials:        newDenialLog(log.Printf),
	}
	if g.managersAbsent {
		log.Print("assembly: Stores.EndDeviceManagers is not wired: no EndDevice access is delegated to a manager")
	}
	return g
}

func (g *ownershipGate) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	if requiresOwnership(pattern) {
		h = g.wrap(h, delegable(pattern))
	}
	g.next.HandleFunc(pattern, h)
}

// requiresOwnership reports whether pattern is under /edev and not exempt.
// A gated pattern that names no {id} wildcard reads an empty id and is
// refused, which is the safe answer for a route nobody has reasoned about.
func requiresOwnership(pattern string) bool {
	if ownershipExempt[pattern] {
		return false
	}
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return false
	}
	path := pattern[i:]
	return path == "/edev" || strings.HasPrefix(path, "/edev/")
}

// delegable reports whether a manager may use pattern on a device it manages:
// GET on the record, which also serves HEAD, and every pattern strictly below
// it except the Registration. A manager never rewrites or deletes the record
// itself, and a record pattern that names no method is not delegated.
func delegable(pattern string) bool {
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return false
	}
	method, _, _ := strings.Cut(pattern[:i], " ")
	segments := strings.Split(pattern[i+1:], "/")
	if len(segments) < 2 || segments[0] != "edev" || segments[1] != "{id}" {
		return false
	}
	if len(segments) == 2 {
		return method == http.MethodGet
	}
	// The resource segment must be a literal other than the Registration: a
	// wildcard or an empty segment there would also match the Registration.
	resource := segments[2]
	return resource != "" && resource != "rg" && !strings.HasPrefix(resource, "{")
}

type ownershipDecision int

const (
	// ownershipDenied is the zero value, so an undecided request is refused.
	ownershipDenied ownershipDecision = iota
	ownershipDeviceAbsent
	ownershipStoreFailed
	ownershipAllowed
)

// Refusal reasons, as written to the denial log.
const (
	reasonNoIdentity      = "no-identity"
	reasonNoDeviceID      = "no-device-id"
	reasonAbsent          = "absent"
	reasonRecordHasNoLFDI = "record-has-no-lfdi"
	reasonNotOwner        = "not-owner"
	reasonNotManager      = "not-owner-or-manager"
)

// ownershipVerdict is the gate's answer to one request. caller and reason
// describe a refusal for the denial log; err is set only when the answer is
// ownershipStoreFailed.
type ownershipVerdict struct {
	decision ownershipDecision
	caller   string
	reason   string
	err      error
}

func (g *ownershipGate) wrap(next func(http.ResponseWriter, *http.Request), delegated bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		v := g.decide(r, delegated)
		switch v.decision {
		case ownershipAllowed:
			next(w, r)
		case ownershipStoreFailed:
			// A 500, not a 403: the gate could not establish access, and a
			// 403 would tell the client it is not authorized. The handler
			// still never runs.
			srverr.Internal(w, r, v.err)
		case ownershipDeviceAbsent:
			g.denials.record(r, v)
			http.Error(w, "not found", http.StatusNotFound)
		default:
			g.denials.record(r, v)
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
}

func (g *ownershipGate) decide(r *http.Request, delegated bool) ownershipVerdict {
	if g.identityAbsent {
		return ownershipVerdict{reason: reasonNoIdentity}
	}
	callerLFDI, _, ok := g.identity(r.Context())
	if !ok || callerLFDI == "" {
		return ownershipVerdict{reason: reasonNoIdentity}
	}
	refuse := func(reason string) ownershipVerdict {
		return ownershipVerdict{caller: callerLFDI, reason: reason}
	}
	id := r.PathValue("id")
	if id == "" {
		return refuse(reasonNoDeviceID)
	}
	if g.devicesAbsent {
		return ownershipVerdict{decision: ownershipStoreFailed, err: errors.New("ownership check has no EndDevice store to read")}
	}

	dev, err := g.devices.Get(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ownershipVerdict{decision: ownershipDeviceAbsent, caller: callerLFDI, reason: reasonAbsent}
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("ownership check could not read the EndDevice: %w", err)}
	case coreedev.OwnedBy(dev.LFDI, callerLFDI):
		return ownershipVerdict{decision: ownershipAllowed}
	case dev.LFDI == "":
		return refuse(reasonRecordHasNoLFDI)
	}

	// Management is consulted only after self fails and only on a delegable
	// pattern, so a management store outage never blocks a device's own
	// /edev/{id} routes.
	if !delegated || g.managersAbsent {
		return refuse(reasonNotOwner)
	}
	manager, err := g.managers.ManagerOf(r.Context(), dev.LFDI)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(reasonNotManager)
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("ownership check could not read the EndDevice's manager: %w", err)}
	case !coreedev.OwnedBy(manager, callerLFDI):
		return refuse(reasonNotManager)
	}
	return ownershipVerdict{decision: ownershipAllowed}
}

// Denial log bounds per window. The per-caller budget stops one caller from
// spending another's lines; the limit across callers stops a probe from many
// identities flooding the log.
const (
	denialLogPerCaller = 5
	denialLogLimit     = 100
	denialLogWindow    = time.Minute
	maxLoggedIDLen     = 64
)

// denialLog writes refusal lines within the bounds above and counts the rest
// by reason. A window opens at the first refusal after the previous one closed.
// A caller is tracked only once it has a line written, so a window holds at
// most denialLogLimit callers. A window that suppressed anything arms a
// one-shot timer that reports the count when the window closes, so the count
// does not wait for another refusal and no goroutine waits for the timer.
type denialLog struct {
	mu        sync.Mutex
	logf      func(format string, args ...any)
	now       func() time.Time
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	window    time.Duration

	windowStart time.Time
	windowEnds  time.Time // zero when no window is open
	generation  uint64
	written     int
	perCaller   map[string]int
	suppressed  map[string]int
	stopTimer   func() bool
}

func newDenialLog(logf func(format string, args ...any)) *denialLog {
	return &denialLog{
		logf: logf,
		now:  time.Now,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		window: denialLogWindow,
	}
}

func (d *denialLog) record(r *http.Request, v ownershipVerdict) {
	summary, admitted := d.admit(v)

	if summary != "" {
		d.logf("%s", summary)
	}
	if !admitted {
		return
	}
	// The id is client-chosen: it is truncated and quoted so it cannot forge a
	// log line. No field of the stored record is written.
	id := r.PathValue("id")
	if len(id) > maxLoggedIDLen {
		id = id[:maxLoggedIDLen]
	}
	d.logf("assembly: ownership gate denied %s: caller=%q id=%q reason=%s", srverr.Route(r), v.caller, id, v.reason)
}

// admit runs the critical section under the mutex, released on every path
// including a panic, so a panic here cannot leave every later refusal
// blocked on the lock.
func (d *denialLog) admit(v ownershipVerdict) (summary string, admitted bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	summary = d.closeExpiredLocked(now)
	if d.windowEnds.IsZero() {
		d.windowStart, d.windowEnds = now, now.Add(d.window)
		d.generation++
	}
	admitted = d.admitLocked(now, v)
	return summary, admitted
}

func (d *denialLog) admitLocked(now time.Time, v ownershipVerdict) bool {
	if d.written < denialLogLimit && d.perCaller[v.caller] < denialLogPerCaller {
		if d.perCaller == nil {
			d.perCaller = make(map[string]int)
		}
		d.perCaller[v.caller]++
		d.written++
		return true
	}
	if d.suppressed == nil {
		d.suppressed = make(map[string]int)
	}
	d.suppressed[v.reason]++
	if d.stopTimer == nil {
		d.armLocked(now)
	}
	return false
}

func (d *denialLog) armLocked(now time.Time) {
	gen := d.generation
	d.stopTimer = d.afterFunc(d.windowEnds.Sub(now), func() { d.flush(gen) })
}

// flush reports the window armed as generation gen, unless a refusal already
// closed it.
func (d *denialLog) flush(gen uint64) {
	d.mu.Lock()
	if gen != d.generation || d.windowEnds.IsZero() {
		d.mu.Unlock()
		return
	}
	now := d.now()
	if now.Before(d.windowEnds) {
		d.armLocked(now)
		d.mu.Unlock()
		return
	}
	summary := d.closeExpiredLocked(now)
	d.mu.Unlock()

	if summary != "" {
		d.logf("%s", summary)
	}
}

// closeExpiredLocked closes the open window if it has ended and returns its
// suppression summary, or "" when there is nothing to report. The interval is
// measured from the window's first refusal to now, not assumed to be the window
// length, because the close can come later than the window's end.
func (d *denialLog) closeExpiredLocked(now time.Time) string {
	if d.windowEnds.IsZero() || now.Before(d.windowEnds) {
		return ""
	}
	var summary string
	if len(d.suppressed) > 0 {
		total := 0
		reasons := make([]string, 0, len(d.suppressed))
		for _, reason := range slices.Sorted(maps.Keys(d.suppressed)) {
			total += d.suppressed[reason]
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, d.suppressed[reason]))
		}
		summary = fmt.Sprintf("assembly: ownership gate suppressed %d denial log lines in the last %s: %s",
			total, now.Sub(d.windowStart).Round(time.Millisecond), strings.Join(reasons, " "))
	}
	if d.stopTimer != nil {
		d.stopTimer()
	}
	d.windowStart, d.windowEnds = time.Time{}, time.Time{}
	d.written, d.perCaller, d.suppressed, d.stopTimer = 0, nil, nil, nil
	return summary
}
