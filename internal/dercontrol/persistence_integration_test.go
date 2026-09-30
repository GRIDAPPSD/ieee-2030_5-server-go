package dercontrol

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestPersistence_IssueCancelReloadDerivedStatus is acceptance criterion 1:
// issue one control, cancel another, rebuild the DERControl and
// LifecycleRecord stores from disk, and assert both controls, both
// lifecycle records, and the derived statuses (cancelled/not) Issuer.Cancel
// relies on.
//
// The reload halves use a fresh Issuer over the reloaded stores rather than
// reaching into LifecycleRecord's unexported predicates directly: Cancel's
// own refusal branches (RefusalAlreadyCancelled, and success on a live
// control) are exactly the "derived status" logic acceptance criterion 1
// means, and this stays inside the package that owns them without touching
// #564's separate serve-time status derivation.
func TestPersistence_IssueCancelReloadDerivedStatus(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "dercontrols.json")
	lifecyclePath := filepath.Join(dir, "dercontrol-lifecycles.json")

	programs := memory.NewScopedStore[sep2.DERProgram]()
	const edev, fsa, derp = "0", "0", "0"
	link := controlListHref(edev, fsa, derp)
	if err := programs.Create(ctx, edev, derp, sep2.DERProgram{
		DERControlListLink: &sep2.ListLink{Href: link},
	}); err != nil {
		t.Fatalf("seed program: %v", err)
	}

	controls, err := memory.NewDERControlStoreWithPersistence(controlPath)
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}
	lifecycles, err := NewLifecycleStoreWithPersistence(lifecyclePath)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}
	cfg := Config{PEN: testPEN(1)}
	issuer, err := NewIssuer(programs, controls, lifecycles, cfg)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}

	href := programHref(edev, fsa, derp)
	// Different control shapes (connect vs. maxLimW) so the two never
	// trigger the supersede scan: this test is about persistence, not
	// acceptance criterion 7's overlap logic.
	resA, err := issuer.Issue(ctx, CreateRequest{DERProgramHref: href, Type: Connect, DurationSeconds: 3600})
	if err != nil {
		t.Fatalf("Issue control A: %v", err)
	}
	limW := uint16ptr(500)
	resB, err := issuer.Issue(ctx, CreateRequest{DERProgramHref: href, Type: MaxLimW, MaxLimW: limW, DurationSeconds: 3600})
	if err != nil {
		t.Fatalf("Issue control B: %v", err)
	}
	if _, err := issuer.Cancel(ctx, resB.Scope, resB.ID, "operator: superseded fleet plan"); err != nil {
		t.Fatalf("Cancel control B: %v", err)
	}

	// Rebuild both stores from disk, in a fresh process's shoes.
	reloadedControls, err := memory.NewDERControlStoreWithPersistence(controlPath)
	if err != nil {
		t.Fatalf("reload DERControlStore: %v", err)
	}
	reloadedLifecycles, err := NewLifecycleStoreWithPersistence(lifecyclePath)
	if err != nil {
		t.Fatalf("reload LifecycleStore: %v", err)
	}

	scopeKey := scopeKeyOf(resA.Scope)
	gotA, err := reloadedControls.Get(ctx, scopeKey, resA.ID)
	if err != nil {
		t.Fatalf("reloaded control A: %v", err)
	}
	if gotA.MRID != resA.Control.MRID {
		t.Errorf("reloaded control A MRID = %q, want %q", gotA.MRID, resA.Control.MRID)
	}
	gotB, err := reloadedControls.Get(ctx, scopeKey, resB.ID)
	if err != nil {
		t.Fatalf("reloaded control B: %v", err)
	}
	if gotB.MRID != resB.Control.MRID {
		t.Errorf("reloaded control B MRID = %q, want %q", gotB.MRID, resB.Control.MRID)
	}

	lcA, err := reloadedLifecycles.Get(ctx, scopeKey, resA.ID)
	if err != nil {
		t.Fatalf("reloaded lifecycle A: %v", err)
	}
	if lcA.cancelled() {
		t.Errorf("reloaded lifecycle A is cancelled, want not cancelled")
	}
	lcB, err := reloadedLifecycles.Get(ctx, scopeKey, resB.ID)
	if err != nil {
		t.Fatalf("reloaded lifecycle B: %v", err)
	}
	if !lcB.cancelled() {
		t.Errorf("reloaded lifecycle B is not cancelled, want cancelled")
	}
	if lcB.CancelReason != "operator: superseded fleet plan" {
		t.Errorf("reloaded lifecycle B CancelReason = %q, want the recorded reason", lcB.CancelReason)
	}

	// The derived status itself, exercised through the same Issuer.Cancel
	// logic a live server uses: a fresh Issuer over the reloaded stores must
	// still refuse cancelling the already-cancelled control, and must still
	// accept cancelling the untouched one.
	freshPrograms := memory.NewScopedStore[sep2.DERProgram]()
	issuer2, err := NewIssuer(freshPrograms, reloadedControls, reloadedLifecycles, cfg)
	if err != nil {
		t.Fatalf("NewIssuer over reloaded stores: %v", err)
	}
	if _, err := issuer2.Cancel(ctx, resA.Scope, resB.ID, "second attempt"); err == nil {
		t.Fatal("Cancel on the already-cancelled control succeeded after reload, want RefusalAlreadyCancelled")
	} else {
		assertRefusal(t, err, RefusalAlreadyCancelled)
	}
	if _, err := issuer2.Cancel(ctx, resA.Scope, resA.ID, "first cancel of A"); err != nil {
		t.Fatalf("Cancel on the untouched control failed after reload: %v, want success", err)
	}
}
