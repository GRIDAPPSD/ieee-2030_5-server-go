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

// Every Issue undo path names the control by mRID and scope, including the
// two where the store write that failed was the control or its record.
func TestIssue_UndoErrorNamesTheControlOnEveryPath(t *testing.T) {
	boom := errors.New("store failed")
	cases := []struct {
		name        string
		controls    *failingControls
		lifecycles  *failingLifecycles
		controlKept bool
	}{
		{
			name:        "control create applied and its delete fails",
			controls:    &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl](), createFailAt: map[int]error{1: boom}, createModeAt: map[int]failMode{1: failApplied}, failDelete: boom},
			lifecycles:  &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord]()},
			controlKept: true,
		},
		{
			name:       "lifecycle create applied and its delete fails",
			controls:   &failingControls{ScopedStore: memory.NewScopedStore[sep2.DERControl]()},
			lifecycles: &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), createFailAt: map[int]error{1: boom}, createModeAt: map[int]failMode{1: failApplied}, failDelete: boom},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer, programs := newWriteOrderIssuer(t, tc.controls, tc.lifecycles)
			seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			_, err := issuer.Issue(context.Background(), CreateRequest{DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, DurationSeconds: 3600})
			var undo *UndoError
			if !errors.As(err, &undo) {
				t.Fatalf("err = %v, want *UndoError", err)
			}
			if undo.ControlKept != tc.controlKept {
				t.Fatalf("ControlKept = %v, want %v", undo.ControlKept, tc.controlKept)
			}
			if len(undo.MRID) != 32 || undo.Scope != (Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}) {
				t.Fatalf("UndoError names mRID %q scope %+v", undo.MRID, undo.Scope)
			}
			if tc.controlKept {
				stored, err := tc.controls.ScopedStore.Get(context.Background(), "dev1/0/p1", undo.ID)
				if err != nil || stored.MRID != undo.MRID {
					t.Fatalf("stored control %+v (%v), want mRID %s", stored, err, undo.MRID)
				}
			}
		})
	}
}
