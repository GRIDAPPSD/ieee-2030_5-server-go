//go:build csip_test_hooks

package server_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// seedProgramInFSA stores DERProgram derpID under edevID with every href and
// link naming fsaID, one DERControl, its lifecycle record and a
// DefaultDERControl, all under the (edev, fsa, derp) scope.
func seedProgramInFSA(t *testing.T, stores *server.Stores, edevID, fsaID, derpID string) {
	t.Helper()
	ctx := context.Background()
	base := "/edev/" + edevID + "/fsa/" + fsaID + "/derp/" + derpID
	prog := sep2.DERProgram{MRID: "PROG-" + derpID, Primacy: 3}
	prog.Href = base
	prog.DefaultDERControlLink = &sep2.Link{Href: base + "/dderc"}
	prog.DERControlListLink = &sep2.ListLink{Href: base + "/derc", All: 1}
	prog.DERCurveListLink = &sep2.ListLink{Href: "/dc", All: 2}
	prog.ActiveDERControlListLink = &sep2.ListLink{Href: base + "/actderc", All: 0}
	if err := stores.DERPrograms.Create(ctx, edevID, derpID, prog); err != nil {
		t.Fatalf("seed program %s: %v", derpID, err)
	}
	scope := derhref.ScopeKey(edevID, fsaID, derpID)
	ctrl := sep2.DERControl{}
	ctrl.MRID = "CTRL-" + derpID
	ctrl.Href = base + "/derc/c-" + derpID
	if err := stores.DERControls.Create(ctx, scope, "c-"+derpID, ctrl); err != nil {
		t.Fatalf("seed control %s: %v", derpID, err)
	}
	if err := stores.DERControlLifecycles.Create(ctx, scope, "c-"+derpID, dercontrol.LifecycleRecord{CancelReason: "kept-" + derpID}); err != nil {
		t.Fatalf("seed lifecycle %s: %v", derpID, err)
	}
	dd := sep2.DefaultDERControl{MRID: "DD-" + derpID}
	dd.Href = base + "/dderc"
	if err := stores.DefaultDERControls.Create(ctx, scope, "default", dd); err != nil {
		t.Fatalf("seed default control %s: %v", derpID, err)
	}
}

// TestFSASwap_MovesProgramsWithTheirFSA pins that a swapped FSA keeps its
// programs (#916): each FSA lists only the programs whose href names it, so
// the programs, their controls, lifecycle records and default controls move
// to the new FSA id, and a program of another FSA is left exactly as it was.
func TestFSASwap_MovesProgramsWithTheirFSA(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	seedFSA(t, stores, "edev-1", "fsa-keep")
	seedProgramInFSA(t, stores, "edev-1", "fsa-old", "p1")
	seedProgramInFSA(t, stores, "edev-1", "fsa-keep", "p2")
	keptBefore, err := stores.DERPrograms.Get(ctx, "edev-1", "p2")
	if err != nil {
		t.Fatalf("read p2 before swap: %v", err)
	}

	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}

	const moved = "/edev/edev-1/fsa/fsa-new/derp/p1"
	p1, err := stores.DERPrograms.Get(ctx, "edev-1", "p1")
	if err != nil {
		t.Fatalf("p1 after swap: %v", err)
	}
	if p1.Href != moved || p1.MRID != "PROG-p1" || p1.Primacy != 3 {
		t.Errorf("p1 = href %q mRID %q primacy %d, want href %q, content kept", p1.Href, p1.MRID, p1.Primacy, moved)
	}
	if p1.DefaultDERControlLink == nil || p1.DefaultDERControlLink.Href != moved+"/dderc" {
		t.Errorf("p1 DefaultDERControlLink = %+v, want %q", p1.DefaultDERControlLink, moved+"/dderc")
	}
	if p1.DERControlListLink == nil || p1.DERControlListLink.Href != moved+"/derc" {
		t.Errorf("p1 DERControlListLink = %+v, want %q", p1.DERControlListLink, moved+"/derc")
	}
	if p1.ActiveDERControlListLink == nil || p1.ActiveDERControlListLink.Href != moved+"/actderc" {
		t.Errorf("p1 ActiveDERControlListLink = %+v, want %q", p1.ActiveDERControlListLink, moved+"/actderc")
	}
	if p1.DERCurveListLink == nil || p1.DERCurveListLink.Href != "/dc" || p1.DERCurveListLink.All != 2 {
		t.Errorf("p1 DERCurveListLink = %+v, want the global /dc link untouched", p1.DERCurveListLink)
	}

	const oldScope, newScope = "edev-1/fsa-old/p1", "edev-1/fsa-new/p1"
	ctrl, err := stores.DERControls.Get(ctx, newScope, "c-p1")
	if err != nil {
		t.Fatalf("control under new scope: %v", err)
	}
	if ctrl.Href != moved+"/derc/c-p1" || ctrl.MRID != "CTRL-p1" {
		t.Errorf("moved control = href %q mRID %q, want href %q mRID CTRL-p1", ctrl.Href, ctrl.MRID, moved+"/derc/c-p1")
	}
	if _, err := stores.DERControls.Get(ctx, oldScope, "c-p1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("control under old scope: err = %v, want ErrNotFound", err)
	}
	if _, _, _, err := stores.DERControls.ByMRID(ctx, "CTRL-p1"); err != nil {
		t.Errorf("ByMRID(CTRL-p1) after move: %v", err)
	}
	rec, err := stores.DERControlLifecycles.Get(ctx, newScope, "c-p1")
	if err != nil || rec.CancelReason != "kept-p1" {
		t.Errorf("lifecycle under new scope = %+v, %v; want CancelReason kept-p1", rec, err)
	}
	if _, err := stores.DERControlLifecycles.Get(ctx, oldScope, "c-p1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("lifecycle under old scope: err = %v, want ErrNotFound", err)
	}
	dd, err := stores.DefaultDERControls.Get(ctx, newScope, "default")
	if err != nil || dd.MRID != "DD-p1" || dd.Href != moved+"/dderc" {
		t.Errorf("default control under new scope = mRID %q href %q, %v; want DD-p1 at %q", dd.MRID, dd.Href, err, moved+"/dderc")
	}
	if _, err := stores.DefaultDERControls.Get(ctx, oldScope, "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default control under old scope: err = %v, want ErrNotFound", err)
	}

	keptAfter, err := stores.DERPrograms.Get(ctx, "edev-1", "p2")
	if err != nil {
		t.Fatalf("p2 after swap: %v", err)
	}
	if keptAfter.Href != keptBefore.Href || keptAfter.DERControlListLink.Href != keptBefore.DERControlListLink.Href {
		t.Errorf("p2 of another FSA changed: href %q, want %q", keptAfter.Href, keptBefore.Href)
	}
	if _, err := stores.DERControls.Get(ctx, "edev-1/fsa-keep/p2", "c-p2"); err != nil {
		t.Errorf("p2's control moved or lost: %v", err)
	}
}

// A control that cannot be re-created under the new scope stays where it
// was, with its lifecycle record, and the swap reports the failure.
func TestFSASwap_FailedControlMoveLeavesControlInPlace(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	seedProgramInFSA(t, stores, "edev-1", "fsa-old", "p1")
	occupant := sep2.DERControl{}
	occupant.MRID = "OCCUPANT"
	if err := stores.DERControls.Create(ctx, "edev-1/fsa-new/p1", "c-p1", occupant); err != nil {
		t.Fatalf("seed occupant: %v", err)
	}

	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
	}
	ctrl, err := stores.DERControls.Get(ctx, "edev-1/fsa-old/p1", "c-p1")
	if err != nil || ctrl.MRID != "CTRL-p1" || ctrl.Href != "/edev/edev-1/fsa/fsa-old/derp/p1/derc/c-p1" {
		t.Errorf("control at old scope = mRID %q href %q, %v; want CTRL-p1 left in place", ctrl.MRID, ctrl.Href, err)
	}
	if _, _, _, err := stores.DERControls.ByMRID(ctx, "CTRL-p1"); err != nil {
		t.Errorf("ByMRID(CTRL-p1): %v", err)
	}
	if rec, err := stores.DERControlLifecycles.Get(ctx, "edev-1/fsa-old/p1", "c-p1"); err != nil || rec.CancelReason != "kept-p1" {
		t.Errorf("lifecycle at old scope = %+v, %v; want it left in place", rec, err)
	}
}
