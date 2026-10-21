package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #972: the FSA control routes over the template and DER control fixtures.

type fsaControlFixture struct {
	dc  *dcHarness
	f   *templateFixture
	h   *handler.AdminFSAControlHandler
	tid string
}

func newFSAControlFixture(t *testing.T) *fsaControlFixture {
	t.Helper()
	d := newDCHarness(t, ptrU32(dcPEN))
	f := newTemplateFixtureWith(t, d.programs, d.devices)
	issuer := d.h.Issuer.(*dercontrol.Issuer)
	f.h.Retirer = issuer
	h := &handler.AdminFSAControlHandler{FSAs: f.h, Controls: d.h, Issuer: issuer}
	f.h.Replicas = h
	d.h.FSAControls = f.h.AdminFSAs
	f.mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls", h.HandleCreate())
	f.mux.HandleFunc("GET /api/fsas/{id}/der-programs/{derpId}/controls", h.HandleList())
	f.mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls/{controlId}/cancel", h.HandleCancel())
	tmpl := f.createTemplate(t)
	return &fsaControlFixture{dc: d, f: f, h: h, tid: tmpl.ID}
}

func (x *fsaControlFixture) path() string {
	return "/api/fsas/fsa-A/der-programs/" + x.tid + "/controls"
}

func fsaControlBody() string {
	return fmt.Sprintf(`{"type":"maxLimW","maxLimW":4000,"startTime":%d,"durationSeconds":600}`, futureStart(900))
}

// With no device assigned the record alone is written, and the first device
// assigned afterwards receives it.
func TestFSAControl_NoDeviceRecordsThenReplicatesOnAssign(t *testing.T) {
	x := newFSAControlFixture(t)
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var got handler.FSAControlCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Copies) != 0 || got.Cancelled || got.Type != "maxLimW" || got.Functions["opModMaxLimW"] != float64(4000) {
		t.Fatalf("reply = %+v, want an uncancelled maxLimW 4000 record with no copies", got.FSAControlView)
	}
	if c, _ := x.dc.storedCounts(t); c != 0 {
		t.Fatalf("stored %d controls with no device assigned", c)
	}

	x.f.assign(t, dcDevice, "fsa-A")
	list, err := x.dc.controls.List(context.Background(), dcDevice+"/fsa-A/"+x.tid, store.ListOptions{Unbounded: true})
	if err != nil || len(list.Items) != 1 || list.Items[0].MRID != got.MRID || list.Items[0].CreationTime != got.CreationTime {
		t.Fatalf("device copy after assign = %+v, %v; want mRID %s", list.Items, err, got.MRID)
	}
}

// An assignment that cannot carry the FSA's live controls is refused whole.
func TestFSAControl_AssignWithoutReplicatorRefusedWhole(t *testing.T) {
	x := newFSAControlFixture(t)
	if w := x.f.do(http.MethodPost, x.path(), fsaControlBody()); w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	x.f.h.Replicas = nil
	w := x.f.do(http.MethodPost, "/api/devices/"+dcDevice+"/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("assign without a replicator = %d %s, want 500", w.Code, w.Body.String())
	}
	ctx := context.Background()
	if slices.Contains(x.f.h.AdminFSAs.Devices(ctx, "fsa-A"), dcDevice) {
		t.Error("the refused assignment is recorded")
	}
	if _, err := x.dc.programs.Get(ctx, dcDevice, x.tid); err == nil {
		t.Error("the refused assignment left the template copy")
	}
}

func TestFSAControl_CreateRefusals(t *testing.T) {
	x := newFSAControlFixture(t)
	for _, tc := range []struct {
		name, path, body string
		status           int
	}{
		{"grant", x.path(), `{"type":"targetW","targetW":{"value":1},"durationSeconds":600,"executesGrant":"0000000000000000000000000000000A"}`, http.StatusBadRequest},
		{"program href", x.path(), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":600}`, http.StatusBadRequest},
		{"no type", x.path(), `{"durationSeconds":600}`, http.StatusBadRequest},
		{"no template", "/api/fsas/fsa-A/der-programs/derp-none/controls", fsaControlBody(), http.StatusNotFound},
	} {
		if w := x.f.do(http.MethodPost, tc.path, tc.body); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.status)
		}
	}
	if recs := x.f.h.AdminFSAs.FSAControls(context.Background(), "fsa-A", x.tid); len(recs) != 0 {
		t.Errorf("refusals wrote %d records", len(recs))
	}
}

func TestFSAControl_CancelRefusals(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	var got handler.FSAControlCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	if w := x.f.do(http.MethodPost, x.path()+"/nope/cancel", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("cancel of an unknown id = %d, want 404", w.Code)
	}
	if w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	if w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`); w.Code != http.StatusConflict {
		t.Errorf("second cancel = %d %s, want 409", w.Code, w.Body.String())
	}
	if !x.dc.controlCancelled(t, got.MRID) {
		t.Error("the device copy is not cancelled")
	}
}

// With one device the copy's mRID is not shared, so only the record says it
// is an FSA control; the device route must still refuse it.
func TestFSAControl_DeviceCancelOfALoneCopyRefused(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	var got handler.FSAControlCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	w = x.dc.do(t, http.MethodPost, x.dc.cancelPath(got.MRID), `{}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"fsa_control"`) ||
		!strings.Contains(w.Body.String(), x.path()+"/"+got.ID+"/cancel") {
		t.Fatalf("device cancel of a lone copy = %d %s, want 409 fsa_control naming the FSA route", w.Code, w.Body.String())
	}
	if x.dc.controlCancelled(t, got.MRID) {
		t.Error("the refused cancel cancelled the copy")
	}
}
