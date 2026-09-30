package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// An UndoError from Issue names the control it may have left stored, by
// mRID and scope, so a caller can report a live control to the operator.
func TestIssue_UndoErrorNamesTheKeptControl(t *testing.T) {
	controls := &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()}
	boom := errors.New("lifecycle update failed")
	lifecycles := &failingLifecycles{
		ScopedStore:  memory.NewScopedStore[LifecycleRecord](),
		updateFailAt: map[int]error{1: boom, 2: boom}, // the mark, then its restore
	}
	issuer, programs := newWriteOrderIssuer(t, controls, lifecycles)
	if err := programs.Create(context.Background(), "dev1", "p1", sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: controlListHref("dev1", "0", "p1")}}); err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, DurationSeconds: 3600}
	if _, err := issuer.Issue(context.Background(), req); err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	_, err := issuer.Issue(context.Background(), req)
	var undo *UndoError
	if !errors.As(err, &undo) {
		t.Fatalf("second Issue err = %v, want *UndoError", err)
	}
	if !undo.ControlKept {
		t.Fatalf("ControlKept = false, want true")
	}
	stored, err := controls.Get(context.Background(), "dev1/0/p1", undo.ID)
	if err != nil {
		t.Fatalf("kept control not stored: %v", err)
	}
	if undo.MRID == "" || undo.MRID != stored.MRID {
		t.Errorf("UndoError.MRID = %q, want the stored control's %q", undo.MRID, stored.MRID)
	}
	if undo.Scope != (Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}) {
		t.Errorf("UndoError.Scope = %+v", undo.Scope)
	}
}
