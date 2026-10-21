package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #922: edit and delete templates, device programs and fixture programs.

const peakShaveBody = `{"description":"peak shave","primacy":70,"defaultControl":{"type":"maxLimW","maxLimW":4000}}`

func templatePath(fsa, id string) string { return "/api/fsas/" + fsa + "/der-programs/" + id }
func devicePath(dev, id string) string   { return "/api/devices/" + dev + "/der-programs/" + id }

type codedError struct {
	Error    string   `json:"error"`
	Code     string   `json:"code"`
	Controls []string `json:"controls"`
}

func decodeCoded(t *testing.T, body []byte) codedError {
	t.Helper()
	var e codedError
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return e
}

func defaultOf(t *testing.T, f *templateFixture, dev, fsa, id string) sep2.DefaultDERControl {
	t.Helper()
	d, err := f.copies.Defaults.Get(context.Background(), derhref.ScopeKey(dev, fsa, id), coresingleton.SingletonKey)
	if err != nil {
		t.Fatalf("default of %s/%s/%s: %v", dev, fsa, id, err)
	}
	return d
}

// edited is what an edit of the program at href notifies: the program, its
// default control when that changed, and its FSA's list.
func edited(href string, withDefault bool) []recordedNotification {
	out := []recordedNotification{{href, sep2.NotificationStatusDefault}}
	if withDefault {
		out = append(out, recordedNotification{href + "/dderc", sep2.NotificationStatusDefault})
	}
	return append(out, recordedNotification{href[:strings.LastIndex(href, "/")], sep2.NotificationStatusDefault})
}

// deleted is what a delete of the program at href notifies.
func deleted(href string) []recordedNotification {
	return []recordedNotification{
		{href, sep2.NotificationStatusCanceledDeleted},
		{href + "/dderc", sep2.NotificationStatusCanceledDeleted},
		{href + "/derc", sep2.NotificationStatusCanceledDeleted},
		{href[:strings.LastIndex(href, "/")], sep2.NotificationStatusDefault},
	}
}

func versionNum(v *uint16) int {
	if v == nil {
		return -1
	}
	return int(*v)
}

func TestUpdateProgramTemplate_RaisesVersionsAndRewritesEveryCopy(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	before := f.createTemplate(t)
	f.notifier.take()

	w := f.do(http.MethodPut, templatePath("fsa-A", before.ID), peakShaveBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	var v templateView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != before.ID || v.MRID != before.MRID || v.Description != "peak shave" || v.Primacy != 70 || v.Version != 1 ||
		v.DefaultControl.MRID != before.DefaultControl.MRID || *v.DefaultControl.DERControlBase.OpModMaxLimW != 4000 ||
		!slices.Equal(v.Copies, []string{"/edev/1/fsa/fsa-A/derp/" + v.ID, "/edev/2/fsa/fsa-A/derp/" + v.ID}) {
		t.Errorf("view = %+v", v)
	}
	stored, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if err != nil || stored.Version != 1 || stored.DefaultVersion != 1 || stored.Description != "peak shave" || stored.Primacy != 70 {
		t.Errorf("stored template = %+v, %v", stored, err)
	}
	for _, dev := range []string{"1", "2"} {
		assertDeviceCopy(t, f.copies, dev, "fsa-A", v)
		if d := defaultOf(t, f, dev, "fsa-A", v.ID); versionNum(d.Version) != 1 {
			t.Errorf("device %s default version = %d, want 1", dev, versionNum(d.Version))
		}
	}
	var want []recordedNotification
	for _, dev := range []string{"1", "2"} {
		want = append(want, edited("/edev/"+dev+"/fsa/fsa-A/derp/"+v.ID, true)...)
	}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}

	// The program alone changing raises the program's version only.
	w = f.do(http.MethodPut, templatePath("fsa-A", v.ID), `{"description":"peak shave","primacy":71}`)
	if w.Code != http.StatusOK {
		t.Fatalf("second PUT: %d %s", w.Code, w.Body.String())
	}
	stored, _ = f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if stored.Version != 2 || stored.DefaultVersion != 1 || stored.Primacy != 71 || *stored.DefaultControlBase.OpModMaxLimW != 4000 {
		t.Errorf("after a program-only edit: %+v", stored)
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "2", v.ID); versionNum(p.Version) != 2 || p.Primacy != 71 {
		t.Errorf("device 2 copy = version %d primacy %d, want 2 and 71", versionNum(p.Version), p.Primacy)
	}
	want = append(edited("/edev/1/fsa/fsa-A/derp/"+v.ID, false), edited("/edev/2/fsa/fsa-A/derp/"+v.ID, false)...)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications after a program-only edit = %+v, want %+v", got, want)
	}
}

func TestUpdateProgramTemplate_UnchangedWritesNothing(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	f.notifier.take()
	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), voltSupportBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if stored, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID); stored.Version != 0 || stored.DefaultVersion != 0 {
		t.Errorf("versions = %d and %d, want 0 and 0", stored.Version, stored.DefaultVersion)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications = %+v, want none", got)
	}
}

func TestUpdateProgramTemplate_Refusals(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	for _, tc := range []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"unknown template", templatePath("fsa-A", "derp-0000000000000000"), peakShaveBody, http.StatusNotFound, ""},
		{"unknown fsa", templatePath("fsa-Z", v.ID), peakShaveBody, http.StatusNotFound, ""},
		{"bad primacy", templatePath("fsa-A", v.ID), `{"description":"x","primacy":3}`, http.StatusBadRequest, ""},
		{"unknown field", templatePath("fsa-A", v.ID), `{"description":"x","primacy":1,"version":9}`, http.StatusBadRequest, ""},
		{"changed mRID", templatePath("fsa-A", v.ID), `{"description":"x","primacy":1,"mRID":"0000000000000000000000000000A0B1"}`, http.StatusConflict, "mrid_immutable"},
	} {
		w := f.do(http.MethodPut, tc.path, tc.body)
		if w.Code != tc.status || (tc.code != "" && decodeCoded(t, w.Body.Bytes()).Code != tc.code) {
			t.Errorf("%s: %d %s, want %d %s", tc.name, w.Code, w.Body.String(), tc.status, tc.code)
		}
	}
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
}

func TestUpdateProgramTemplate_VersionExhausted409(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	v := f.createTemplate(t)
	tmpl, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	tmpl.Version = 65535
	if err := f.h.AdminFSAs.UpdateTemplate(context.Background(), "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), peakShaveBody)
	if w.Code != http.StatusConflict || decodeCoded(t, w.Body.Bytes()).Code != "version_exhausted" {
		t.Fatalf("PUT at 65535: %d %s, want 409 version_exhausted", w.Code, w.Body.String())
	}
	if after, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID); after.Description != "volt support" {
		t.Errorf("template written: %+v", after)
	}
}

func TestDeviceRoutes_RefuseATemplateCopy(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		w := f.do(method, devicePath("1", v.ID), peakShaveBody)
		if w.Code != http.StatusConflict || decodeCoded(t, w.Body.Bytes()).Code != "edit_the_template" {
			t.Errorf("%s on a copy: %d %s, want 409 edit_the_template", method, w.Code, w.Body.String())
		}
	}
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
}

func TestUpdateDeviceProgram_RaisesVersionsOnce(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	f.notifier.take()

	w := f.do(http.MethodPut, devicePath("1", created.ID), peakShaveBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	var v deviceProgramView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != created.ID || v.Href != created.Href || v.MRID != created.MRID || v.Description != "peak shave" || v.Primacy != 70 ||
		v.Version != 1 || v.DefaultControl.MRID != created.DefaultControl.MRID || *v.DefaultControl.DERControlBase.OpModMaxLimW != 4000 {
		t.Errorf("view = %+v", v)
	}
	p, err := f.copies.Programs.Get(context.Background(), "1", created.ID)
	if err != nil || versionNum(p.Version) != 1 || p.Description != "peak shave" || p.Primacy != 70 ||
		p.DERControlListLink.Href != created.Href+"/derc" || p.DefaultDERControlLink.Href != created.Href+"/dderc" {
		t.Errorf("stored program = %+v, %v", p, err)
	}
	if d := defaultOf(t, f, "1", "fsa-A", created.ID); versionNum(d.Version) != 1 || uint16(*d.DERControlBase.OpModMaxLimW) != 4000 || d.MRID != created.DefaultControl.MRID {
		t.Errorf("stored default = %+v", d)
	}
	if got, want := f.notifier.take(), edited(created.Href, true); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
	if o, _ := f.copies.Origins.Get(context.Background(), "1", created.ID); o.Kind != memory.ProgramOriginDevice {
		t.Errorf("origin = %+v, want it kept as a device program", o)
	}
}

// seedFixtureProgram stores a program the way the boot fixture does: no
// origin, no version, and a default only when withDefault.
func seedFixtureProgram(t *testing.T, f *templateFixture, dev, fsa, id string, withDefault bool) string {
	t.Helper()
	href := "/edev/" + dev + "/fsa/" + fsa + "/derp/" + id
	p := sep2.DERProgram{MRID: "0000000000000000000000000000F1C5", Description: "fixture", Primacy: 1,
		DERControlListLink: &sep2.ListLink{Href: href + "/derc"}}
	p.Href = href
	if err := f.copies.Programs.Create(context.Background(), dev, id, p); err != nil {
		t.Fatal(err)
	}
	if withDefault {
		d := sep2.DefaultDERControl{MRID: "0000000000000000000000000000D1C5", SetGradW: ptrU16(50)}
		d.Href = href + "/dderc"
		if err := f.copies.Defaults.Create(context.Background(), derhref.ScopeKey(dev, fsa, id), coresingleton.SingletonKey, d); err != nil {
			t.Fatal(err)
		}
	}
	return href
}

func ptrU16(v uint16) *uint16 { return &v }

func TestUpdateDeviceProgram_EditsAFixtureProgram(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	seedFixtureProgram(t, f, "1", "0", "0", true)
	seedFixtureProgram(t, f, "2", "0", "0", false)

	w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"edited","primacy":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	p, _ := f.copies.Programs.Get(context.Background(), "1", "0")
	if versionNum(p.Version) != 1 || p.Description != "edited" || p.Primacy != 2 || p.MRID != "0000000000000000000000000000F1C5" {
		t.Errorf("stored program = %+v", p)
	}
	if d := defaultOf(t, f, "1", "0", "0"); d.Version != nil || *d.SetGradW != 50 {
		t.Errorf("default = %+v, want it untouched", d)
	}
	if _, err := f.copies.Origins.Get(context.Background(), "1", "0"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("origin err = %v, want a fixture program to stay without one", err)
	}

	w = f.do(http.MethodPut, devicePath("1", "0"), `{"description":"edited","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT with a default: %d %s", w.Code, w.Body.String())
	}
	d := defaultOf(t, f, "1", "0", "0")
	if versionNum(d.Version) != 1 || d.DERControlBase == nil || uint16(*d.DERControlBase.OpModMaxLimW) != 3000 || d.MRID != "0000000000000000000000000000D1C5" {
		t.Errorf("default = %+v", d)
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "1", "0"); versionNum(p.Version) != 2 {
		t.Errorf("program version = %d, want 2", versionNum(p.Version))
	}

	// A program with no default takes one when a value is sent (#1043).
	w = f.do(http.MethodPut, devicePath("2", "0"), `{"description":"x","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("default set on a program with none: %d %s, want 200", w.Code, w.Body.String())
	}
	p2, _ := f.copies.Programs.Get(context.Background(), "2", "0")
	if p2.Description != "x" || versionNum(p2.Version) != 1 || p2.MRID != "0000000000000000000000000000F1C5" {
		t.Errorf("device 2 program = %+v", p2)
	}
	d2 := defaultOf(t, f, "2", "0", "0")
	if d2.DERControlBase == nil || uint16(*d2.DERControlBase.OpModMaxLimW) != 3000 || versionNum(d2.Version) != 1 || d2.MRID == "" || d2.MRID == p2.MRID {
		t.Errorf("device 2 default = %+v", d2)
	}
}

func TestUpdateDeviceProgram_NotFound(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	if w := f.do(http.MethodPut, devicePath("1", "nope"), peakShaveBody); w.Code != http.StatusNotFound {
		t.Errorf("unknown program: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPut, devicePath("9", "nope"), peakShaveBody); w.Code != http.StatusNotFound {
		t.Errorf("unknown device: %d %s", w.Code, w.Body.String())
	}
}

func TestDeleteDeviceProgram_RemovesEveryPartAndNotifies(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	fixtureHref := seedFixtureProgram(t, f, "1", "0", "0", true)
	f.notifier.take()

	if w := f.do(http.MethodDelete, devicePath("1", created.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE device program: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, devicePath("1", "0"), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE fixture program: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	for _, p := range []struct{ fsa, id string }{{"fsa-A", created.ID}, {"0", "0"}} {
		if _, err := f.copies.Programs.Get(ctx, "1", p.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("program %s: %v, want gone", p.id, err)
		}
		if _, err := f.copies.Defaults.Get(ctx, derhref.ScopeKey("1", p.fsa, p.id), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("default of %s: %v, want gone", p.id, err)
		}
		if _, err := f.copies.Origins.Get(ctx, "1", p.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("origin of %s: %v, want gone", p.id, err)
		}
	}
	want := append(deleted(created.Href), deleted(fixtureHref)...)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
	// With its device program gone, the device can be unassigned again.
	if w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Errorf("unassign after the delete: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, devicePath("1", "0"), ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE: %d, want 404", w.Code)
	}
}

func TestDeleteProgramTemplate_RemovesItsCopies(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	v := f.createTemplate(t)
	f.notifier.take()

	if w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("template: %v, want gone", err)
	}
	assertNoDeviceCopy(t, f.copies, "1")
	assertNoDeviceCopy(t, f.copies, "2")
	want := append(deleted("/edev/1/fsa/fsa-A/derp/"+v.ID), deleted("/edev/2/fsa/fsa-A/derp/"+v.ID)...)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
	if w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE: %d, want 404", w.Code)
	}
	// A device assigned later gets no copy of the deleted template.
	f.assign(t, "3", "fsa-A")
	assertNoDeviceCopy(t, f.copies, "3")
}

func TestDelete_WithNoRetirerFailsClosed(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	seedFixtureProgram(t, f, "1", "0", "0", false)
	f.h.Retirer = nil
	if w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), ""); w.Code != http.StatusInternalServerError {
		t.Errorf("template DELETE: %d, want 500", w.Code)
	}
	if w := f.do(http.MethodDelete, devicePath("1", "0"), ""); w.Code != http.StatusInternalServerError {
		t.Errorf("device DELETE: %d, want 500", w.Code)
	}
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
	if _, err := f.copies.Programs.Get(context.Background(), "1", "0"); err != nil {
		t.Errorf("fixture program: %v, want kept", err)
	}
}

// liveFixture shares one program store between the program routes and the
// DER control routes, with the control issuer as the program retirer.
func liveFixture(t *testing.T) (*templateFixture, *dcHarness) {
	t.Helper()
	d := newDCHarness(t, ptrU32(dcPEN))
	f := newTemplateFixtureWith(t, d.programs, d.devices)
	issuer, ok := d.h.Issuer.(*dercontrol.Issuer)
	if !ok {
		t.Fatalf("issuer is %T", d.h.Issuer)
	}
	f.h.Retirer = issuer
	return f, d
}

func issueOn(t *testing.T, d *dcHarness, programHref string) string {
	t.Helper()
	body := fmt.Sprintf(`{"derProgramHref":%q,"type":"maxLimW","maxLimW":4000,"startTime":%d,"durationSeconds":300}`, programHref, time.Now().Unix()+600)
	w := d.do(t, http.MethodPost, "/api/der/controls", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("control on %s: %d %s", programHref, w.Code, w.Body.String())
	}
	return decodeCreated(t, w).MRID
}

func assertLiveRefusal(t *testing.T, f *templateFixture, path string, controls []string) {
	t.Helper()
	w := f.do(http.MethodDelete, path, "")
	e := decodeCoded(t, w.Body.Bytes())
	if w.Code != http.StatusConflict || e.Code != "live_controls" || !slices.Equal(e.Controls, controls) {
		t.Fatalf("DELETE %s with a live control: %d %s, want 409 live_controls naming %v", path, w.Code, w.Body.String(), controls)
	}
}

func controlHref(t *testing.T, d *dcHarness, mrid string) (scope, id, href string) {
	t.Helper()
	scope, id, c, err := d.controls.ByMRID(context.Background(), mrid)
	if err != nil {
		t.Fatal(err)
	}
	return scope, id, c.Href
}

func assertControlKept(t *testing.T, d *dcHarness, mrid string) {
	t.Helper()
	scope, id, _ := controlHref(t, d, mrid)
	if lc, err := d.lifecycles.Get(context.Background(), scope, id); err != nil || lc.CancelledAt == nil {
		t.Errorf("lifecycle of %s = %+v, %v; want the cancelled record kept", mrid, lc, err)
	}
}

func TestDeleteDeviceProgram_RefusedWhileAControlIsLive(t *testing.T) {
	t.Parallel()
	f, d := liveFixture(t)
	if err := f.h.DeviceFSAs.Create(context.Background(), dcDeviceB, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	v := f.createDeviceProgram(t, dcDeviceB, "fsa-A", voltSupportBody)
	mrid := issueOn(t, d, v.Href)
	_, _, href := controlHref(t, d, mrid)
	f.notifier.take()

	assertLiveRefusal(t, f, devicePath(dcDeviceB, v.ID), []string{href})
	if _, err := f.copies.Programs.Get(context.Background(), dcDeviceB, v.ID); err != nil {
		t.Fatalf("program after the refusal: %v, want kept", err)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications after a refusal = %+v, want none", got)
	}

	if w := d.do(t, http.MethodPost, d.cancelPath(mrid), ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, devicePath(dcDeviceB, v.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE after cancel: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.copies.Programs.Get(context.Background(), dcDeviceB, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program after delete: %v, want gone", err)
	}
	assertControlKept(t, d, mrid)
}

func TestDeleteProgramTemplate_RefusedWhileAnyCopyHoldsALiveControl(t *testing.T) {
	t.Parallel()
	f, d := liveFixture(t)
	f.assign(t, dcDevice, "fsa-A")
	f.assign(t, dcDeviceB, "fsa-A")
	v := f.createTemplate(t)
	mrid := issueOn(t, d, "/edev/"+dcDeviceB+"/fsa/fsa-A/derp/"+v.ID)
	_, _, href := controlHref(t, d, mrid)

	assertLiveRefusal(t, f, templatePath("fsa-A", v.ID), []string{href})
	assertDeviceCopy(t, f.copies, dcDevice, "fsa-A", v)
	assertDeviceCopy(t, f.copies, dcDeviceB, "fsa-A", v)
	if _, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID); err != nil {
		t.Fatalf("template after the refusal: %v", err)
	}

	if w := d.do(t, http.MethodPost, d.cancelPath(mrid), ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE after cancel: %d %s", w.Code, w.Body.String())
	}
	for _, dev := range []string{dcDevice, dcDeviceB} {
		if _, err := f.copies.Programs.Get(context.Background(), dev, v.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("device %s copy: %v, want gone", dev, err)
		}
	}
	if _, err := d.programs.Get(context.Background(), dcDevice, "0"); err != nil {
		t.Errorf("device %s's own program: %v, want it untouched", dcDevice, err)
	}
	assertControlKept(t, d, mrid)
}

// A fixture program whose control list link names another FSA than its href
// has its controls under the link's scope; delete checks that one too.
func TestDeleteDeviceProgram_ChecksTheControlListLinkScope(t *testing.T) {
	t.Parallel()
	f, d := liveFixture(t)
	p := sep2.DERProgram{MRID: "0000000000000000000000000000F1C6", DERControlListLink: &sep2.ListLink{Href: "/edev/" + dcDeviceB + "/fsa/other/derp/9/derc"}}
	p.Href = "/edev/" + dcDeviceB + "/fsa/0/derp/9"
	if err := d.programs.Create(context.Background(), dcDeviceB, "9", p); err != nil {
		t.Fatal(err)
	}
	mrid := issueOn(t, d, p.Href)
	_, _, href := controlHref(t, d, mrid)
	assertLiveRefusal(t, f, devicePath(dcDeviceB, "9"), []string{href})
}
