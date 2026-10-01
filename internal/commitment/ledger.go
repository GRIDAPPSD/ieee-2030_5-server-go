package commitment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// GrantSource reads flow reservation grants from wherever they are stored.
type GrantSource interface {
	// GrantsInFleet returns every response of the fleet whose interval has a
	// positive duration, cancelled or not: liveness is the ledger's test.
	GrantsInFleet(ctx context.Context, fleetKey string) ([]Grant, error)
	// Grant returns the response with this mRID, or an error wrapping
	// ErrNoGrant when there is none.
	Grant(ctx context.Context, mrid string) (Grant, error)
}

// ControlSource reads admin-issued DER controls. Only controls with a
// lifecycle record are returned, so boot fixture and CSIP loader controls
// are never counted.
type ControlSource interface {
	ControlsInFleet(ctx context.Context, fleetKey string) ([]Control, error)
	ExecutionsOf(ctx context.Context, grantMRID string) ([]Control, error)
}

// ErrNoGrant is what a GrantSource returns when no response has the mRID.
// It is the only error the ledger reads as an absent grant; any other
// failure, a store.ErrNotFound from a resolver included, is internal.
var ErrNoGrant = errors.New("commitment: no such grant")

// ErrNoLedger is returned by Within on a nil Ledger, so an unwired ledger
// refuses every check instead of passing it.
var ErrNoLedger = errors.New("commitment: no ledger wired")

// ErrFleetNotLocked is returned by a View asked about a fleet its Within
// call did not lock: a check outside the lock could be stale by the time
// the caller writes.
var ErrFleetNotLocked = errors.New("commitment: fleet not locked by this Within call")

// Ledger serializes "check, then write" per fleet. It keeps no copy of any
// commitment: every check reads the sources under the fleet lock, so there
// is no second truth to drift from the stores. One Ledger per process;
// two would mean two lock maps and no serialization.
type Ledger struct {
	grants   GrantSource
	controls ControlSource

	// mu guards fleetLocks only, never the per-fleet work. Entries are not
	// pruned: there is one per fleet, bounded by the number of aggregators
	// and standalone devices.
	mu         sync.Mutex
	fleetLocks map[string]*sync.Mutex
}

// NewLedger builds a Ledger over its two sources. Both are required.
func NewLedger(grants GrantSource, controls ControlSource) *Ledger {
	if grants == nil || controls == nil {
		panic("commitment: NewLedger: grants and controls must not be nil")
	}
	return &Ledger{grants: grants, controls: controls, fleetLocks: make(map[string]*sync.Mutex)}
}

// View is the only way to run a check. It is valid only inside the fn
// passed to Within, which holds the fleet locks for as long as fn runs.
type View interface {
	// CheckGrant refuses a new grant on w when a live grant or a live plain
	// control of the fleet overlaps it. except names a grant being replaced
	// (a revision), which is ignored along with its executions.
	CheckGrant(ctx context.Context, fleetKey string, w *Window, except string) error

	// CheckControl refuses a control proposal: a plain one that overlaps a
	// live grant, or an execution that breaks any bound of its grant.
	CheckControl(ctx context.Context, p Proposal) error
}

// Proposal is a DER control about to be issued.
type Proposal struct {
	FleetKey  string
	Window    Window
	GrantMRID string // "" for a plain dispatch
	TargetW   *sep2.ActivePower
	Reach     int

	// Supersedes names controls the issuer will mark superseded at
	// Window.Start; each is counted only up to that instant.
	Supersedes []string
}

// Within locks every key in fleetKeys, calls fn with a View valid only
// inside fn, and unlocks. Keys are locked in ascending order so two calls
// naming the same fleets in different orders cannot deadlock. An empty key
// set or an empty key is refused: a check of fleet "" would silently group
// every unresolved device together. A nil Ledger refuses with ErrNoLedger.
func (l *Ledger) Within(ctx context.Context, fleetKeys []string, fn func(View) error) error {
	if l == nil {
		return ErrNoLedger
	}
	keys := slices.Clone(fleetKeys)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) == 0 || keys[0] == "" {
		return errors.New("commitment: Within needs at least one non-empty fleet key")
	}

	for _, k := range keys {
		m := l.fleetLock(k)
		m.Lock()
		defer m.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	v := &view{ledger: l, locked: keys}
	defer v.done.Store(true)
	return fn(v)
}

func (l *Ledger) fleetLock(key string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.fleetLocks[key]
	if !ok {
		m = &sync.Mutex{}
		l.fleetLocks[key] = m
	}
	return m
}

type view struct {
	ledger *Ledger
	locked []string // sorted
	done   atomic.Bool
}

func (v *view) guard(fleetKey string) error {
	if v.done.Load() {
		panic("commitment: View used after its Within call returned")
	}
	if _, ok := slices.BinarySearch(v.locked, fleetKey); !ok {
		return fmt.Errorf("%w: %q", ErrFleetNotLocked, fleetKey)
	}
	return nil
}

func (v *view) CheckGrant(ctx context.Context, fleetKey string, w *Window, except string) error {
	if err := v.guard(fleetKey); err != nil {
		return err
	}
	if w == nil || w.Duration == 0 {
		return nil
	}

	grants, err := v.ledger.grants.GrantsInFleet(ctx, fleetKey)
	if err != nil {
		return fmt.Errorf("commitment: reading grants of fleet %s: %w", fleetKey, err)
	}
	// Grants are checked before controls are read, so a grant conflict is
	// refused even when the control read would fail.
	live, _ := Live(grants, nil)
	for _, g := range live {
		if g.MRID != except && g.Window.Overlaps(*w) {
			return &ConflictError{Code: ConflictFleetWindow, MRID: g.MRID}
		}
	}

	controls, err := v.ledger.controls.ControlsInFleet(ctx, fleetKey)
	if err != nil {
		return fmt.Errorf("commitment: reading controls of fleet %s: %w", fleetKey, err)
	}
	_, plain := Live(grants, controls)
	for _, c := range plain {
		if c.Window.Overlaps(*w) {
			return &ConflictError{Code: ConflictFleetWindow, MRID: c.MRID}
		}
	}
	return nil
}

// Live sorts a fleet's grants and controls into what the ledger enforces.
// A grant is live while it is not cancelled and has an interval. A control
// that is not cancelled is a plain dispatch unless it executes a live
// grant: one whose grant is cancelled or gone counts as plain (design 5.5),
// which fails closed. Executions of live grants sit inside their grant's
// window and are in neither result. Both results keep their input order.
func Live(grants []Grant, controls []Control) (live []Grant, plain []Control) {
	liveMRIDs := make(map[string]bool, len(grants))
	for _, g := range grants {
		if g.CancelledAt != nil || g.Window == nil {
			continue
		}
		liveMRIDs[g.MRID] = true
		live = append(live, g)
	}
	for _, c := range controls {
		if c.Cancelled || (c.GrantMRID != "" && liveMRIDs[c.GrantMRID]) {
			continue
		}
		plain = append(plain, c)
	}
	return live, plain
}

// Commitments is what a fleet is committed to: its live grants, each with
// its live executions, and its plain controls.
type Commitments struct {
	Grants []CommittedGrant
	Plain  []Control
}

// CommittedGrant is a live grant with the executions that are not
// cancelled, read the way an execution check reads them (ExecutionsOf).
type CommittedGrant struct {
	Grant
	Executions []Control
}

// Commitments reads fleetKey's commitments under its fleet lock, so no
// write lands between the grant and control reads, and keeps those whose
// window covers an instant after now. Only the end decides: a commitment
// that has not started yet is still one.
func (l *Ledger) Commitments(ctx context.Context, fleetKey string, now int64) (Commitments, error) {
	var out Commitments
	err := l.Within(ctx, []string{fleetKey}, func(View) error {
		grants, err := l.grants.GrantsInFleet(ctx, fleetKey)
		if err != nil {
			return fmt.Errorf("commitment: reading grants of fleet %s: %w", fleetKey, err)
		}
		controls, err := l.controls.ControlsInFleet(ctx, fleetKey)
		if err != nil {
			return fmt.Errorf("commitment: reading controls of fleet %s: %w", fleetKey, err)
		}
		live, plain := Live(grants, controls)
		for _, g := range live {
			if !coversAfter(*g.Window, now) {
				continue
			}
			cg := CommittedGrant{Grant: g}
			// ExecutionsOf refuses an empty mRID, and a control cannot link
			// to a grant without one, so such a grant has no executions.
			if g.MRID != "" {
				execs, err := l.controls.ExecutionsOf(ctx, g.MRID)
				if err != nil {
					return fmt.Errorf("commitment: reading executions of grant %s: %w", g.MRID, err)
				}
				for _, c := range execs {
					if !c.Cancelled {
						cg.Executions = append(cg.Executions, c)
					}
				}
			}
			out.Grants = append(out.Grants, cg)
		}
		for _, c := range plain {
			if coversAfter(c.Window, now) {
				out.Plain = append(out.Plain, c)
			}
		}
		return nil
	})
	if err != nil {
		return Commitments{}, err
	}
	return out, nil
}

// coversAfter reports whether w covers any instant after now. A window
// clipped to zero duration by a supersede covers nothing.
func coversAfter(w Window, now int64) bool {
	return w.Duration > 0 && w.End() > now
}

func (v *view) CheckControl(ctx context.Context, p Proposal) error {
	if err := v.guard(p.FleetKey); err != nil {
		return err
	}
	if p.GrantMRID == "" {
		return v.checkPlain(ctx, p)
	}
	return v.checkExecution(ctx, p)
}

// checkPlain refuses a plain dispatch over a live grant of its fleet. Plain
// against plain is left to 2030.5's own per-device rules (operator
// decision on #714).
func (v *view) checkPlain(ctx context.Context, p Proposal) error {
	if p.Window.Duration == 0 {
		return nil
	}
	grants, err := v.ledger.grants.GrantsInFleet(ctx, p.FleetKey)
	if err != nil {
		return fmt.Errorf("commitment: reading grants of fleet %s: %w", p.FleetKey, err)
	}
	live, _ := Live(grants, nil)
	for _, g := range live {
		if g.Window.Overlaps(p.Window) {
			return &ConflictError{Code: ConflictFleetWindow, MRID: g.MRID}
		}
	}
	return nil
}

func (v *view) checkExecution(ctx context.Context, p Proposal) error {
	// A reach below one would make the power and energy sums vanish.
	if p.Reach < 1 {
		return fmt.Errorf("commitment: execution of %s has reach %d, want at least 1", p.GrantMRID, p.Reach)
	}
	g, err := v.ledger.grants.Grant(ctx, p.GrantMRID)
	if err != nil {
		if errors.Is(err, ErrNoGrant) {
			return &ConflictError{Code: ConflictGrantNotLive, MRID: p.GrantMRID}
		}
		return fmt.Errorf("commitment: reading grant %s: %w", p.GrantMRID, err)
	}

	execs, err := v.ledger.controls.ExecutionsOf(ctx, g.MRID)
	if err != nil {
		return fmt.Errorf("commitment: reading executions of grant %s: %w", g.MRID, err)
	}
	existing := make([]Control, 0, len(execs))
	for _, c := range execs {
		if c.Cancelled {
			continue
		}
		if slices.Contains(p.Supersedes, c.MRID) {
			c.Window = c.Window.ClipAt(p.Window.Start)
		}
		existing = append(existing, c)
	}

	// The proposal has no mRID yet; naming the grant for its own failures
	// is design 5.3's "409 with the grant's mRID".
	proposal := Control{
		MRID:      g.MRID,
		FleetKey:  p.FleetKey,
		Window:    p.Window,
		GrantMRID: g.MRID,
		TargetW:   p.TargetW,
		Reach:     p.Reach,
	}
	return FitsGrant(g, existing, &proposal)
}
