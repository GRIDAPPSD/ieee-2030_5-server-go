package dercontrol

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Acceptance criteria 7 and 8 require that Issue and Cancel are
// all-or-nothing: a partial write never leaves a lifecycle mark naming a
// control that was never stored, never leaves a stored control without a
// lifecycle record, and undoes every write it attempted on any failure
// that is not an *UndoError. These tests inject a failure at each write
// Issue and Cancel make and assert the stores directly afterward, not
// merely that the call returned an error.

// failMode selects how a forced store failure interacts with the
// underlying write: clean means the write never reaches the real store, so
// nothing changes; applied means it does, and the store's failure is
// reported only after the change already took effect. A persistent store
// can behave the "applied" way when its in-memory write succeeds and its
// snapshot write does not, and the undo path must handle both.
type failMode int

const (
	failClean failMode = iota
	failApplied
)

// loggedCall is one call a wrapper recorded: which operation, which id, and
// the ctx state observed at the moment of the call. The ctx state is
// captured at log time rather than keeping the context.Context itself,
// because an undo context is cancelled by a deferred call as soon as its
// undo function returns; a test reading the raw context after Issue or
// Cancel has already returned would always see it done, regardless of
// what it looked like when the write actually happened.
type loggedCall struct {
	op          string
	id          string
	errAtLog    error
	deadline    time.Time
	hasDeadline bool
}

// callLog is a mutex-guarded record of every call a wrapper makes, shared
// between a test's controls and lifecycles wrappers so call order and
// count are comparable across both stores.
type callLog struct {
	mu    sync.Mutex
	calls []loggedCall
}

func (l *callLog) record(op, id string, ctx context.Context) {
	dl, ok := ctx.Deadline()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, loggedCall{op: op, id: id, errAtLog: ctx.Err(), deadline: dl, hasDeadline: ok})
}

func (l *callLog) snapshot() []loggedCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]loggedCall, len(l.calls))
	copy(out, l.calls)
	return out
}

func (l *callLog) count(op string) int {
	n := 0
	for _, c := range l.snapshot() {
		if c.op == op {
			n++
		}
	}
	return n
}

func (l *callLog) idCount(op, id string) int {
	n := 0
	for _, c := range l.snapshot() {
		if c.op == op && c.id == id {
			n++
		}
	}
	return n
}

// lastLoggedID returns the id of the most recent call logged for op,
// failing the test if none was logged. It is how a test recovers the store
// id of a control or record Issue failed to return (a fresh mRID is
// random, so the id cannot be predicted ahead of the call).
func lastLoggedID(t *testing.T, log *callLog, op string) string {
	t.Helper()
	calls := log.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].op == op {
			return calls[i].id
		}
	}
	t.Fatalf("no logged call for op %q", op)
	return ""
}

// failingControls wraps a real control store so a test can force Create or
// Delete to fail without a real backend, in either failMode, and can make
// either observe ctx cancellation the way a real network-backed store
// would (honorCtx).
type failingControls struct {
	*memory.ScopedStore[sep2.DERControl]
	log *callLog

	createCalls  int
	createFailAt map[int]error
	createModeAt map[int]failMode

	failDelete error
	deleteMode failMode
	honorCtx   bool
}

func (f *failingControls) Create(ctx context.Context, parentID, id string, resource sep2.DERControl) error {
	f.createCalls++
	n := f.createCalls
	if f.log != nil {
		f.log.record("controls.Create", id, ctx)
	}
	if err, ok := f.createFailAt[n]; ok {
		if f.createModeAt[n] == failApplied {
			_ = f.ScopedStore.Create(ctx, parentID, id, resource)
		}
		return err
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return f.ScopedStore.Create(ctx, parentID, id, resource)
}

func (f *failingControls) Delete(ctx context.Context, parentID, id string) error {
	if f.log != nil {
		f.log.record("controls.Delete", id, ctx)
	}
	if f.failDelete != nil {
		if f.deleteMode == failApplied {
			_ = f.ScopedStore.Delete(ctx, parentID, id)
		}
		return f.failDelete
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return f.ScopedStore.Delete(ctx, parentID, id)
}

// failingLifecycles wraps a real lifecycle store so a test can force
// Create or Delete to fail, or force specific Update calls (counted from 1,
// across every call the wrapper receives) to fail, in either failMode. It
// can also cancel a caller-supplied context part way through a specific
// Update call (cancelCtxAtCall), for the tests proving undo does not use
// that same context.
type failingLifecycles struct {
	*memory.ScopedStore[LifecycleRecord]
	log *callLog

	createCalls  int
	createFailAt map[int]error
	createModeAt map[int]failMode

	failDelete error
	deleteMode failMode

	updateCalls  int
	updateFailAt map[int]error
	updateModeAt map[int]failMode

	cancelCtxAtCall int
	cancelFn        context.CancelFunc

	honorCtx bool
}

func (f *failingLifecycles) Create(ctx context.Context, parentID, id string, resource LifecycleRecord) error {
	f.createCalls++
	n := f.createCalls
	if f.log != nil {
		f.log.record("lifecycles.Create", id, ctx)
	}
	if err, ok := f.createFailAt[n]; ok {
		if f.createModeAt[n] == failApplied {
			_ = f.ScopedStore.Create(ctx, parentID, id, resource)
		}
		return err
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return f.ScopedStore.Create(ctx, parentID, id, resource)
}

func (f *failingLifecycles) Update(ctx context.Context, parentID, id string, resource LifecycleRecord) error {
	f.updateCalls++
	n := f.updateCalls
	if f.log != nil {
		f.log.record("lifecycles.Update", id, ctx)
	}
	if n == f.cancelCtxAtCall && f.cancelFn != nil {
		f.cancelFn()
	}
	if err, ok := f.updateFailAt[n]; ok {
		if f.updateModeAt[n] == failApplied {
			_ = f.ScopedStore.Update(ctx, parentID, id, resource)
		}
		return err
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return f.ScopedStore.Update(ctx, parentID, id, resource)
}

func (f *failingLifecycles) Delete(ctx context.Context, parentID, id string) error {
	if f.log != nil {
		f.log.record("lifecycles.Delete", id, ctx)
	}
	if f.failDelete != nil {
		if f.deleteMode == failApplied {
			_ = f.ScopedStore.Delete(ctx, parentID, id)
		}
		return f.failDelete
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return f.ScopedStore.Delete(ctx, parentID, id)
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

// newFailingLifecycleHarness is newWriteOrderIssuer's counterpart for the
// Cancel tests: a real control store (Cancel's own write is on the
// lifecycle store only) plus a failing lifecycle store.
func newFailingLifecycleHarness(t *testing.T, lifecycles *failingLifecycles) (*Issuer, *memory.ScopedStore[sep2.DERProgram], *memory.ScopedStore[sep2.DERControl]) {
	t.Helper()
	programs := memory.NewScopedStore[sep2.DERProgram]()
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, err := NewIssuer(programs, controls, lifecycles, Config{PEN: testPEN(1)})
	if err != nil {
		t.Fatalf("NewIssuer() error = %v", err)
	}
	return issuer, programs, controls
}

func seedWriteOrderProgram(t *testing.T, programs *memory.ScopedStore[sep2.DERProgram], edev, derp, linkHref string) {
	t.Helper()
	p := sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: linkHref}}
	if err := programs.Create(context.Background(), edev, derp, p); err != nil {
		t.Fatalf("seed program: %v", err)
	}
}

var errWriteOrderStore = errors.New("store unavailable")

// A lifecycle Create failure that never applied (clean) leaves only the
// pre-existing candidate stored and unmarked, and reports a plain wrapped
// error, not an *UndoError: nothing needed undoing because the failed
// write never took effect. Marks or a control Create moved before the
// lifecycle write would mark or store something this call never created.
func TestIssue_LifecycleCreateFailure_Clean(t *testing.T) {
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log, createFailAt: map[int]error{2: errWriteOrderStore}}
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
	var undo *UndoError
	if errors.As(err, &undo) {
		t.Fatalf("Issue() error = %v, want no *UndoError (nothing was written)", err)
	}

	lc0, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c0.ID)
	if gerr != nil {
		t.Fatalf("load C0 lifecycle: %v", gerr)
	}
	if lc0.SupersededAt != nil {
		t.Fatalf("C0.SupersededAt = %v, want nil (N's lifecycle Create failed, so no mark may name N's unstored mRID)", lc0.SupersededAt)
	}
	n, cerr := controls.Count(context.Background(), "dev1/0/p1")
	if cerr != nil {
		t.Fatalf("Count() error = %v", cerr)
	}
	if n != 1 {
		t.Fatalf("controls in scope = %d, want 1 (only C0; N's failed lifecycle Create must not leave a control)", n)
	}
}

// A lifecycle Create failure whose write actually applied leaves no
// record behind: the undo path always attempts the delete, whether or not
// the failed write took effect.
func TestIssue_LifecycleCreateFailure_Applied(t *testing.T) {
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log, createFailAt: map[int]error{2: errWriteOrderStore}, createModeAt: map[int]failMode{2: failApplied}}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}); err != nil {
		t.Fatalf("issue C0: %v", err)
	}

	_, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	if !errors.Is(err, errWriteOrderStore) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errWriteOrderStore)
	}

	id := lastLoggedID(t, log, "lifecycles.Create")
	if _, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", id); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("lifecycle record for %q: err = %v, want store.ErrNotFound (undo must delete it)", id, gerr)
	}
	if n := log.count("controls.Create"); n != 1 {
		t.Fatalf("controls.Create calls logged = %d, want 1 (C0's own; a failed lifecycle Create for N must never reach the control store)", n)
	}
}

// A control Create failure, in both write modes, leaves no trace of N and
// no mark on the overlapping candidate: the lifecycle record N's Create
// already stored is deleted along with the control rollback.
func TestIssue_ControlCreateFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode failMode
	}{
		{"clean", failClean},
		{"applied", failApplied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &callLog{}
			controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log, createFailAt: map[int]error{2: errWriteOrderStore}, createModeAt: map[int]failMode{2: tc.mode}}
			lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log}
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

			nStart := start + 500
			_, err = issuer.Issue(context.Background(), CreateRequest{
				DERProgramHref:  programHref("dev1", "0", "p1"),
				Type:            Disconnect,
				Start:           &nStart,
				DurationSeconds: 1000,
			})
			if !errors.Is(err, errWriteOrderStore) {
				t.Fatalf("Issue() error = %v, want wrapping %v", err, errWriteOrderStore)
			}

			lcID := lastLoggedID(t, log, "lifecycles.Create")
			if _, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", lcID); !errors.Is(gerr, store.ErrNotFound) {
				t.Fatalf("N's lifecycle record: err = %v, want store.ErrNotFound", gerr)
			}
			n, cerr := controls.Count(context.Background(), "dev1/0/p1")
			if cerr != nil {
				t.Fatalf("Count() error = %v", cerr)
			}
			if n != 1 {
				t.Fatalf("controls in scope = %d, want 1 (only C0)", n)
			}
			lc0, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c0.ID)
			if gerr != nil {
				t.Fatalf("load C0 lifecycle: %v", gerr)
			}
			if lc0.SupersededAt != nil {
				t.Fatalf("C0.SupersededAt = %v, want nil (N's control Create failed, so no mark may name N's unstored control)", lc0.SupersededAt)
			}
		})
	}
}

// When the control Create failure applied and the rollback Delete itself
// fails, Issue reports an *UndoError: the control cannot be proven gone,
// so its lifecycle record is kept too, and both causes are reachable
// with errors.Is.
func TestIssue_ControlCreateFailure_RollbackDeleteAlsoFails(t *testing.T) {
	errDelete := errors.New("delete failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log, createFailAt: map[int]error{1: errWriteOrderStore}, createModeAt: map[int]failMode{1: failApplied}, failDelete: errDelete}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log}
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
	if !errors.Is(err, errDelete) {
		t.Fatalf("Issue() error = %v, want also wrapping %v", err, errDelete)
	}
	var undo *UndoError
	if !errors.As(err, &undo) {
		t.Fatalf("Issue() error = %v (%T), want *UndoError", err, err)
	}
	if !undo.ControlKept || !undo.LifecycleKept {
		t.Fatalf("UndoError = %+v, want ControlKept and LifecycleKept true", undo)
	}

	lcID := lastLoggedID(t, log, "lifecycles.Create")
	if _, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", lcID); gerr != nil {
		t.Fatalf("N's lifecycle record missing: %v, want kept (control Delete failed, so the record must not be removed)", gerr)
	}
}

// A mark failure (clean write) leaves the new control and its record
// deleted, restores every candidate already marked in this call to its
// seeded value, and returns a zero Result. Head left N's own store
// untouched on this path.
func TestIssue_SupersedeMarkFailure_RevertsAppliedMarks(t *testing.T) {
	errMark := errors.New("mark failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
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
	res, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect,
		Start:           &nStart,
		DurationSeconds: 1900,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errMark)
	}
	if res.ID != "" || res.Href != "" || res.Scope != (Scope{}) || res.Control.MRID != "" || len(res.Supersedes) != 0 {
		t.Fatalf("Result = %+v, want zero", res)
	}

	nID := lastLoggedID(t, log, "controls.Create")
	if _, gerr := controls.Get(context.Background(), "dev1/0/p1", nID); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("N's control: err = %v, want store.ErrNotFound", gerr)
	}
	nlcID := lastLoggedID(t, log, "lifecycles.Create")
	if _, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", nlcID); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("N's lifecycle record: err = %v, want store.ErrNotFound", gerr)
	}

	lc1, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c1.ID)
	if gerr != nil {
		t.Fatalf("load C1 lifecycle: %v", gerr)
	}
	if lc1 != (LifecycleRecord{}) {
		t.Fatalf("C1 lifecycle = %+v, want its seeded zero value (reverted after C2's mark failed)", lc1)
	}
	lc2, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c2.ID)
	if gerr != nil {
		t.Fatalf("load C2 lifecycle: %v", gerr)
	}
	if lc2 != (LifecycleRecord{}) {
		t.Fatalf("C2 lifecycle = %+v, want its seeded zero value (its own mark failed)", lc2)
	}
}

// A revert restores the exact record read under the lock, not a blank
// one. C1 is already superseded by an earlier control when N attempts to
// mark both C1 and a second candidate C2; C2's mark fails, so C1's revert
// must put back the earlier mark, not LifecycleRecord{}, or that earlier
// supersede would be silently erased. The earlier mark is seeded directly
// on the lifecycle store (as cancel_test.go seeds supersede state for its
// own refusal tests) so the two candidates can be placed without the
// geometry of a third real control.
func TestIssue_SupersedeMarkFailure_RevertRestoresExactPriorRecord(t *testing.T) {
	errMark := errors.New("mark failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{2: errMark},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c1, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000, // C1: [start, start+1000)
	})
	if err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	c2Start := start + 2000
	c2, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &c2Start,
		DurationSeconds: 400, // C2: [start+2000, start+2400), disjoint from C1
	})
	if err != nil {
		t.Fatalf("issue C2: %v", err)
	}

	// Seed C1 as already superseded by an earlier, unrelated control, going
	// straight to the real store so this write is not counted by the
	// wrapper's call numbering above.
	n1Start := start + 800
	const n1MRID = "DEADBEEF00000000000000000000001"
	if err := lifecycles.ScopedStore.Update(context.Background(), "dev1/0/p1", c1.ID, LifecycleRecord{SupersededAt: &n1Start, SupersededBy: n1MRID}); err != nil {
		t.Fatalf("seed C1 as already superseded: %v", err)
	}

	nStart := start + 100 // overlaps both C1 and C2; earlier than n1Start, so C1 stays eligible
	_, err = issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect,
		Start:           &nStart,
		DurationSeconds: 2500,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue(N) error = %v, want wrapping %v", err, errMark)
	}

	lc1, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c1.ID)
	if gerr != nil {
		t.Fatalf("load C1 lifecycle: %v", gerr)
	}
	if lc1.SupersededAt == nil || *lc1.SupersededAt != n1Start || lc1.SupersededBy != n1MRID {
		t.Fatalf("C1 lifecycle = %+v, want SupersededAt=%d SupersededBy=%q (the earlier mark, restored exactly, not wiped)", lc1, n1Start, n1MRID)
	}
	_ = c2
}

// When a mark failure's write actually applied, the revert must restore
// the failing candidate itself, not merely the ones marked before it:
// reverting only candidates[:n] would leave it marked by a control Issue
// is about to report as never created.
func TestIssue_SupersedeMarkFailure_RevertsFailingCandidateWhenApplied(t *testing.T) {
	errMark := errors.New("mark failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{2: errMark},
		updateModeAt: map[int]failMode{2: failApplied},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

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
	if lc1 != (LifecycleRecord{}) {
		t.Fatalf("C1 lifecycle = %+v, want its seeded zero value", lc1)
	}
	lc2, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c2.ID)
	if gerr != nil {
		t.Fatalf("load C2 lifecycle: %v", gerr)
	}
	if lc2 != (LifecycleRecord{}) {
		t.Fatalf("C2 lifecycle = %+v, want its seeded zero value (reverted even though its own mark applied before returning the error)", lc2)
	}
}

// When reverting two already-marked candidates both fail, Issue reports
// an *UndoError naming both ids, wraps the mark failure and both revert
// failures, keeps the new control and its record, and its text names no
// candidate id, no N id, and no mRID.
func TestIssue_SupersedeMarkFailure_RevertFailureIsReported(t *testing.T) {
	errMark := errors.New("mark failed")
	errRevert1 := errors.New("revert 1 failed")
	errRevert2 := errors.New("revert 2 failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{3: errMark, 4: errRevert1, 5: errRevert2},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c1, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 300,
	})
	if err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	start2 := start + 1000
	c2, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start2, DurationSeconds: 300,
	})
	if err != nil {
		t.Fatalf("issue C2: %v", err)
	}
	start3 := start + 2000
	c3, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start3, DurationSeconds: 300,
	})
	if err != nil {
		t.Fatalf("issue C3: %v", err)
	}

	nStart := start + 100 // overlaps C1, C2 and C3
	_, err = issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Disconnect, Start: &nStart, DurationSeconds: 2200,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errMark)
	}
	if !errors.Is(err, errRevert1) || !errors.Is(err, errRevert2) {
		t.Fatalf("Issue() error = %v, want also wrapping both revert failures", err)
	}
	var undo *UndoError
	if !errors.As(err, &undo) {
		t.Fatalf("Issue() error = %v (%T), want *UndoError", err, err)
	}
	if !undo.ControlKept || !undo.LifecycleKept {
		t.Fatalf("UndoError = %+v, want ControlKept and LifecycleKept true", undo)
	}
	wantUnreverted := map[string]bool{c1.ID: true, c2.ID: true}
	if len(undo.UnrevertedIDs) != 2 || !wantUnreverted[undo.UnrevertedIDs[0]] || !wantUnreverted[undo.UnrevertedIDs[1]] {
		t.Fatalf("UnrevertedIDs = %v, want %v in some order", undo.UnrevertedIDs, []string{c1.ID, c2.ID})
	}
	if log.idCount("lifecycles.Update", c1.ID) < 2 || log.idCount("lifecycles.Update", c2.ID) < 2 {
		t.Fatalf("call log did not hold both a mark and a revert Update for C1 and C2")
	}

	nID := lastLoggedID(t, log, "controls.Create")
	for _, id := range []string{c1.ID, c2.ID, c3.ID, nID} {
		if strings.Contains(err.Error(), id) {
			t.Fatalf("error text %q contains candidate or N id %q", err.Error(), id)
		}
	}
}

// Undo writes use a context derived from the caller's, not the caller's
// own: cancelling the caller's context during the failing mark call must
// not defeat the revert, or the deletes of the new control and its
// record that follow it.
func TestIssue_SupersedeMarkFailure_UndoIgnoresCallerCancellation(t *testing.T) {
	errMark := errors.New("mark failed")
	log := &callLog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log, honorCtx: true}
	lifecycles := &failingLifecycles{
		ScopedStore:     memory.NewScopedStore[LifecycleRecord](),
		log:             log,
		honorCtx:        true,
		updateFailAt:    map[int]error{2: errMark},
		cancelCtxAtCall: 2,
		cancelFn:        cancel,
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c1, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 400,
	})
	if err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	start2 := start + 1000
	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start2, DurationSeconds: 400,
	}); err != nil {
		t.Fatalf("issue C2: %v", err)
	}

	nStart := start + 100 // overlaps both C1 and C2
	_, err = issuer.Issue(ctx, CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Disconnect, Start: &nStart, DurationSeconds: 1900,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errMark)
	}
	if ctx.Err() == nil {
		t.Fatalf("test setup: ctx was not cancelled during the failing call")
	}

	nID := lastLoggedID(t, log, "controls.Create")
	if _, gerr := controls.Get(context.Background(), "dev1/0/p1", nID); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("N's control: err = %v, want store.ErrNotFound (undo must not be defeated by caller cancellation)", gerr)
	}
	lc1, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", c1.ID)
	if gerr != nil {
		t.Fatalf("load C1 lifecycle: %v", gerr)
	}
	if lc1 != (LifecycleRecord{}) {
		t.Fatalf("C1 lifecycle = %+v, want its seeded zero value", lc1)
	}
}

// Every undo call's context carries its own deadline within undoTimeout
// of the call, and is not already done. context.WithoutCancel alone, with
// no timeout, would pass the caller-cancellation check above but leave a
// hung backend holding the scope lock indefinitely during undo.
func TestIssue_SupersedeMarkFailure_UndoContextIsBounded(t *testing.T) {
	errMark := errors.New("mark failed")
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{2: errMark},
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 400,
	}); err != nil {
		t.Fatalf("issue C1: %v", err)
	}
	start2 := start + 1000
	if _, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start2, DurationSeconds: 400,
	}); err != nil {
		t.Fatalf("issue C2: %v", err)
	}

	before := time.Now()
	nStart := start + 100
	_, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Disconnect, Start: &nStart, DurationSeconds: 1900,
	})
	if !errors.Is(err, errMark) {
		t.Fatalf("Issue() error = %v, want wrapping %v", err, errMark)
	}
	after := time.Now()

	// Forward writes use context.Background(), which carries no deadline;
	// only an undo context (undoContext) has one, so filtering on that
	// distinguishes undo calls from forward calls without depending on
	// exact call counts.
	var undoCalls []loggedCall
	for _, c := range log.snapshot() {
		if c.hasDeadline {
			undoCalls = append(undoCalls, c)
		}
	}
	if len(undoCalls) == 0 {
		t.Fatalf("no undo calls logged (none carried a deadline)")
	}
	for _, c := range undoCalls {
		if c.errAtLog != nil {
			t.Fatalf("undo call %s(%s) ctx already done at call time: %v", c.op, c.id, c.errAtLog)
		}
		if c.deadline.After(after.Add(undoTimeout)) || c.deadline.Before(before) {
			t.Fatalf("undo call %s(%s) deadline %v, want within %s of the call (between %v and %v)", c.op, c.id, c.deadline, undoTimeout, before, after.Add(undoTimeout))
		}
	}
}

// Issue checks ctx before its first write: a context cancelled before the
// call returns the cancellation directly, and the check happens before
// the store logs any Create or Update at all.
func TestIssue_CtxCancelledBeforeIssue(t *testing.T) {
	log := &callLog{}
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), log: log}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := issuer.Issue(ctx, CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue() error = %v, want wrapping context.Canceled", err)
	}
	if n := log.count("controls.Create") + log.count("lifecycles.Create"); n != 0 {
		t.Fatalf("Create calls logged = %d, want 0", n)
	}
	if n := log.count("lifecycles.Update"); n != 0 {
		t.Fatalf("Update calls logged = %d, want 0", n)
	}
}

// A Cancel Update failure that never applied (clean) leaves the record
// exactly as it was, and Cancel reports a plain wrapped error, not an
// *UndoError.
func TestCancel_UpdateFailure_Clean(t *testing.T) {
	errCancel := errors.New("cancel store unavailable")
	log := &callLog{}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log, updateFailAt: map[int]error{1: errCancel}}
	issuer, programs, _ := newFailingLifecycleHarness(t, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	res, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	seeded, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", res.ID)
	if gerr != nil {
		t.Fatalf("load seeded lifecycle: %v", gerr)
	}

	_, err = issuer.Cancel(context.Background(), res.Scope, res.ID, "operator stop")
	if !errors.Is(err, errCancel) {
		t.Fatalf("Cancel() error = %v, want wrapping %v", err, errCancel)
	}
	var undo *UndoError
	if errors.As(err, &undo) {
		t.Fatalf("Cancel() error = %v, want no *UndoError (nothing was written)", err)
	}

	got, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", res.ID)
	if gerr != nil {
		t.Fatalf("load lifecycle after failed Cancel: %v", gerr)
	}
	if got != seeded {
		t.Fatalf("lifecycle = %+v, want unchanged seeded value %+v", got, seeded)
	}
}

// A Cancel Update failure whose write applied is restored to the record
// Cancel read: CancelledAt and CancelReason revert to unset.
func TestCancel_UpdateFailure_Applied(t *testing.T) {
	errCancel := errors.New("cancel store unavailable")
	log := &callLog{}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{1: errCancel},
		updateModeAt: map[int]failMode{1: failApplied},
	}
	issuer, programs, _ := newFailingLifecycleHarness(t, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	res, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = issuer.Cancel(context.Background(), res.Scope, res.ID, "operator stop")
	if !errors.Is(err, errCancel) {
		t.Fatalf("Cancel() error = %v, want wrapping %v", err, errCancel)
	}

	got, gerr := lifecycles.Get(context.Background(), "dev1/0/p1", res.ID)
	if gerr != nil {
		t.Fatalf("load lifecycle after failed Cancel: %v", gerr)
	}
	if got.CancelledAt != nil {
		t.Fatalf("CancelledAt = %v, want nil (restored after the applied write's error)", got.CancelledAt)
	}
	if got.CancelReason != "" {
		t.Fatalf("CancelReason = %q, want empty", got.CancelReason)
	}
}

// When the restore of a failed, applied Cancel write itself fails,
// Cancel reports an *UndoError naming both causes rather than a plain
// error, since the record's final state is not known.
func TestCancel_UpdateFailure_RestoreAlsoFails(t *testing.T) {
	errCancel := errors.New("cancel store unavailable")
	errRestore := errors.New("restore failed")
	log := &callLog{}
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		log:          log,
		updateFailAt: map[int]error{1: errCancel, 2: errRestore},
		updateModeAt: map[int]failMode{1: failApplied},
	}
	issuer, programs, _ := newFailingLifecycleHarness(t, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	res, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = issuer.Cancel(context.Background(), res.Scope, res.ID, "operator stop")
	if !errors.Is(err, errCancel) || !errors.Is(err, errRestore) {
		t.Fatalf("Cancel() error = %v, want wrapping both %v and %v", err, errCancel, errRestore)
	}
	var undo *UndoError
	if !errors.As(err, &undo) {
		t.Fatalf("Cancel() error = %v (%T), want *UndoError", err, err)
	}
	if undo.Step != UndoStepCancel || undo.ID != res.ID {
		t.Fatalf("UndoError = %+v, want Step=%q ID=%q", undo, UndoStepCancel, res.ID)
	}
}

// Cancel checks ctx before its write: a cancelled context stops before
// any Update call.
func TestCancel_CtxCancelledBeforeWrite(t *testing.T) {
	log := &callLog{}
	lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: log}
	issuer, programs, _ := newFailingLifecycleHarness(t, lifecycles)
	seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	res, err := issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = issuer.Cancel(ctx, res.Scope, res.ID, "operator stop")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel() error = %v, want wrapping context.Canceled", err)
	}
	if n := log.count("lifecycles.Update"); n != 0 {
		t.Fatalf("Update calls logged = %d, want 0", n)
	}
}
