package sources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

var (
	aggLFDI        = strings.Repeat("A", 40)
	managedLFDI    = strings.Repeat("B", 40)
	standaloneLFDI = strings.Repeat("C", 40)
)

const (
	aggID        = "agg"
	managedID    = "m1"
	standaloneID = "s1"
	managedScope = "m1/fsa1/derp1"
	standScope   = "s1/fsa1/derp1"
)

type fixture struct {
	devices            *memory.EndDeviceStore
	managers           *memory.EndDeviceManagementStore
	responses          *memory.ScopedStore[sep2.FlowReservationResponse]
	responseLifecycles *memory.ScopedStore[dercontrol.LifecycleRecord]
	controls           *memory.ScopedStore[sep2.DERControl]
	controlLifecycles  *memory.ScopedStore[dercontrol.LifecycleRecord]
}

func (f *fixture) resolver() commitment.Resolver {
	return commitment.Resolver{Devices: f.devices, Managers: f.managers}
}

func (f *fixture) grants() *sources.Grants {
	return sources.NewGrants(f.responses, f.responseLifecycles, f.resolver())
}

func (f *fixture) controlSource() *sources.Controls {
	return sources.NewControls(f.controls, f.controlLifecycles, f.resolver())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addResponse(t *testing.T, id string, interval *sep2.DateTimeInterval, cancelledAt *int64) {
	t.Helper()
	frp := sep2.FlowReservationResponse{
		EnergyAvailable: &sep2.SignedRealEnergy{Value: 10000},
		PowerAvailable:  &sep2.ActivePower{Value: 4000},
	}
	frp.Href = "/edev/" + aggID + "/frp/" + id
	frp.MRID = "MRID-" + id
	frp.Interval = interval
	must(t, f.responses.Create(context.Background(), aggID, id, frp))
	if cancelledAt != nil {
		must(t, f.responseLifecycles.Create(context.Background(), aggID, id, dercontrol.LifecycleRecord{CancelledAt: cancelledAt}))
	}
}

// addControl stores a control; lc nil stores no lifecycle record, which is
// what a boot fixture or the CSIP loader leaves.
func (f *fixture) addControl(t *testing.T, scope, id string, start int64, dur uint32, lc *dercontrol.LifecycleRecord) {
	t.Helper()
	ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: -500}}}
	parts := strings.Split(scope, "/")
	ctrl.Href = "/edev/" + parts[0] + "/fsa/" + parts[1] + "/derp/" + parts[2] + "/derc/" + id
	ctrl.MRID = "MRID-" + id
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
	must(t, f.controls.Create(context.Background(), scope, id, ctrl))
	if lc != nil {
		must(t, f.controlLifecycles.Create(context.Background(), scope, id, *lc))
	}
}

// newFixture: an aggregator managing one device, and a standalone device.
// Responses under the aggregator: R1 a live grant on [1000, 1600), R2 a
// denial, R3 a grant with no interval, R4 a cancelled grant on [2000, 2600).
// Controls on the managed device: C1 live, F1 a fixture with no record,
// C2 superseded at 1300, C3 cancelled. C4 on the standalone device.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{
		devices:            memory.NewEndDeviceStore(),
		managers:           memory.NewEndDeviceManagementStore(),
		responses:          memory.NewScopedStore[sep2.FlowReservationResponse](),
		responseLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
		controls:           memory.NewScopedStore[sep2.DERControl](),
		controlLifecycles:  memory.NewScopedStore[dercontrol.LifecycleRecord](),
	}
	must(t, f.devices.Create(ctx, aggID, sep2.EndDevice{LFDI: aggLFDI}))
	must(t, f.devices.Create(ctx, managedID, sep2.EndDevice{LFDI: managedLFDI}))
	must(t, f.devices.Create(ctx, standaloneID, sep2.EndDevice{LFDI: standaloneLFDI}))
	must(t, f.managers.Assign(ctx, aggLFDI, managedLFDI))

	cancelled := int64(1)
	f.addResponse(t, "R1", &sep2.DateTimeInterval{Start: 1000, Duration: 600}, nil)
	f.addResponse(t, "R2", &sep2.DateTimeInterval{Start: 1000, Duration: 0}, nil)
	f.addResponse(t, "R3", nil, nil)
	f.addResponse(t, "R4", &sep2.DateTimeInterval{Start: 2000, Duration: 600}, &cancelled)

	supersededAt := int64(1300)
	f.addControl(t, managedScope, "C1", 5000, 600, &dercontrol.LifecycleRecord{})
	f.addControl(t, managedScope, "F1", 7000, 600, nil)
	f.addControl(t, managedScope, "C2", 1000, 600, &dercontrol.LifecycleRecord{SupersededAt: &supersededAt, SupersededBy: "MRID-X"})
	f.addControl(t, managedScope, "C3", 9000, 600, &dercontrol.LifecycleRecord{CancelledAt: &cancelled})
	f.addControl(t, standScope, "C4", 5000, 600, &dercontrol.LifecycleRecord{})
	return f
}

func byMRID[T any](items []T, mrid func(T) string) map[string]T {
	out := make(map[string]T, len(items))
	for _, it := range items {
		out[mrid(it)] = it
	}
	return out
}

func TestGrants_GrantsInFleet(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	got, err := f.grants().GrantsInFleet(context.Background(), aggLFDI)
	must(t, err)
	m := byMRID(got, func(g commitment.Grant) string { return g.MRID })
	if len(m) != 2 {
		t.Fatalf("GrantsInFleet returned %d grants %v, want R1 and R4 only (no denial, no interval-less grant)", len(m), m)
	}
	r1, ok := m["MRID-R1"]
	if !ok {
		t.Fatal("R1 missing")
	}
	if r1.EndDeviceID != aggID || r1.FleetKey != aggLFDI || r1.Window == nil || *r1.Window != (commitment.Window{Start: 1000, Duration: 600}) {
		t.Errorf("R1 = %+v, want stored under %s in fleet %s on [1000, 1600)", r1, aggID, aggLFDI)
	}
	if r1.Energy == nil || r1.Energy.Value != 10000 || r1.Power == nil || r1.Power.Value != 4000 || r1.CancelledAt != nil {
		t.Errorf("R1 energy/power/cancel = %v/%v/%v, want 10000/4000/nil", r1.Energy, r1.Power, r1.CancelledAt)
	}
	r4 := m["MRID-R4"]
	if r4.CancelledAt == nil || *r4.CancelledAt != 1 {
		t.Errorf("R4.CancelledAt = %v, want 1 from its lifecycle record", r4.CancelledAt)
	}

	other, err := f.grants().GrantsInFleet(context.Background(), standaloneLFDI)
	must(t, err)
	if len(other) != 0 {
		t.Errorf("GrantsInFleet(standalone) = %v, want none", other)
	}
}

func TestGrants_Grant(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, err := f.grants().Grant(context.Background(), "MRID-R3")
	must(t, err)
	if g.Window != nil || g.FleetKey != aggLFDI {
		t.Errorf("Grant(R3) = %+v, want no window in fleet %s", g, aggLFDI)
	}
	if _, err := f.grants().Grant(context.Background(), "MRID-none"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Grant(absent) = %v, want store.ErrNotFound", err)
	}
}

func TestControls_ControlsInFleet(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	got, err := f.controlSource().ControlsInFleet(context.Background(), aggLFDI)
	must(t, err)
	m := byMRID(got, func(c commitment.Control) string { return c.MRID })
	if _, ok := m["MRID-F1"]; ok {
		t.Error("the fixture control F1, which has no lifecycle record, was returned")
	}
	if len(m) != 3 {
		t.Fatalf("ControlsInFleet returned %v, want C1, C2 and C3", m)
	}
	c1 := m["MRID-C1"]
	if c1.Scope != managedScope || c1.FleetKey != aggLFDI || c1.Window != (commitment.Window{Start: 5000, Duration: 600}) ||
		c1.GrantMRID != "" || c1.Reach != 1 || c1.Cancelled || c1.TargetW == nil || c1.TargetW.Value != -500 {
		t.Errorf("C1 = %+v", c1)
	}
	if c2 := m["MRID-C2"]; c2.Window != (commitment.Window{Start: 1000, Duration: 300}) {
		t.Errorf("C2 window = %+v, want clipped at its supersede time to [1000, 1300)", c2.Window)
	}
	if c3 := m["MRID-C3"]; !c3.Cancelled {
		t.Error("C3 not marked cancelled")
	}

	stand, err := f.controlSource().ControlsInFleet(context.Background(), standaloneLFDI)
	must(t, err)
	if len(stand) != 1 || stand[0].MRID != "MRID-C4" {
		t.Errorf("ControlsInFleet(standalone) = %v, want C4 only", stand)
	}
}

func TestControls_ExecutionsOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	got, err := f.controlSource().ExecutionsOf(context.Background(), "MRID-R1")
	must(t, err)
	if len(got) != 0 {
		t.Errorf("ExecutionsOf(R1) = %v, want none: no record carries a link yet", got)
	}
	if _, err := f.controlSource().ExecutionsOf(context.Background(), ""); err == nil {
		t.Error("ExecutionsOf(\"\") = nil error, want a refusal: it would return every plain control")
	}
}

// The ledger over the real sources: the done-when cases of this slice.
func TestLedgerOverSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	check := func(t *testing.T, f *fixture, w commitment.Window) error {
		t.Helper()
		l := commitment.NewLedger(f.grants(), f.controlSource())
		return l.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
			return v.CheckGrant(ctx, aggLFDI, &w, "")
		})
	}
	wantConflict := func(t *testing.T, err error, mrid string) {
		t.Helper()
		var ce *commitment.ConflictError
		if !errors.As(err, &ce) || ce.Code != commitment.ConflictFleetWindow || ce.MRID != mrid {
			t.Fatalf("CheckGrant = %v, want fleet_window_committed naming %s", err, mrid)
		}
	}

	t.Run("an overlapping grant is named", func(t *testing.T) {
		t.Parallel()
		wantConflict(t, check(t, newFixture(t), commitment.Window{Start: 1500, Duration: 10}), "MRID-R1")
	})
	t.Run("an overlapping plain control is named", func(t *testing.T) {
		t.Parallel()
		wantConflict(t, check(t, newFixture(t), commitment.Window{Start: 5500, Duration: 10}), "MRID-C1")
	})
	t.Run("a cancelled grant is not counted", func(t *testing.T) {
		t.Parallel()
		must(t, check(t, newFixture(t), commitment.Window{Start: 2000, Duration: 600}))
	})
	t.Run("a cancelled control is not counted", func(t *testing.T) {
		t.Parallel()
		must(t, check(t, newFixture(t), commitment.Window{Start: 9000, Duration: 600}))
	})
	t.Run("a superseded control stops counting at its supersede time", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		must(t, f.responses.Delete(ctx, aggID, "R1"))
		must(t, check(t, f, commitment.Window{Start: 1300, Duration: 100}))
		wantConflict(t, check(t, f, commitment.Window{Start: 1299, Duration: 100}), "MRID-C2")
	})
	t.Run("a fixture control with no record is never counted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		must(t, check(t, f, commitment.Window{Start: 7000, Duration: 600}))
		// Control: the same window conflicts once the control has a record.
		must(t, f.controlLifecycles.Create(ctx, managedScope, "F1", dercontrol.LifecycleRecord{}))
		wantConflict(t, check(t, f, commitment.Window{Start: 7000, Duration: 600}), "MRID-F1")
	})
	t.Run("a control in another fleet is not counted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		l := commitment.NewLedger(f.grants(), f.controlSource())
		w := commitment.Window{Start: 5000, Duration: 600}
		err := l.Within(ctx, []string{standaloneLFDI}, func(v commitment.View) error {
			return v.CheckGrant(ctx, standaloneLFDI, &w, "")
		})
		wantConflict(t, err, "MRID-C4")
		wantConflict(t, check(t, f, w), "MRID-C1")
	})
}

var errDown = errors.New("fake: store unreachable")

type failingScoped[T store.Copier[T]] struct {
	store.ScopedStore[T]
	failParents, failList, failGet bool
}

func (s failingScoped[T]) Parents(ctx context.Context) ([]string, error) {
	if s.failParents {
		return nil, errDown
	}
	return s.ScopedStore.Parents(ctx)
}

func (s failingScoped[T]) List(ctx context.Context, p string, o store.ListOptions) (store.ListResult[T], error) {
	if s.failList {
		return store.ListResult[T]{}, errDown
	}
	return s.ScopedStore.List(ctx, p, o)
}

func (s failingScoped[T]) Get(ctx context.Context, p, id string) (T, error) {
	if s.failGet {
		var zero T
		return zero, errDown
	}
	return s.ScopedStore.Get(ctx, p, id)
}

// Every store failure a source can meet refuses the check inside Within;
// none reads as a free window.
func TestLedgerOverSources_StoreErrorRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type build func(f *fixture) (commitment.GrantSource, commitment.ControlSource)
	cases := map[string]build{
		"response parents": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return sources.NewGrants(failingScoped[sep2.FlowReservationResponse]{ScopedStore: f.responses, failParents: true}, f.responseLifecycles, f.resolver()), f.controlSource()
		},
		"response list": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return sources.NewGrants(failingScoped[sep2.FlowReservationResponse]{ScopedStore: f.responses, failList: true}, f.responseLifecycles, f.resolver()), f.controlSource()
		},
		"response lifecycle": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return sources.NewGrants(f.responses, failingScoped[dercontrol.LifecycleRecord]{ScopedStore: f.responseLifecycles, failGet: true}, f.resolver()), f.controlSource()
		},
		"control lifecycle parents": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return f.grants(), sources.NewControls(f.controls, failingScoped[dercontrol.LifecycleRecord]{ScopedStore: f.controlLifecycles, failParents: true}, f.resolver())
		},
		"control list": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return f.grants(), sources.NewControls(failingScoped[sep2.DERControl]{ScopedStore: f.controls, failList: true}, f.controlLifecycles, f.resolver())
		},
		"control lifecycle get": func(f *fixture) (commitment.GrantSource, commitment.ControlSource) {
			return f.grants(), sources.NewControls(f.controls, failingScoped[dercontrol.LifecycleRecord]{ScopedStore: f.controlLifecycles, failGet: true}, f.resolver())
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			g, c := cases[name](f)
			l := commitment.NewLedger(g, c)
			// A window nothing in the fixture overlaps, so only a failed read
			// can stop it.
			w := commitment.Window{Start: 100000, Duration: 10}
			wrote := false
			err := l.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
				if err := v.CheckGrant(ctx, aggLFDI, &w, ""); err != nil {
					return err
				}
				wrote = true
				return nil
			})
			if !errors.Is(err, errDown) || wrote {
				t.Fatalf("Within = %v, wrote=%v; want the store failure and no write", err, wrote)
			}
		})
	}
}

func TestSources_UnresolvableFleetRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	must(t, f.devices.Delete(ctx, managedID))
	if _, err := f.controlSource().ControlsInFleet(ctx, aggLFDI); err == nil {
		t.Error("ControlsInFleet with a control's EndDevice gone = nil error, want a refusal")
	}
	f = newFixture(t)
	must(t, f.devices.Delete(ctx, aggID))
	if _, err := f.grants().GrantsInFleet(ctx, aggLFDI); err == nil {
		t.Error("GrantsInFleet with a response's EndDevice gone = nil error, want a refusal")
	}
}
