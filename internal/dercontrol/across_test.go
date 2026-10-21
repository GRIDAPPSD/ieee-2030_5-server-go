package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func passFleet() Fleet {
	return Fleet{Key: "fleet", Reach: 1, Check: func(context.Context, Proposal) error { return nil }}
}

// acrossHarness seeds one template copy program per device, as an FSA
// template assigned to each would.
func acrossHarness(t *testing.T, controls controlStore, devices ...string) (*Issuer, *memory.ScopedStore[sep2.DERProgram], *memory.ScopedStore[LifecycleRecord], []Target) {
	t.Helper()
	programs := memory.NewScopedStore[sep2.DERProgram]()
	lifecycles := memory.NewScopedStore[LifecycleRecord]()
	issuer, err := NewIssuer(programs, controls, lifecycles, Config{PEN: testPEN(12345)})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	var targets []Target
	for _, d := range devices {
		p := sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: controlListHref(d, "fsa", "tmpl")}}
		if err := programs.Create(context.Background(), d, "tmpl", p); err != nil {
			t.Fatalf("seed program: %v", err)
		}
		targets = append(targets, Target{Scope: Scope{EndDeviceID: d, FSAID: "fsa", DERProgramID: "tmpl"}, Fleet: passFleet()})
	}
	return issuer, programs, lifecycles, targets
}

// recordLog stands in for the FSA store: it holds the records IssueAcross
// wrote and lets a test see whether the undo removed them.
type recordLog struct {
	ids map[string]sep2.DERControl
}

func (l *recordLog) write(_ context.Context, id string, ctrl sep2.DERControl) (func(context.Context) error, error) {
	l.ids[id] = ctrl
	return func(context.Context) error { delete(l.ids, id); return nil }, nil
}

func maxLimReq(v uint16) CreateRequest {
	return CreateRequest{Type: MaxLimW, MaxLimW: uint16ptr(v), DurationSeconds: 600}
}

func TestIssueAcross_OneEventInEveryScope(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, controls, "d1", "d2", "d3")
	ctx := context.Background()

	// A newer event already in one scope pushes every copy's creationTime past it.
	future := sep2time.Now().Unix() + 1000
	seed := sep2.DERControl{}
	seed.MRID, seed.CreationTime, seed.Href = "SEED", future, controlListHref("d2", "fsa", "tmpl")+"/seed"
	if err := controls.Create(ctx, "d2/fsa/tmpl", "seed", seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	res, err := issuer.IssueAcross(ctx, maxLimReq(4000), targets, recs.write)
	if err != nil {
		t.Fatalf("IssueAcross: %v", err)
	}
	if len(recs.ids) != 1 || recs.ids[res.ID].MRID != res.Control.MRID || recs.ids[res.ID].Href != "" {
		t.Fatalf("records = %+v, want one record under %s with mRID %s and no href", recs.ids, res.ID, res.Control.MRID)
	}
	if res.Control.CreationTime != future+1 {
		t.Errorf("creationTime = %d, want %d: one more than the largest in any scope", res.Control.CreationTime, future+1)
	}
	if len(res.Copies) != 3 {
		t.Fatalf("copies = %d, want 3", len(res.Copies))
	}
	for _, d := range []string{"d1", "d2", "d3"} {
		key := d + "/fsa/tmpl"
		got, err := controls.Get(ctx, key, res.ID)
		if err != nil {
			t.Fatalf("copy in %s: %v", key, err)
		}
		if got.MRID != res.Control.MRID || got.CreationTime != future+1 || got.Href != controlListHref(d, "fsa", "tmpl")+"/"+res.ID {
			t.Errorf("copy in %s = mRID %s creationTime %d href %s", key, got.MRID, got.CreationTime, got.Href)
		}
		if got.DERControlBase == nil || got.DERControlBase.OpModMaxLimW == nil || *got.DERControlBase.OpModMaxLimW != 4000 {
			t.Errorf("copy in %s base = %+v, want opModMaxLimW 4000", key, got.DERControlBase)
		}
		lc, err := lifecycles.Get(ctx, key, res.ID)
		if err != nil || lc.FleetKey != "fleet" || lc.Reach != 1 {
			t.Errorf("lifecycle in %s = %+v, %v; want fleet key and reach", key, lc, err)
		}
	}
}

// failOnParent fails the first Create under one parent, after the write
// would have reached the store, so the test sees no partial copy survive.
type failOnParent struct {
	*memory.ScopedStore[sep2.DERControl]
	parent string
}

func (f *failOnParent) Create(ctx context.Context, parentID, id string, c sep2.DERControl) error {
	if parentID == f.parent {
		return errors.New("disk full")
	}
	return f.ScopedStore.Create(ctx, parentID, id, c)
}

func TestIssueAcross_ThirdCopyFailsLeavesNothing(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, &failOnParent{ScopedStore: inner, parent: "d3/fsa/tmpl"}, "d1", "d2", "d3")
	ctx := context.Background()

	// An older equal control in d1 is marked superseded by the first copy, so
	// the undo must also put its record back.
	older := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModMaxLimW: (*sep2.PerCent)(uint16ptr(1))}}
	now := sep2time.Now().Unix()
	older.MRID, older.CreationTime, older.Href = "OLD", now-5, controlListHref("d1", "fsa", "tmpl")+"/old"
	older.Interval = &sep2.DateTimeInterval{Start: now, Duration: 3600}
	if err := inner.Create(ctx, "d1/fsa/tmpl", "old", older); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := lifecycles.Create(ctx, "d1/fsa/tmpl", "old", LifecycleRecord{}); err != nil {
		t.Fatalf("seed lifecycle: %v", err)
	}

	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcross(ctx, maxLimReq(4000), targets, recs.write)
	if err == nil {
		t.Fatal("IssueAcross succeeded with the third copy failing")
	}
	var undo *UndoError
	if errors.As(err, &undo) {
		t.Fatalf("err = %v, want a plain error: every write is undoable", err)
	}
	if len(recs.ids) != 0 {
		t.Errorf("records left = %+v, want none", recs.ids)
	}
	for _, d := range []string{"d1", "d2", "d3"} {
		key := d + "/fsa/tmpl"
		list, _ := inner.List(ctx, key, store.ListOptions{Unbounded: true})
		for _, c := range list.Items {
			if c.MRID != "OLD" {
				t.Errorf("scope %s holds copy %s after the failed fan-out", key, c.MRID)
			}
		}
		lcs, _ := lifecycles.List(ctx, key, store.ListOptions{Unbounded: true})
		if want := map[string]int{"d1": 1}[d]; len(lcs.Items) != want {
			t.Errorf("scope %s holds %d lifecycle records, want %d", key, len(lcs.Items), want)
		}
	}
	lc, err := lifecycles.Get(ctx, "d1/fsa/tmpl", "old")
	if err != nil || lc.SupersededAt != nil || lc.SupersededBy != "" {
		t.Errorf("older control's record = %+v, %v; want its supersede mark undone", lc, err)
	}
}

func TestIssueAcross_RefusedCheckWritesNothing(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2")
	conflict := errors.New("grant overlaps")
	targets[1].Fleet.Check = func(context.Context, Proposal) error { return conflict }
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	if _, err := issuer.IssueAcross(context.Background(), maxLimReq(1), targets, recs.write); !errors.Is(err, conflict) {
		t.Fatalf("err = %v, want the check's error", err)
	}
	if parents, _ := controls.Parents(context.Background()); len(parents) != 0 || len(recs.ids) != 0 {
		t.Errorf("writes after a refused check: scopes %v, records %v", parents, recs.ids)
	}
}

func TestReplicate_WritesOnceAndOrdersSupersede(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, controls, "d1", "d4")
	ctx := context.Background()
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	first, err := issuer.IssueAcross(ctx, maxLimReq(4000), targets[:1], recs.write)
	if err != nil {
		t.Fatalf("IssueAcross: %v", err)
	}

	// d4 already holds a newer equal control issued straight to its copy; a
	// control of the replica's creationTime, with no lifecycle record, makes
	// the direct issue newer without waiting for the clock.
	pad := sep2.DERControl{}
	pad.MRID, pad.CreationTime, pad.Href = "PAD", first.Control.CreationTime, controlListHref("d4", "fsa", "tmpl")+"/pad"
	if err := controls.Create(ctx, "d4/fsa/tmpl", "pad", pad); err != nil {
		t.Fatalf("seed: %v", err)
	}
	direct, err := issuer.Issue(ctx, CreateRequest{DERProgramHref: programHref("d4", "fsa", "tmpl"), Type: MaxLimW, MaxLimW: uint16ptr(2000), DurationSeconds: 600})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rep := Replica{Target: targets[1], ID: first.ID, Control: first.Control}
	out, err := issuer.Replicate(ctx, []Replica{rep})
	if err != nil || len(out) != 1 {
		t.Fatalf("Replicate = %+v, %v; want one written", out, err)
	}
	got, err := controls.Get(ctx, "d4/fsa/tmpl", first.ID)
	if err != nil || got.MRID != first.Control.MRID || got.CreationTime != first.Control.CreationTime {
		t.Fatalf("replica = %+v, %v; want the record's mRID and creationTime", got, err)
	}
	lc, err := lifecycles.Get(ctx, "d4/fsa/tmpl", first.ID)
	if err != nil || lc.SupersededBy != direct.Control.MRID || lc.SupersededAt == nil || *lc.SupersededAt != direct.Control.Interval.Start {
		t.Errorf("replica lifecycle = %+v, %v; want superseded by the newer direct control %s", lc, err, direct.Control.MRID)
	}
	dlc, _ := lifecycles.Get(ctx, "d4/fsa/tmpl", direct.ID)
	if dlc.SupersededAt != nil {
		t.Errorf("the newer direct control was marked superseded by an older replica: %+v", dlc)
	}

	again, err := issuer.Replicate(ctx, []Replica{rep})
	if err != nil || len(again) != 0 {
		t.Errorf("second Replicate = %+v, %v; want nothing written", again, err)
	}
}

// A replica that fails undoes the ones the same call wrote before it, so an
// assignment rolled back leaves no control on the device.
func TestReplicate_LaterFailureUndoesEarlierReplicas(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, &failOnParent{ScopedStore: inner, parent: "d2/fsa/tmpl"}, "d1", "d2")
	ctx := context.Background()
	start := sep2time.Now().Unix() + 600
	mk := func(mrid string) sep2.DERControl {
		c := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModMaxLimW: (*sep2.PerCent)(uint16ptr(1000))}}
		c.MRID, c.CreationTime = mrid, start-60
		c.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}
		return c
	}
	_, err := issuer.Replicate(ctx, []Replica{
		{Target: targets[0], ID: "r1", Control: mk("R1")},
		{Target: targets[1], ID: "r2", Control: mk("R2")},
	})
	if err == nil {
		t.Fatal("Replicate succeeded with the second replica failing")
	}
	for _, key := range []string{"d1/fsa/tmpl", "d2/fsa/tmpl"} {
		if list, _ := inner.List(ctx, key, store.ListOptions{Unbounded: true}); len(list.Items) != 0 {
			t.Errorf("scope %s holds %d controls after the failed replication, want 0", key, len(list.Items))
		}
		if lcs, _ := lifecycles.List(ctx, key, store.ListOptions{Unbounded: true}); len(lcs.Items) != 0 {
			t.Errorf("scope %s holds %d lifecycle records, want 0", key, len(lcs.Items))
		}
	}
}
