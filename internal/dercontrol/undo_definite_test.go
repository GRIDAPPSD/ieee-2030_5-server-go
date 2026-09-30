package dercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// persistedHarness is newHarness's shape over the REAL persisted stores
// (memory.DERControlStore, LifecycleStore) rather than the bare in-memory
// test doubles newHarness builds. GRIDAPPSD/ieee-2030_5-server-go#565
// round 2 item 1 needs the real stores: they are what implements
// selfRollingBack, and the bug the tests below prove fixed exists only
// against a store that actually rolls back on a failed write.
type persistedHarness struct {
	issuer        *Issuer
	programs      *memory.ScopedStore[sep2.DERProgram]
	controls      *memory.DERControlStore
	lifecycles    *LifecycleStore
	lifecyclePath string
}

func newPersistedHarness(t *testing.T, cfg Config) *persistedHarness {
	t.Helper()
	dir := t.TempDir()
	lifecyclePath := filepath.Join(dir, "dercontrol-lifecycles.json")
	controls, err := memory.NewDERControlStoreWithPersistence(filepath.Join(dir, "dercontrols.json"))
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}
	lifecycles, err := NewLifecycleStoreWithPersistence(lifecyclePath)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}
	h := &persistedHarness{
		programs:      memory.NewScopedStore[sep2.DERProgram](),
		controls:      controls,
		lifecycles:    lifecycles,
		lifecyclePath: lifecyclePath,
	}
	issuer, err := NewIssuer(h.programs, h.controls, h.lifecycles, cfg)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	h.issuer = issuer
	return h
}

func (h *persistedHarness) seedProgram(t *testing.T, edev, derp, linkHref string) {
	t.Helper()
	p := sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: linkHref}}
	if err := h.programs.Create(context.Background(), edev, derp, p); err != nil {
		t.Fatalf("seed program: %v", err)
	}
}

// TestCancel_DiskWriteFails_ReturnsDefiniteNotUndoError is
// GRIDAPPSD/ieee-2030_5-server-go#565 round 2 item 1, the Cancel half:
// over the real persisting LifecycleStore, a Cancel whose disk write fails
// must return a plain error (issuer.go's own documented contract, "the
// record equals what Cancel read"), never an *UndoError, because the
// store's own rollback has already restored the record before Issuer's
// compensating write would even run.
func TestCancel_DiskWriteFails_ReturnsDefiniteNotUndoError(t *testing.T) {
	h := newPersistedHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	blockLifecyclePersist(t, h.lifecyclePath)

	if _, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "operator request"); err == nil {
		t.Fatal("Cancel with a blocked lifecycle path returned nil error, want the persist failure")
	} else {
		var undo *UndoError
		if errors.As(err, &undo) {
			t.Fatalf("Cancel returned *UndoError = %+v, want a plain error: the store's own rollback already made the outcome definite", undo)
		}
	}

	scopeKey := scopeKeyOf(res.Scope)
	lc, gerr := h.lifecycles.Get(context.Background(), scopeKey, res.ID)
	if gerr != nil {
		t.Fatalf("Get lifecycle after the failed Cancel: %v", gerr)
	}
	if lc.cancelled() {
		t.Fatal("lifecycle record reports cancelled() after a failed Cancel, want the pre-cancel value")
	}
}

// TestIssue_SupersedeMarkDiskWriteFails_ReturnsDefiniteNotUndoError is
// round 2 item 1, the supersede-mark half (undoMarkFailure "has the same
// shape" as undoCancelFailure). Control C is issued first; issuing N,
// which supersedes C, has its mark-write on C fail, and the path stays
// sabotaged afterward (so a retry of that same write would fail too,
// exactly as an unresolved disk fault would). The store rolls the
// original failure back on its own, so C's lifecycle record is already
// unchanged; the compensating re-write undoMarkFailure used to attempt
// for exactly that candidate is now skipped, so C's id must never appear
// in UnrevertedIDs. undoMarkFailure's other two compensating deletes
// (which undo N's own, separately successful writes) still fail against
// the same sabotaged path and are still genuinely uncertain, so an
// UndoError for THOSE is expected and not itself the property under test.
func TestIssue_SupersedeMarkDiskWriteFails_ReturnsDefiniteNotUndoError(t *testing.T) {
	h := newPersistedHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	// Lifecycle write #1 from here is N's own Create (must succeed so the
	// supersede scan and the mark step are reached at all); #2 is
	// applySupersedes's mark-Update on C, sabotaged and left that way.
	calls := 0
	h.lifecycles.afterMutateBeforePersist = func() {
		calls++
		if calls == 2 {
			if err := os.MkdirAll(filepath.Join(h.lifecyclePath+".tmp", "keep"), 0o700); err != nil {
				t.Fatalf("block lifecycle path: %v", err)
			}
		}
	}

	nStart := start + 500
	_, err = h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Disconnect, // same control set as Connect: opModConnect/opModEnergize
		Start:           &nStart,
		DurationSeconds: 1000,
	})
	if err == nil {
		t.Fatal("Issue N with a blocked supersede-mark write returned nil error, want the persist failure")
	}
	var undo *UndoError
	if errors.As(err, &undo) {
		for _, unrevertedID := range undo.UnrevertedIDs {
			if unrevertedID == c.ID {
				t.Fatalf("UndoError.UnrevertedIDs = %v includes C (%q), want C skipped: its own mark-write failure was already rolled back by the store", undo.UnrevertedIDs, c.ID)
			}
		}
	}

	lc, gerr := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if gerr != nil {
		t.Fatalf("Get C's lifecycle after the failed Issue: %v", gerr)
	}
	if lc.SupersededAt != nil {
		t.Fatalf("C.SupersededAt = %v after the failed Issue, want nil: C was never actually marked", lc.SupersededAt)
	}
}
