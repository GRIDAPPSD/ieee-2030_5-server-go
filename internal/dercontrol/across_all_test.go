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

// #1156: several controls, each across its own targets, in one call.

func scopeControls(t *testing.T, controls *memory.ScopedStore[sep2.DERControl], key string) []sep2.DERControl {
	t.Helper()
	list, err := controls.List(context.Background(), key, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// Items get consecutive creationTimes in order, and an item without a start
// starts its offset after the first creationTime, so offsets chain exactly.
func TestIssueAcrossAll_OrderAndChainedStarts(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, controls, "d1", "d2", "d3")
	ctx := context.Background()
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	derms := maxLimReq(4000)
	derms.Origin = OriginDERMS

	items := []Across{
		{Req: derms, Targets: targets[:2], Record: recs.write},
		{Req: derms, Targets: targets[2:], Record: recs.write},
		{Req: derms, Targets: targets[:2], Record: recs.write, StartOffset: 600},
	}
	before := sep2time.Now().Unix()
	res, err := issuer.IssueAcrossAll(ctx, items, nil)
	if err != nil {
		t.Fatalf("IssueAcrossAll: %v", err)
	}
	if len(res) != 3 || len(recs.ids) != 3 {
		t.Fatalf("results %d records %d, want 3 and 3", len(res), len(recs.ids))
	}
	first := res[0].Control.CreationTime
	if first < before {
		t.Fatalf("first creationTime %d before the call at %d", first, before)
	}
	for n, r := range res {
		if r.Control.CreationTime != first+int64(n) {
			t.Errorf("item %d creationTime = %d, want %d", n, r.Control.CreationTime, first+int64(n))
		}
	}
	if s := res[0].Control.Interval.Start; s != first {
		t.Errorf("item 0 start = %d, want the first creationTime %d", s, first)
	}
	if s := res[1].Control.Interval.Start; s != first {
		t.Errorf("item 1 start = %d, want %d: no offset starts at the first creationTime, not its own", s, first)
	}
	if s, end := res[2].Control.Interval.Start, IntervalEnd(res[0].Control.Interval); s != end {
		t.Errorf("item 2 start = %d, want item 0's end %d", s, end)
	}
	for n, wantCopies := range []int{2, 1, 2} {
		if len(res[n].Copies) != wantCopies {
			t.Errorf("item %d copies = %d, want %d", n, len(res[n].Copies), wantCopies)
		}
		for _, c := range res[n].Copies {
			lc, err := lifecycles.Get(ctx, c.Scope.Key(), res[n].ID)
			if err != nil || lc.Origin != OriginDERMS {
				t.Errorf("item %d copy in %s lifecycle = %+v, %v; want origin derms", n, c.Scope.Key(), lc, err)
			}
		}
	}
	if got := len(scopeControls(t, controls, "d1/fsa/tmpl")); got != 2 {
		t.Errorf("d1 holds %d controls, want items 0 and 2", got)
	}
}

// A copy that fails in the second item undoes the first item whole: its
// copies, its lifecycle records and its record.
func TestIssueAcrossAll_LaterItemFailureUndoesEarlierItems(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, &failOnParent{ScopedStore: inner, parent: "d3/fsa/tmpl"}, "d1", "d2", "d3")
	ctx := context.Background()
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(ctx, []Across{
		{Req: maxLimReq(4000), Targets: targets[:2], Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[2:], Record: recs.write},
	}, nil)
	if err == nil {
		t.Fatal("IssueAcrossAll succeeded with the second item's copy failing")
	}
	var undo *UndoError
	if errors.As(err, &undo) {
		t.Fatalf("err = %v, want a plain error: every write is undoable", err)
	}
	if len(recs.ids) != 0 {
		t.Errorf("records left = %d, want none", len(recs.ids))
	}
	for _, d := range []string{"d1", "d2", "d3"} {
		key := d + "/fsa/tmpl"
		if got := scopeControls(t, inner, key); len(got) != 0 {
			t.Errorf("%s holds %d controls after the failed call", key, len(got))
		}
		lcs, _ := lifecycles.List(ctx, key, store.ListOptions{Unbounded: true})
		if len(lcs.Items) != 0 {
			t.Errorf("%s holds %d lifecycle records after the failed call", key, len(lcs.Items))
		}
	}
}

// A record that cannot be removed in the undo of an earlier item is named,
// and the item it belongs to is kept, so what remains is a prefix.
func TestIssueAcrossAll_UndoFailureKeepsAPrefix(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, &failOnParent{ScopedStore: inner, parent: "d2/fsa/tmpl"}, "d1", "d2")
	stuck := func(_ context.Context, _ string, _ sep2.DERControl) (func(context.Context) error, error) {
		return func(context.Context) error { return errors.New("record store down") }, nil
	}
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: stuck},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write},
	}, nil)
	var undo *UndoError
	if !errors.As(err, &undo) || undo.Step != UndoStepStoreRecord {
		t.Fatalf("err = %v, want a *UndoError at the record step", err)
	}
	if len(recs.ids) != 0 {
		t.Errorf("the failed item's record is kept: %d", len(recs.ids))
	}
	if got := scopeControls(t, inner, "d1/fsa/tmpl"); len(got) != 0 {
		t.Errorf("the first item's copy is kept: %d controls in d1; its copies are removed before its record", len(got))
	}
}

// Every item that cannot be planned is named, and nothing is written.
func TestIssueAcrossAll_NamesEveryRefusedItem(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2", "d3")
	conflict := errors.New("grant overlaps")
	refusing := targets[2]
	refusing.Fleet.Check = func(context.Context, Proposal) error { return conflict }
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	bad := maxLimReq(4000)
	bad.DurationSeconds = 1
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write},
		{Req: bad, Targets: targets[1:2], Record: recs.write},
		{Req: maxLimReq(4000), Targets: []Target{refusing}, Record: recs.write},
	}, nil)
	items, ok := err.(AcrossItemErrors)
	if !ok || len(items) != 2 {
		t.Fatalf("err = %v, want AcrossItemErrors naming items 1 and 2", err)
	}
	var refusal *RefusalError
	if items[0].Item != 1 || !errors.As(items[0].Err, &refusal) || refusal.Code != RefusalDurationOutOfRange {
		t.Errorf("first refused = %+v, want item 1 duration_out_of_range", items[0])
	}
	if items[1].Item != 2 || !errors.Is(items[1].Err, conflict) {
		t.Errorf("second refused = %+v, want item 2 with the check's error", items[1])
	}
	if !errors.Is(err, conflict) {
		t.Error("errors.Is does not reach an item's error through AcrossItemErrors")
	}
	if parents, _ := controls.Parents(context.Background()); len(parents) != 0 || len(recs.ids) != 0 {
		t.Errorf("writes after refused items: scopes %v, records %d", parents, len(recs.ids))
	}
}

// Two items that would overlap in a scope they share are refused: neither
// supersedes the other, so both would run.
func TestIssueAcrossAll_OverlapInASharedScopeRefused(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets, Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write, StartOffset: 599},
	}, nil)
	if !errors.Is(err, ErrAcrossOverlap) {
		t.Fatalf("err = %v, want ErrAcrossOverlap", err)
	}
	// Back to back is not overlap.
	res, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets, Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write, StartOffset: 600},
	}, nil)
	if err != nil || len(res) != 2 {
		t.Fatalf("back to back = %d results, %v; want both written", len(res), err)
	}
}

// The check sees every control with its resolved interval, and its error
// writes nothing.
func TestIssueAcrossAll_CheckSeesResolvedControls(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	stop := errors.New("overlaps a live block")
	var seen []sep2.DERControl
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write, StartOffset: 600},
	}, func(cs []sep2.DERControl) error { seen = cs; return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the check's error", err)
	}
	if len(seen) != 2 || seen[1].Interval.Start != seen[0].Interval.Start+600 || seen[0].MRID == "" {
		t.Errorf("check saw %+v, want two controls with mRIDs, the second 600 s later", seen)
	}
	if parents, _ := controls.Parents(context.Background()); len(parents) != 0 || len(recs.ids) != 0 {
		t.Errorf("writes after a refused check: scopes %v, records %d", parents, len(recs.ids))
	}
}
