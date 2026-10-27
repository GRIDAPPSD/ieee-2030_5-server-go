package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1155: where a control came from.

func originRequest(origin string) dercontrol.CreateRequest {
	pct := uint16(4000)
	return dercontrol.CreateRequest{Type: dercontrol.MaxLimW, MaxLimW: &pct, DurationSeconds: 600, Origin: origin}
}

func (x *fsaControlFixture) listFSA(t *testing.T) handler.FSAControlList {
	t.Helper()
	w := x.f.do(http.MethodGet, x.path(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("fsa list = %d %s", w.Code, w.Body.String())
	}
	var l handler.FSAControlList
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func (x *fsaControlFixture) listDevice(t *testing.T) handler.DERControlList {
	t.Helper()
	w := x.dc.do(t, http.MethodGet, "/api/der/controls?device="+dcDevice, "")
	if w.Code != http.StatusOK {
		t.Fatalf("device list = %d %s", w.Code, w.Body.String())
	}
	var l handler.DERControlList
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// A control issued with the derms origin reads derms in the FSA list and in
// the device list, and an operator's control beside it reads admin in both.
func TestControlOrigin_ListsReturnIt(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	ctx := context.Background()
	if _, err := x.h.IssueAcrossForTest(ctx, originRequest(dercontrol.OriginDERMS), []string{dcDevice}, "fsa-A", x.tid); err != nil {
		t.Fatal(err)
	}
	if w := x.f.do(http.MethodPost, x.path(), fsaControlBody()); w.Code != http.StatusCreated {
		t.Fatalf("operator create = %d %s", w.Code, w.Body.String())
	}

	fsa := x.listFSA(t)
	if len(fsa.Controls) != 2 {
		t.Fatalf("fsa list holds %d controls, want 2", len(fsa.Controls))
	}
	got := map[string]string{}
	for _, c := range fsa.Controls {
		got[c.MRID] = c.Origin
	}
	dev := x.listDevice(t)
	if len(dev.Controls) != 2 {
		t.Fatalf("device list holds %d controls, want 2", len(dev.Controls))
	}
	counts := map[string]int{}
	for _, c := range dev.Controls {
		if got[c.MRID] != c.Origin {
			t.Errorf("control %s: device list origin %q, fsa list %q", c.MRID, c.Origin, got[c.MRID])
		}
		counts[c.Origin]++
	}
	if counts["derms"] != 1 || counts["admin"] != 1 {
		t.Errorf("device list origins = %v, want one derms and one admin", counts)
	}
	// The stored values: derms is kept, admin is stored as nothing.
	recs := x.f.h.AdminFSAs.FSAControls(ctx, "fsa-A", x.tid)
	stored := map[string]int{}
	for _, r := range recs {
		stored[r.Origin]++
	}
	if stored[dercontrol.OriginDERMS] != 1 || stored[""] != 1 || len(recs) != 2 {
		t.Errorf("stored FSA record origins = %v, want one derms and one empty", stored)
	}
}

// Cancelling a derms control keeps its origin on the record and on every copy.
func TestControlOrigin_SurvivesCancel(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	res, err := x.h.IssueAcrossForTest(context.Background(), originRequest(dercontrol.OriginDERMS), []string{dcDevice}, "fsa-A", x.tid)
	if err != nil {
		t.Fatal(err)
	}
	w := x.f.do(http.MethodPost, x.path()+"/"+res.ID+"/cancel", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	var view handler.FSAControlView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Cancelled || view.Origin != "derms" {
		t.Errorf("cancelled view cancelled=%v origin=%q, want true derms", view.Cancelled, view.Origin)
	}
	dev := x.listDevice(t)
	if len(dev.Controls) != 1 || dev.Controls[0].Origin != "derms" || dev.Controls[0].EventStatus.Status != "cancelled" {
		t.Errorf("device list after cancel = %+v, want one cancelled derms control", dev.Controls)
	}
}

// A device assigned after a derms control was issued gets a copy that reads
// derms.
func TestControlOrigin_ReplicatedCopyKeepsIt(t *testing.T) {
	x := newFSAControlFixture(t)
	if _, err := x.h.IssueAcrossForTest(context.Background(), originRequest(dercontrol.OriginDERMS), nil, "fsa-A", x.tid); err != nil {
		t.Fatal(err)
	}
	x.f.assign(t, dcDevice, "fsa-A")
	dev := x.listDevice(t)
	if len(dev.Controls) != 1 || dev.Controls[0].Origin != "derms" {
		t.Fatalf("replicated copy = %+v, want one derms control", dev.Controls)
	}
}

// Origin is server-set: a create body carrying it is refused on both create
// routes, and nothing is written.
func TestControlOrigin_CreateBodyRefused(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	for _, origin := range []string{"derms", "admin", ""} {
		body := `{"type":"maxLimW","maxLimW":4000,"durationSeconds":600,"origin":"` + origin + `"}`
		dev := `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":4000,"durationSeconds":600,"origin":"` + origin + `"}`
		if w := x.f.do(http.MethodPost, x.path(), body); w.Code != http.StatusBadRequest {
			t.Errorf("fsa create with origin %q = %d %s, want 400", origin, w.Code, w.Body.String())
		}
		if w := x.dc.do(t, http.MethodPost, "/api/der/controls", dev); w.Code != http.StatusBadRequest {
			t.Errorf("device create with origin %q = %d %s, want 400", origin, w.Code, w.Body.String())
		}
	}
	if recs := x.f.h.AdminFSAs.FSAControls(context.Background(), "fsa-A", x.tid); len(recs) != 0 {
		t.Errorf("refused creates wrote %d FSA records", len(recs))
	}
	// Control: the same bodies without origin are accepted.
	if w := x.f.do(http.MethodPost, x.path(), fsaControlBody()); w.Code != http.StatusCreated {
		t.Errorf("control create = %d %s, want 201", w.Code, w.Body.String())
	}
}

// An origin that is neither admin nor derms is refused before anything is
// written, by IssueAcross and by the single-device path.
func TestControlOrigin_InvalidRefusedWritesNothing(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	if _, err := x.h.IssueAcrossForTest(context.Background(), originRequest("bogus"), []string{dcDevice}, "fsa-A", x.tid); err != dercontrol.ErrInvalidOrigin {
		t.Errorf("IssueAcross with origin bogus = %v, want ErrInvalidOrigin", err)
	}
	issuer := x.dc.h.Issuer.(*dercontrol.Issuer)
	req := originRequest(dercontrol.OriginDERMS)
	req.DERProgramHref = "/edev/0/fsa/0/derp/0"
	if _, err := issuer.Issue(context.Background(), req); err != dercontrol.ErrInvalidOrigin {
		t.Errorf("Issue with origin derms = %v, want ErrInvalidOrigin: a single-device issue cannot record it", err)
	}
	if recs := x.f.h.AdminFSAs.FSAControls(context.Background(), "fsa-A", x.tid); len(recs) != 0 {
		t.Errorf("refusals left %d FSA records", len(recs))
	}
	if c, l := x.dc.storedCounts(t); c != 0 || l != 0 {
		t.Errorf("refusals left %d controls and %d lifecycle records", c, l)
	}
}

// A lifecycle file or FSA record written before the field loads unchanged and
// reads admin; a record without an origin serializes without the key, so its
// snapshot is byte-identical to one written before the field existed.
func TestControlOrigin_OldRecordsReadAdmin(t *testing.T) {
	oldLC := `{"CancelledAt":null,"CancelReason":"","SupersededAt":null,"SupersededBy":""}`
	var lc dercontrol.LifecycleRecord
	if err := json.Unmarshal([]byte(oldLC), &lc); err != nil {
		t.Fatal(err)
	}
	if lc.Origin != "" || dercontrol.OriginOf(lc.Origin) != "admin" {
		t.Errorf("old lifecycle origin %q reads %q, want empty reading admin", lc.Origin, dercontrol.OriginOf(lc.Origin))
	}
	b, err := json.Marshal(lc)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != oldLC {
		t.Errorf("old lifecycle record re-serializes as %s, want %s", b, oldLC)
	}
	derms, _ := json.Marshal(dercontrol.LifecycleRecord{Origin: dercontrol.OriginDERMS})
	var back dercontrol.LifecycleRecord
	if err := json.Unmarshal(derms, &back); err != nil || back.Origin != "derms" {
		t.Errorf("derms record round trip = %+v, %v", back, err)
	}

	var rec memory.FSAControl
	if err := json.Unmarshal([]byte(`{"templateId":"t","id":"i","mRID":"M","creationTime":1,"start":2,"duration":3,"derControlBase":{}}`), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Origin != "" {
		t.Errorf("old FSA record origin = %q, want empty", rec.Origin)
	}
	out, _ := json.Marshal(rec)
	if strings.Contains(string(out), "origin") {
		t.Errorf("FSA record without origin serializes it: %s", out)
	}
	rec.Origin = "derms"
	out, _ = json.Marshal(rec)
	if !strings.Contains(string(out), `"origin":"derms"`) {
		t.Errorf("derms FSA record = %s, want origin derms", out)
	}
}

// The views name the flow of a power target: positive discharges, negative
// charges, and a control with no target has none. The device list and the FSA
// list agree.
func TestControlDirection_ListsNameTheFlow(t *testing.T) {
	x := newFSAControlFixture(t)
	x.f.assign(t, dcDevice, "fsa-A")
	ctx := context.Background()
	up := originRequest(dercontrol.OriginDERMS)
	up.Type, up.MaxLimW = dercontrol.TargetW, nil
	up.TargetW = &sep2.ActivePower{Value: 5000}
	if _, err := x.h.IssueAcrossForTest(ctx, up, []string{dcDevice}, "fsa-A", x.tid); err != nil {
		t.Fatal(err)
	}
	down := `{"type":"targetW","targetW":{"value":-3000},"durationSeconds":600}`
	if w := x.f.do(http.MethodPost, x.path(), down); w.Code != http.StatusCreated {
		t.Fatalf("charge create = %d %s", w.Code, w.Body.String())
	}
	if w := x.f.do(http.MethodPost, x.path(), `{"type":"targetW","targetW":{"value":0},"durationSeconds":600}`); w.Code != http.StatusCreated {
		t.Fatalf("zero create = %d %s", w.Code, w.Body.String())
	}
	if w := x.f.do(http.MethodPost, x.path(), fsaControlBody()); w.Code != http.StatusCreated {
		t.Fatalf("maxLimW create = %d %s", w.Code, w.Body.String())
	}
	word := func(p *string) string {
		if p == nil {
			return "none"
		}
		return *p
	}
	fsa := map[string]string{}
	for _, c := range x.listFSA(t).Controls {
		fsa[c.MRID] = word(c.Direction)
	}
	counts := map[string]int{}
	for _, c := range x.listDevice(t).Controls {
		d := word(c.Direction)
		if fsa[c.MRID] != d {
			t.Errorf("control %s: device list direction %q, fsa list %q", c.MRID, d, fsa[c.MRID])
		}
		counts[d]++
	}
	if counts["discharge"] != 1 || counts["charge"] != 1 || counts["none"] != 2 || len(fsa) != 4 {
		t.Errorf("directions = %v over %d controls, want 1 discharge, 1 charge, 2 none over 4", counts, len(fsa))
	}
}

// A stored origin this build does not know reads as admin and is never served
// verbatim.
func TestOriginOf_UnknownReadsAdmin(t *testing.T) {
	for stored, want := range map[string]string{"": "admin", "admin": "admin", "derms": "derms", "bogus": "admin", "DERMS": "admin"} {
		if got := dercontrol.OriginOf(stored); got != want {
			t.Errorf("OriginOf(%q) = %q, want %q", stored, got, want)
		}
	}
}
