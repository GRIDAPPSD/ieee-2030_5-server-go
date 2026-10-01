package commitment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// fakeGrants and fakeControls are in-memory sources. err, when set, is
// returned by every read so a test can prove a failed read refuses.
type fakeGrants struct {
	grants []Grant
	err    error
}

func (f *fakeGrants) GrantsInFleet(_ context.Context, fleetKey string) ([]Grant, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Grant
	for _, g := range f.grants {
		if g.FleetKey == fleetKey && g.Window != nil && g.Window.Duration > 0 {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeGrants) Grant(_ context.Context, mrid string) (Grant, error) {
	if f.err != nil {
		return Grant{}, f.err
	}
	for _, g := range f.grants {
		if g.MRID == mrid {
			return g, nil
		}
	}
	return Grant{}, ErrNoGrant
}

type fakeControls struct {
	controls []Control
	err      error
}

func (f *fakeControls) ControlsInFleet(_ context.Context, fleetKey string) ([]Control, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Control
	for _, c := range f.controls {
		if c.FleetKey == fleetKey {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeControls) ExecutionsOf(_ context.Context, grantMRID string) ([]Control, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Control
	for _, c := range f.controls {
		if c.GrantMRID == grantMRID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeControls) ControlsAndExecutions(ctx context.Context, fleetKey string, grantMRIDs []string) ([]Control, map[string][]Control, error) {
	inFleet, err := f.ControlsInFleet(ctx, fleetKey)
	if err != nil {
		return nil, nil, err
	}
	execs := make(map[string][]Control, len(grantMRIDs))
	for _, m := range grantMRIDs {
		if execs[m], err = f.ExecutionsOf(ctx, m); err != nil {
			return nil, nil, err
		}
	}
	return inFleet, execs, nil
}

var errStoreDown = errors.New("fake: store unreachable")

func plainControl(mrid string, w Window) Control {
	return Control{
		MRID:     mrid,
		Scope:    "edev-1/fsa-1/derp-1",
		FleetKey: "FLEET1",
		Window:   w,
		TargetW:  &sep2.ActivePower{Value: 500},
		Reach:    1,
	}
}

func grantOn(mrid string, w Window) Grant {
	g := baseGrant()
	g.MRID = mrid
	g.Window = &w
	return g
}

// checkGrant runs CheckGrant inside Within, as every caller must.
func checkGrant(t *testing.T, l *Ledger, fleet string, w *Window, except string) error {
	t.Helper()
	return l.Within(context.Background(), []string{fleet}, func(v View) error {
		return v.CheckGrant(context.Background(), fleet, w, except)
	})
}

func checkControl(t *testing.T, l *Ledger, p Proposal) error {
	t.Helper()
	return l.Within(context.Background(), []string{p.FleetKey}, func(v View) error {
		return v.CheckControl(context.Background(), p)
	})
}

func TestCheckGrant(t *testing.T) {
	t.Parallel()
	cancelledAt := int64(500)
	w := &Window{Start: 1000, Duration: 600}

	tests := []struct {
		name      string
		grants    []Grant
		controls  []Control
		window    *Window
		except    string
		wantCode  ConflictCode
		wantMRID  string
		wantFree  bool
		fleetKeys string
	}{
		{name: "empty fleet is free", window: w, wantFree: true},
		{
			name:     "overlapping live grant names the grant",
			grants:   []Grant{grantOn("g-live", Window{Start: 1500, Duration: 600})},
			window:   w,
			wantCode: ConflictFleetWindow, wantMRID: "g-live",
		},
		{
			name:     "overlapping live plain control names the control",
			controls: []Control{plainControl("c-live", Window{Start: 1599, Duration: 10})},
			window:   w,
			wantCode: ConflictFleetWindow, wantMRID: "c-live",
		},
		{
			name:     "grant touching at an instant is free",
			grants:   []Grant{grantOn("g-touch", Window{Start: 1600, Duration: 600})},
			window:   w,
			wantFree: true,
		},
		{
			name:     "grant sharing one second conflicts",
			grants:   []Grant{grantOn("g-one", Window{Start: 1599, Duration: 600})},
			window:   w,
			wantCode: ConflictFleetWindow, wantMRID: "g-one",
		},
		{
			name: "cancelled grant is not counted",
			grants: []Grant{func() Grant {
				g := grantOn("g-cancelled", Window{Start: 1000, Duration: 600})
				g.CancelledAt = &cancelledAt
				return g
			}()},
			window:   w,
			wantFree: true,
		},
		{
			name: "cancelled control is not counted",
			controls: []Control{func() Control {
				c := plainControl("c-cancelled", Window{Start: 1000, Duration: 600})
				c.Cancelled = true
				return c
			}()},
			window:   w,
			wantFree: true,
		},
		{
			name:     "grant in another fleet is not counted",
			grants:   []Grant{func() Grant { g := grantOn("g-other", *w); g.FleetKey = "FLEET2"; return g }()},
			window:   w,
			wantFree: true,
		},
		{
			name:     "the grant being replaced is ignored",
			grants:   []Grant{grantOn("g-old", *w)},
			window:   w,
			except:   "g-old",
			wantFree: true,
		},
		{
			name:   "an execution of the grant being replaced is ignored",
			grants: []Grant{grantOn("g-old", *w)},
			controls: []Control{func() Control {
				c := baseExecution()
				c.GrantMRID = "g-old"
				c.Window = *w
				return c
			}()},
			window:   w,
			except:   "g-old",
			wantFree: true,
		},
		{
			name: "an execution of a live grant is covered by that grant's window",
			grants: []Grant{func() Grant {
				g := grantOn("g-a", Window{Start: 0, Duration: 1000})
				return g
			}()},
			controls: []Control{func() Control {
				c := baseExecution()
				c.GrantMRID = "g-a"
				c.Window = Window{Start: 0, Duration: 1000}
				return c
			}()},
			window:   w,
			wantFree: true,
		},
		{
			name: "an execution whose grant is gone counts as plain",
			controls: []Control{func() Control {
				c := baseExecution()
				c.MRID = "c-orphan"
				c.GrantMRID = "g-gone"
				c.Window = *w
				return c
			}()},
			window:   w,
			wantCode: ConflictFleetWindow, wantMRID: "c-orphan",
		},
		{
			name:     "a nil window commits nothing",
			grants:   []Grant{grantOn("g-live", *w)},
			window:   nil,
			wantFree: true,
		},
		{
			name:     "a zero-duration window commits nothing",
			grants:   []Grant{grantOn("g-live", *w)},
			window:   &Window{Start: 1000},
			wantFree: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := NewLedger(&fakeGrants{grants: tc.grants}, &fakeControls{controls: tc.controls})
			err := checkGrant(t, l, "FLEET1", tc.window, tc.except)
			if tc.wantFree {
				if err != nil {
					t.Fatalf("CheckGrant = %v, want nil", err)
				}
				return
			}
			wantConflict(t, err, tc.wantCode, tc.wantMRID)
		})
	}
}

func TestCheckControl_Plain(t *testing.T) {
	t.Parallel()
	w := Window{Start: 1000, Duration: 600}

	t.Run("overlapping live grant refuses naming the grant", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{grants: []Grant{grantOn("g-live", Window{Start: 1599, Duration: 10})}}, &fakeControls{})
		err := checkControl(t, l, Proposal{FleetKey: "FLEET1", Window: w, TargetW: &sep2.ActivePower{Value: 1}, Reach: 1})
		wantConflict(t, err, ConflictFleetWindow, "g-live")
	})
	t.Run("touching live grant is free", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{grants: []Grant{grantOn("g-live", Window{Start: 1600, Duration: 10})}}, &fakeControls{})
		if err := checkControl(t, l, Proposal{FleetKey: "FLEET1", Window: w, Reach: 1}); err != nil {
			t.Fatalf("CheckControl = %v, want nil", err)
		}
	})
	t.Run("another plain control on the same window is free", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{}, &fakeControls{controls: []Control{plainControl("c-1", w)}})
		if err := checkControl(t, l, Proposal{FleetKey: "FLEET1", Window: w, Reach: 1}); err != nil {
			t.Fatalf("CheckControl = %v, want nil (operator decision: plain controls may share a window)", err)
		}
	})
	t.Run("cancelled grant is not counted", func(t *testing.T) {
		t.Parallel()
		at := int64(1)
		g := grantOn("g-cancelled", w)
		g.CancelledAt = &at
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{})
		if err := checkControl(t, l, Proposal{FleetKey: "FLEET1", Window: w, Reach: 1}); err != nil {
			t.Fatalf("CheckControl = %v, want nil", err)
		}
	})
}

func TestCheckControl_Execution(t *testing.T) {
	t.Parallel()
	g := baseGrant() // [1000, 4600), +10000 Wh, 4000 W

	execProposal := func() Proposal {
		return Proposal{
			FleetKey:  "FLEET1",
			Window:    Window{Start: 1000, Duration: 1800},
			GrantMRID: g.MRID,
			TargetW:   &sep2.ActivePower{Value: -1000},
			Reach:     1,
		}
	}

	t.Run("an execution inside every bound is accepted", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{})
		if err := checkControl(t, l, execProposal()); err != nil {
			t.Fatalf("CheckControl = %v, want nil", err)
		}
	})
	t.Run("a reversed target names the grant", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{})
		p := execProposal()
		p.TargetW = &sep2.ActivePower{Value: 1000}
		wantConflict(t, checkControl(t, l, p), ConflictDirection, g.MRID)
	})
	t.Run("an absent grant is not live", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{}, &fakeControls{})
		wantConflict(t, checkControl(t, l, execProposal()), ConflictGrantNotLive, g.MRID)
	})
	t.Run("a cancelled grant is not live", func(t *testing.T) {
		t.Parallel()
		at := int64(1)
		cg := g
		cg.CancelledAt = &at
		l := NewLedger(&fakeGrants{grants: []Grant{cg}}, &fakeControls{})
		wantConflict(t, checkControl(t, l, execProposal()), ConflictGrantNotLive, g.MRID)
	})
	t.Run("live executions count toward power, naming the other execution", func(t *testing.T) {
		t.Parallel()
		other := baseExecution()
		other.MRID = "ctrl-other"
		other.TargetW = &sep2.ActivePower{Value: -3500}
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{controls: []Control{other}})
		p := execProposal()
		p.TargetW = &sep2.ActivePower{Value: -501}
		wantConflict(t, checkControl(t, l, p), ConflictPower, "ctrl-other")

		p.TargetW = &sep2.ActivePower{Value: -500}
		if err := checkControl(t, l, p); err != nil {
			t.Fatalf("CheckControl at exactly powerAvailable = %v, want nil", err)
		}
	})
	t.Run("a cancelled execution does not count", func(t *testing.T) {
		t.Parallel()
		other := baseExecution()
		other.MRID = "ctrl-other"
		other.TargetW = &sep2.ActivePower{Value: -4000}
		other.Cancelled = true
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{controls: []Control{other}})
		if err := checkControl(t, l, execProposal()); err != nil {
			t.Fatalf("CheckControl = %v, want nil", err)
		}
	})
	t.Run("a superseded execution stops counting at the proposal's start", func(t *testing.T) {
		t.Parallel()
		// other runs [1000, 2800) at 4000 W; the proposal starts at 1800 at
		// 4000 W. Unclipped they stack to 8000 W at 1800; clipped at 1800
		// they never share an instant.
		other := baseExecution()
		other.MRID = "ctrl-old"
		other.TargetW = &sep2.ActivePower{Value: -4000}
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{controls: []Control{other}})
		p := execProposal()
		p.Window = Window{Start: 1800, Duration: 600}
		p.TargetW = &sep2.ActivePower{Value: -4000}
		wantConflict(t, checkControl(t, l, p), ConflictPower, "ctrl-old")

		p.Supersedes = []string{"ctrl-old"}
		if err := checkControl(t, l, p); err != nil {
			t.Fatalf("CheckControl with the old execution superseded = %v, want nil", err)
		}
	})
	t.Run("a proposal with no reach is refused, not counted as zero", func(t *testing.T) {
		t.Parallel()
		l := NewLedger(&fakeGrants{grants: []Grant{g}}, &fakeControls{})
		p := execProposal()
		p.Reach = 0
		err := checkControl(t, l, p)
		var ce *ConflictError
		if err == nil || errors.As(err, &ce) {
			t.Fatalf("CheckControl with Reach 0 = %v, want a non-conflict error", err)
		}
	})
}

// TestWithin_StoreErrorRefuses pins design 5.5: a read that fails inside
// Within is an error, never a conflict and never "free", and the write the
// caller would make after a clean check is not reached.
func TestWithin_StoreErrorRefuses(t *testing.T) {
	t.Parallel()
	w := &Window{Start: 0, Duration: 10}
	tests := []struct {
		name  string
		l     *Ledger
		check func(View) error
	}{
		{
			name:  "CheckGrant, grant read fails",
			l:     NewLedger(&fakeGrants{err: errStoreDown}, &fakeControls{}),
			check: func(v View) error { return v.CheckGrant(context.Background(), "FLEET1", w, "") },
		},
		{
			name:  "CheckGrant, control read fails",
			l:     NewLedger(&fakeGrants{}, &fakeControls{err: errStoreDown}),
			check: func(v View) error { return v.CheckGrant(context.Background(), "FLEET1", w, "") },
		},
		{
			name: "CheckControl plain, grant read fails",
			l:    NewLedger(&fakeGrants{err: errStoreDown}, &fakeControls{}),
			check: func(v View) error {
				return v.CheckControl(context.Background(), Proposal{FleetKey: "FLEET1", Window: *w, Reach: 1})
			},
		},
		{
			name: "CheckControl execution, grant read fails",
			l:    NewLedger(&fakeGrants{err: errStoreDown}, &fakeControls{}),
			check: func(v View) error {
				p := Proposal{FleetKey: "FLEET1", Window: Window{Start: 1000, Duration: 10}, GrantMRID: "grant-1", TargetW: &sep2.ActivePower{Value: -1}, Reach: 1}
				return v.CheckControl(context.Background(), p)
			},
		},
		{
			name: "CheckControl execution, execution read fails",
			l:    NewLedger(&fakeGrants{grants: []Grant{baseGrant()}}, &fakeControls{err: errStoreDown}),
			check: func(v View) error {
				p := Proposal{FleetKey: "FLEET1", Window: Window{Start: 1000, Duration: 10}, GrantMRID: "grant-1", TargetW: &sep2.ActivePower{Value: -1}, Reach: 1}
				return v.CheckControl(context.Background(), p)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrote := false
			err := tc.l.Within(context.Background(), []string{"FLEET1"}, func(v View) error {
				if err := tc.check(v); err != nil {
					return err
				}
				wrote = true
				return nil
			})
			if !errors.Is(err, errStoreDown) {
				t.Fatalf("Within = %v, want an error wrapping the store failure", err)
			}
			var ce *ConflictError
			if errors.As(err, &ce) {
				t.Fatalf("Within = %v, a store failure must not read as a conflict", err)
			}
			if wrote {
				t.Fatal("the write ran after a failed check")
			}
		})
	}
}

func TestView_RefusesAFleetNotLocked(t *testing.T) {
	t.Parallel()
	l := NewLedger(&fakeGrants{}, &fakeControls{})
	err := l.Within(context.Background(), []string{"FLEET1"}, func(v View) error {
		return v.CheckGrant(context.Background(), "FLEET2", &Window{Duration: 10}, "")
	})
	if !errors.Is(err, ErrFleetNotLocked) {
		t.Fatalf("CheckGrant on an unlocked fleet = %v, want ErrFleetNotLocked", err)
	}
	err = l.Within(context.Background(), []string{"FLEET1"}, func(v View) error {
		return v.CheckControl(context.Background(), Proposal{FleetKey: "FLEET2", Window: Window{Duration: 10}, Reach: 1})
	})
	if !errors.Is(err, ErrFleetNotLocked) {
		t.Fatalf("CheckControl on an unlocked fleet = %v, want ErrFleetNotLocked", err)
	}
}

func TestWithin_RefusesEmptyKeys(t *testing.T) {
	t.Parallel()
	l := NewLedger(&fakeGrants{}, &fakeControls{})
	for _, keys := range [][]string{nil, {""}, {"FLEET1", ""}} {
		called := false
		err := l.Within(context.Background(), keys, func(View) error { called = true; return nil })
		if err == nil || called {
			t.Errorf("Within(%q) = %v, called=%v; want an error and fn not called", keys, err, called)
		}
	}
}

func TestWithin_ViewUsedAfterReturnPanics(t *testing.T) {
	t.Parallel()
	l := NewLedger(&fakeGrants{}, &fakeControls{})
	var leaked View
	if err := l.Within(context.Background(), []string{"FLEET1"}, func(v View) error { leaked = v; return nil }); err != nil {
		t.Fatalf("Within = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a View used after Within returned did not panic")
		}
	}()
	_ = leaked.CheckGrant(context.Background(), "FLEET1", &Window{Duration: 10}, "")
}

// TestWithin_Serializes proves check-then-write on one fleet is atomic: many
// goroutines each check a fleet window and, when free, commit it. Exactly
// one may win. Keys are passed in both orders to catch a lock-order
// deadlock across two fleets.
func TestWithin_Serializes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	grants := &lockedGrants{mu: &mu}
	l := NewLedger(grants, &fakeControls{})
	w := Window{Start: 0, Duration: 10}

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Mixed key sets: locking only the first or only the last
			// sorted key leaves FLEET1 unlocked for some of them.
			keySets := [][]string{{"FLEET1"}, {"FLEET1", "FLEET2"}, {"FLEET2", "FLEET1"}, {"FLEET0", "FLEET1"}}
			keys := keySets[i%len(keySets)]
			err := l.Within(context.Background(), keys, func(v View) error {
				if err := v.CheckGrant(context.Background(), "FLEET1", &w, ""); err != nil {
					return err
				}
				// Widen the gap between check and write so an unlocked
				// ledger loses the race reliably.
				time.Sleep(time.Millisecond)
				grants.add(grantOn("g", w))
				wins.Add(1)
				return nil
			})
			var ce *ConflictError
			if err != nil && !errors.As(err, &ce) {
				t.Errorf("Within = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("%d goroutines committed the same fleet window, want 1", got)
	}
}

// lockedGrants is fakeGrants safe for concurrent use, so the serialization
// test measures the ledger's lock rather than a data race in the fake.
type lockedGrants struct {
	mu     *sync.Mutex
	grants []Grant
}

func (f *lockedGrants) add(g Grant) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, g)
}

func (f *lockedGrants) GrantsInFleet(ctx context.Context, fleetKey string) ([]Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return (&fakeGrants{grants: append([]Grant(nil), f.grants...)}).GrantsInFleet(ctx, fleetKey)
}

func (f *lockedGrants) Grant(ctx context.Context, mrid string) (Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return (&fakeGrants{grants: append([]Grant(nil), f.grants...)}).Grant(ctx, mrid)
}

// Only ErrNoGrant is an absent grant. A store.ErrNotFound from anywhere else
// (a device the resolver cannot find) is an internal error, not a 409.
func TestCheckControl_OnlyErrNoGrantIsNotLive(t *testing.T) {
	t.Parallel()
	p := Proposal{FleetKey: "FLEET1", Window: Window{Start: 1000, Duration: 10}, GrantMRID: "grant-1", TargetW: &sep2.ActivePower{Value: -1}, Reach: 1}
	resolveFailed := fmt.Errorf("resolving fleet of EndDevice gone: %w", store.ErrNotFound)
	err := checkControl(t, NewLedger(&fakeGrants{err: resolveFailed}, &fakeControls{}), p)
	var ce *ConflictError
	if err == nil || errors.As(err, &ce) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CheckControl = %v, want the resolve error, not a conflict", err)
	}
}

func TestWithin_NilLedgerRefuses(t *testing.T) {
	t.Parallel()
	var l *Ledger
	called := false
	err := l.Within(context.Background(), []string{"FLEET1"}, func(View) error { called = true; return nil })
	if !errors.Is(err, ErrNoLedger) || called {
		t.Fatalf("nil Ledger Within = %v, called=%v; want ErrNoLedger and fn not called", err, called)
	}
}

func TestWithin_CancelledContextRefuses(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := NewLedger(&fakeGrants{}, &fakeControls{}).Within(ctx, []string{"FLEET1"}, func(View) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Within on a cancelled ctx = %v, called=%v; want context.Canceled and fn not called", err, called)
	}
}
