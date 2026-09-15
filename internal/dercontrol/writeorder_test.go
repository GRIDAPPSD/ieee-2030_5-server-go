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

// Acceptance criteria 7 and 8 require that a partial write during Issue
// never leaves a lifecycle mark naming a control that was never stored, and
// never leaves a stored control without a lifecycle record. These tests
// inject a failure at each write Issue makes and assert the two stores
// directly afterward, not merely that Issue returned an error.

// failingControls wraps a real control store so a test can force Create or
// Delete to fail without a real backend.
type failingControls struct {
	*memory.ScopedStore[sep2.DERControl]
	failCreate error
	failDelete error
}

func (f *failingControls) Create(ctx context.Context, parentID, id string, resource sep2.DERControl) error {
	if f.failCreate != nil {
		return f.failCreate
	}
	return f.ScopedStore.Create(ctx, parentID, id, resource)
}

func (f *failingControls) Delete(ctx context.Context, parentID, id string) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	return f.ScopedStore.Delete(ctx, parentID, id)
}

// failingLifecycles wraps a real lifecycle store so a test can force Create
// to fail, or force specific Update calls (counted from 1) to fail, without
// a real backend.
type failingLifecycles struct {
	*memory.ScopedStore[LifecycleRecord]
	failCreate   error
	updateCalls  int
	updateFailAt map[int]error
}

func (f *failingLifecycles) Create(ctx context.Context, parentID, id string, resource LifecycleRecord) error {
	if f.failCreate != nil {
		return f.failCreate
	}
	return f.ScopedStore.Create(ctx, parentID, id, resource)
}

func (f *failingLifecycles) Update(ctx context.Context, parentID, id string, resource LifecycleRecord) error {
	f.updateCalls++
	if err, ok := f.updateFailAt[f.updateCalls]; ok {
		return err
	}
	return f.ScopedStore.Update(ctx, parentID, id, resource)
}

func newWriteOrderIssuer(t *testing.T, controls *failingControls, lifecycles *failingLifecycles) (*Issuer, *memory.ScopedStore[sep2.DERProgram]) {
	t.Helper()
	programs := memory.NewScopedStore[sep2.DERProgram]()
	issuer, err := NewIssuer(programs, controls, lifecycles, Config{PEN: testPEN(1)})
	if err != nil {
		t.Fatalf("NewIssuer() error = %v", err)
	}
	return issuer, programs
}

func seedWriteOrderProgram(t *testing.T, programs *memory.ScopedStore[sep2.DERProgram], edev, derp, linkHref string) {
	t.Helper()
	p := sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: linkHref}}
	if err := programs.Create(context.Background(), edev, derp, p); err != nil {
		t.Fatalf("seed program: %v", err)
	}
}

// assertScopedStoreEmpty fails the test unless every scope s knows holds
// zero records.
func assertScopedStoreEmpty[T store.Copier[T]](t *testing.T, s *memory.ScopedStore[T], label string) {
	t.Helper()
	ctx := context.Background()
	parents, err := s.Parents(ctx)
	if err != nil {
		t.Fatalf("%s Parents() error = %v", label, err)
	}
	for _, p := range parents {
		n, err := s.Count(ctx, p)
		if err != nil {
			t.Fatalf("%s Count(%q) error = %v", label, p, err)
		}
		if n != 0 {
			t.Fatalf("%s scope %q holds %d records, want 0 after a failed Issue", label, p, n)
		}
	}
}

var errWriteOrderStore = errors.New("store unavailable")

// A failed control Create must leave no mark naming the failed control's
// mRID on any older control it would have superseded: candidates are
// computed read-only, before either store is written, so a failed Create
// never leaves a dangling SupersededBy.
func TestIssue_ControlCreateFailure_LeavesNoDanglingSupersedeMark(t *testing.T) {
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord]()}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c0, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue C0: %v", err)
	}

	controls.failCreate = errWriteOrderStore
	nStart := start + 500 // overlaps C0
	_, err = issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect,
		Start:           &nStart,
		DurationSeconds: 1000,
	})
	if !errors.Is(err, errWriteOrderStore) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errWriteOrderStore)
	}

	lc0, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c0.ID)
	if gerr != nil {
		t.Fatalf("load C0 lifecycle: %v", gerr)
	}
	if lc0.SupersededAt != nil {
		t.Fatalf("C0.SupersededAt = %v, want nil (N's control Create failed, so no mark may name N's unstored mRID)", lc0.SupersededAt)
	}
	n, cerr := controls.Count(context.Background(), "dev1/0/p1")
	if cerr != nil {
		t.Fatalf("Count() error = %v", cerr)
	}
	if n != 1 {
		t.Fatalf("controls in scope = %d, want 1 (only C0; N's failed Create must not leave a partial record)", n)
	}
}

// A failed lifecycle Create must roll back the control Create that already
// committed: a control is never left without a lifecycle record.
func TestIssue_LifecycleCreateFailure_RollsBackControl(t *testing.T) {
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), failCreate: errWriteOrderStore}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	if !errors.Is(err, errWriteOrderStore) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errWriteOrderStore)
	}
	assertScopedStoreEmpty(t, controls.ScopedStore, "control")
	assertScopedStoreEmpty(t, lifecycles.ScopedStore, "lifecycle")
}

// When the rollback delete itself fails, Issue must report both failures:
// the caller cannot otherwise know the control was left orphaned.
func TestIssue_LifecycleCreateFailure_RollbackDeleteAlsoFails(t *testing.T) {
	errDelete := errors.New("delete failed")
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), failDelete: errDelete}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), failCreate: errWriteOrderStore}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	if !errors.Is(err, errWriteOrderStore) {
		t.Fatalf("Issue() error = %v, want wrapping the lifecycle store failure %v", err, errWriteOrderStore)
	}
	if !errors.Is(err, errDelete) {
		t.Fatalf("Issue() error = %v, want also wrapping the rollback failure %v", err, errDelete)
	}
}

// A failure marking a later candidate must revert any earlier candidate
// already marked in the same call: the new control's own store already
// committed by this point, but no older control may be left half-marked by
// a control this call is about to report as failed.
func TestIssue_SupersedeMarkFailure_RevertsAppliedMarks(t *testing.T) {
	errMark := errors.New("mark failed")
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		updateFailAt: map[int]error{2: errMark},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	// C1 and C2 do not overlap each other, so issuing C2 marks nothing: the
	// only Update calls in this test come from N's supersede scan below.
	start := sep2time.Now().Unix() + 1000
	c1, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 400,
	})
	if err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	start2 := start + 1000
	c2, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start2,
		DurationSeconds: 400,
	})
	if err != nil {
		t.Fatalf("issue C2: %v", err)
	}

	nStart := start + 100 // overlaps both C1 and C2
	_, err = issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect,
		Start:           &nStart,
		DurationSeconds: 1900,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errMark)
	}

	lc1, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c1.ID)
	if gerr != nil {
		t.Fatalf("load C1 lifecycle: %v", gerr)
	}
	if lc1.SupersededAt != nil {
		t.Fatalf("C1.SupersededAt = %v, want nil (reverted after C2's mark failed)", lc1.SupersededAt)
	}
	lc2, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c2.ID)
	if gerr != nil {
		t.Fatalf("load C2 lifecycle: %v", gerr)
	}
	if lc2.SupersededAt != nil {
		t.Fatalf("C2.SupersededAt = %v, want nil (its own mark failed)", lc2.SupersededAt)
	}
}

// When the revert of an already-applied mark itself fails, Issue must
// report both failures.
func TestIssue_SupersedeMarkFailure_RevertFailureIsReported(t *testing.T) {
	errMark := errors.New("mark failed")
	errRevert := errors.New("revert failed")
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		updateFailAt: map[int]error{2: errMark, 3: errRevert},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 400,
	}); err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	start2 := start + 1000
	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start2,
		DurationSeconds: 400,
	}); err != nil {
		t.Fatalf("issue C2: %v", err)
	}

	nStart := start + 100
	_, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect,
		Start:           &nStart,
		DurationSeconds: 1900,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping the mark failure %v", err, errMark)
	}
	if !errors.Is(err, errRevert) {
		t.Fatalf("Issue() error = %v, want also wrapping the revert failure %v", err, errRevert)
	}
}
