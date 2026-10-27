package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A later item whose copy fails and whose record cannot be removed still
// leaves no earlier item on any device.
func TestIssueAcrossAll_RecordUndoFailureStillUndoesEarlierItems(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, &failOnParent{ScopedStore: inner, parent: "d3/fsa/tmpl"}, "d1", "d2", "d3")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	stuck := func(_ context.Context, _ string, _ sep2.DERControl) (func(context.Context) error, error) {
		return func(context.Context) error { return errors.New("record store down") }, nil
	}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:2], Record: recs.write, StartOffset: 600},
		{Req: maxLimReq(4000), Targets: targets[2:], Record: stuck, StartOffset: 1200},
	}, nil)
	if err == nil {
		t.Fatal("IssueAcrossAll succeeded with the third item's copy failing")
	}
	for _, key := range []string{"d1/fsa/tmpl", "d2/fsa/tmpl", "d3/fsa/tmpl"} {
		if got := scopeControls(t, inner, key); len(got) != 0 {
			t.Errorf("%s still holds %d controls after the failed call", key, len(got))
		}
	}
	if len(recs.ids) != 0 {
		t.Errorf("records of the undone items left: %d", len(recs.ids))
	}
	var kept *AcrossUndoError
	if !errors.As(err, &kept) || len(kept.Kept) != 1 || kept.Kept[0].Item != 2 {
		t.Fatalf("err = %v, want an *AcrossUndoError naming item 2 alone", err)
	}
	var undo *UndoError
	if !errors.As(kept.Kept[0].Err, &undo) || undo.Step != UndoStepStoreRecord || undo.ControlKept || undo.MRID == "" {
		t.Errorf("item 2 kept = %v, want its record named, with no copy on a device", kept.Kept[0].Err)
	}
}

// A copy whose own undo fails is named as on a device, and every other item,
// earlier copies of the same item included, is removed.
func TestIssueAcrossAll_CopyUndoFailureNamesItAndUndoesTheRest(t *testing.T) {
	inner := memory.NewScopedStore[sep2.DERControl]()
	controls := &failCreateAndDelete{ScopedStore: inner, parent: "d3/fsa/tmpl"}
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2", "d3")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write, StartOffset: 600},
	}, nil)
	var kept *AcrossUndoError
	if !errors.As(err, &kept) || len(kept.Kept) != 1 || kept.Kept[0].Item != 1 {
		t.Fatalf("err = %v, want an *AcrossUndoError naming item 1", err)
	}
	var undo *UndoError
	if !errors.As(kept.Kept[0].Err, &undo) || !undo.ControlKept {
		t.Errorf("item 1 kept = %v, want a copy that may stay on a device", kept.Kept[0].Err)
	}
	for _, key := range []string{"d1/fsa/tmpl", "d2/fsa/tmpl"} {
		if got := scopeControls(t, inner, key); len(got) != 0 {
			t.Errorf("%s still holds %d controls", key, len(got))
		}
	}
	if len(recs.ids) != 1 {
		t.Errorf("records left = %d, want item 1's alone, kept while its copy may be live", len(recs.ids))
	}
}

// failCreateAndDelete stores the create under parent and then fails it, and
// fails the delete that would undo it, so the copy stays.
type failCreateAndDelete struct {
	*memory.ScopedStore[sep2.DERControl]
	parent string
}

func (f *failCreateAndDelete) Create(ctx context.Context, parentID, id string, c sep2.DERControl) error {
	if err := f.ScopedStore.Create(ctx, parentID, id, c); err != nil || parentID != f.parent {
		return err
	}
	return errors.New("disk full after write")
}

func (f *failCreateAndDelete) Delete(ctx context.Context, parentID, id string) error {
	if parentID == f.parent {
		return errors.New("disk full")
	}
	return f.ScopedStore.Delete(ctx, parentID, id)
}

// A failing check does not hide a commitment refusal in the same call.
func TestIssueAcrossAll_CheckAndCommitmentRefusalsBothNamed(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2")
	conflict := errors.New("grant overlaps")
	targets[1].Fleet.Check = func(context.Context, Proposal) error { return conflict }
	stop := errors.New("overlaps a live block")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write},
	}, func([]sep2.DERControl) error { return stop })
	if !errors.Is(err, stop) || !errors.Is(err, conflict) {
		t.Fatalf("err = %v, want both the check's error and item 1's commitment refusal", err)
	}
	if parents, _ := controls.Parents(context.Background()); len(parents) != 0 || len(recs.ids) != 0 {
		t.Errorf("writes after refusals: scopes %v, records %d", parents, len(recs.ids))
	}
}

// An item refused while its interval is resolved does not stop the check
// from seeing the others.
func TestIssueAcrossAll_ResolveRefusalStillRunsTheCheck(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1", "d2")
	stop := errors.New("overlaps a live block")
	var seen []sep2.DERControl
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: targets[:1], Record: recs.write, StartOffset: 8 * 24 * 3600},
		{Req: maxLimReq(4000), Targets: targets[1:], Record: recs.write},
	}, func(cs []sep2.DERControl) error { seen = cs; return stop })
	var refusal *RefusalError
	if !errors.Is(err, stop) || !errors.As(err, &refusal) || refusal.Code != RefusalStartTooFarAhead {
		t.Fatalf("err = %v, want the check's error and item 0's start_too_far_ahead", err)
	}
	if len(seen) != 2 || seen[0].Interval != nil || seen[1].Interval == nil {
		t.Errorf("check saw %+v, want item 0 unresolved and item 1 resolved", seen)
	}
}

// Every item whose scope has no program is named, not only the first.
func TestIssueAcrossAll_EveryMissingProgramNamed(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, _, targets := acrossHarness(t, controls, "d1")
	gone := func(dev string) Target {
		return Target{Scope: Scope{EndDeviceID: dev, FSAID: "fsa", DERProgramID: "tmpl"}, Fleet: passFleet()}
	}
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	_, err := issuer.IssueAcrossAll(context.Background(), []Across{
		{Req: maxLimReq(4000), Targets: []Target{gone("x1")}, Record: recs.write},
		{Req: maxLimReq(4000), Targets: targets, Record: recs.write, StartOffset: 600},
		{Req: maxLimReq(4000), Targets: []Target{gone("x2")}, Record: recs.write, StartOffset: 1200},
	}, nil)
	var items AcrossItemErrors
	if !errors.As(err, &items) || len(items) != 2 || items[0].Item != 0 || items[1].Item != 2 {
		t.Fatalf("err = %v, want items 0 and 2 named", err)
	}
	for _, it := range items {
		var refusal *RefusalError
		if !errors.As(it.Err, &refusal) || refusal.Code != RefusalProgramNotFound {
			t.Errorf("item %d = %v, want program_not_found", it.Item, it.Err)
		}
	}
}

// SoleItemError unwraps a one-item refusal, and leaves any other error.
func TestSoleItemError(t *testing.T) {
	one := errors.New("one")
	if got := SoleItemError(AcrossItemErrors{{Item: 0, Err: one}}); got != one {
		t.Errorf("one item = %v, want its error", got)
	}
	two := AcrossItemErrors{{Item: 0, Err: one}, {Item: 1, Err: one}}
	if got := SoleItemError(two); !errors.As(got, new(AcrossItemErrors)) {
		t.Errorf("two items = %v, want them kept", got)
	}
	plain := errors.New("plain")
	if got := SoleItemError(plain); got != plain {
		t.Errorf("plain = %v, want it back", got)
	}
}
