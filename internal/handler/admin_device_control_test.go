package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #970: POST /api/devices/{id}/controls reaches one device through a
// device-only FSA.

type deviceControlFixture struct {
	dc  *dcHarness
	f   *templateFixture
	mux *http.ServeMux
}

func newDeviceControlFixture(t *testing.T) *deviceControlFixture {
	t.Helper()
	d := newDCHarness(t, ptrU32(dcPEN))
	f := newTemplateFixtureWith(t, d.programs, d.devices)
	return wireDeviceControl(d, f)
}

func wireDeviceControl(d *dcHarness, f *templateFixture) *deviceControlFixture {
	dh := &handler.AdminDeviceControlHandler{FSAs: f.h, Controls: d.h}
	f.mux.HandleFunc("POST /api/devices/{id}/controls", dh.HandleCreate())
	f.mux.HandleFunc("GET /api/devices/{id}/default-control", dh.HandleGetDefault())
	return &deviceControlFixture{dc: d, f: f, mux: f.mux}
}

func (x *deviceControlFixture) post(dev, body string) *httptest.ResponseRecorder {
	return x.f.do(http.MethodPost, "/api/devices/"+dev+"/controls", body)
}

func scheduledBody(t *testing.T, maxLimW int) string {
	t.Helper()
	return fmt.Sprintf(`{"kind":"scheduled","type":"maxLimW","maxLimW":%d,"startTime":%d,"durationSeconds":600}`, maxLimW, futureStart(900))
}

func defaultBody(maxLimW int) string {
	return fmt.Sprintf(`{"kind":"default","type":"maxLimW","maxLimW":%d}`, maxLimW)
}

type deviceControlReply struct {
	FSA           string `json:"fsa"`
	Program       string `json:"program"`
	ProgramHref   string `json:"programHref"`
	Primacy       uint8  `json:"primacy"`
	PrimacyReason string `json:"primacyReason"`
	Control       *struct {
		MRID           string `json:"mRID"`
		Href           string `json:"href"`
		DERProgramHref string `json:"derProgramHref"`
		DERControlBase struct {
			OpModMaxLimW *uint16 `json:"opModMaxLimW"`
		} `json:"derControlBase"`
	} `json:"control"`
	DefaultControl *struct {
		Type           string `json:"type"`
		DERControlBase struct {
			OpModMaxLimW *uint16 `json:"opModMaxLimW"`
		} `json:"derControlBase"`
	} `json:"defaultControl"`
}

func decodeDeviceControl(t *testing.T, w *httptest.ResponseRecorder) deviceControlReply {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
	}
	var got deviceControlReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return got
}

func decodeCode(t *testing.T, w *httptest.ResponseRecorder, status int) string {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d body = %s, want %d", w.Code, w.Body.String(), status)
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return b.Code
}

// nothingWritten fails when a refused request left an admin FSA, a device
// program or a control behind.
func (x *deviceControlFixture) nothingWritten(t *testing.T, dev string) {
	t.Helper()
	ctx := context.Background()
	if _, ok := x.f.h.AdminFSAs.DeviceOnlyFSA(dev); ok {
		t.Errorf("a refused request created a device-only FSA for device %s", dev)
	}
	list, err := x.f.copies.Programs.List(ctx, dev, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		if strings.Contains(p.Href, "/derp/derp-") {
			t.Errorf("a refused request created program %s", p.Href)
		}
	}
	if c, _ := x.dc.storedCounts(t); c != 0 {
		t.Errorf("a refused request stored %d controls", c)
	}
}

func TestDeviceControl_Scheduled_CreatesAServedDeviceOnlyProgram(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))

	fsaID, ok := x.f.h.AdminFSAs.DeviceOnlyFSA("1")
	if !ok || got.FSA != fsaID {
		t.Fatalf("reply fsa = %q, store's device-only FSA = %q, %v", got.FSA, fsaID, ok)
	}
	fsa, err := x.f.h.AdminFSAs.Get(ctx, fsaID)
	if err != nil || fsa.Description != "device-only 1" || fsa.MRID != fsaID {
		t.Errorf("fsa = %+v, %v, want description %q", fsa, err, "device-only 1")
	}
	if devs := x.f.h.AdminFSAs.Devices(ctx, fsaID); len(devs) != 1 || devs[0] != "1" {
		t.Errorf("devices assigned = %v, want [1]", devs)
	}
	if _, err := x.f.h.DeviceFSAs.Get(ctx, "1", fsaID); err != nil {
		t.Errorf("the device's own FSA store lacks %s: %v", fsaID, err)
	}

	href := "/edev/1/fsa/" + fsaID + "/derp/" + got.Program
	if got.ProgramHref != href || !templateIDPattern.MatchString(got.Program) {
		t.Errorf("program %q href %q, want a derp- id and href %s", got.Program, got.ProgramHref, href)
	}
	p, err := x.f.copies.Programs.Get(ctx, "1", got.Program)
	if err != nil {
		t.Fatal(err)
	}
	if p.Href != href || p.Primacy != 191 || got.Primacy != 191 ||
		p.DERControlListLink == nil || p.DERControlListLink.Href != href+"/derc" {
		t.Errorf("stored program = %+v, reply primacy %d, want primacy 191", p, got.Primacy)
	}
	if !strings.Contains(got.PrimacyReason, "191") {
		t.Errorf("primacyReason = %q, want it to name 191", got.PrimacyReason)
	}
	o, err := x.f.copies.Origins.Get(ctx, "1", got.Program)
	if err != nil || o.Kind != memory.ProgramOriginDevice || o.FSAID != fsaID {
		t.Errorf("origin = %+v, %v, want a device origin under %s", o, err, fsaID)
	}
	if _, err := x.f.copies.Defaults.Get(ctx, derhref.ScopeKey("1", fsaID, got.Program), coresingleton.SingletonKey); err != nil {
		t.Errorf("the program has no default control: %v", err)
	}

	if got.Control == nil || got.Control.DERProgramHref != href || got.Control.DERControlBase.OpModMaxLimW == nil || *got.Control.DERControlBase.OpModMaxLimW != 5000 {
		t.Fatalf("reply control = %+v, want a maxLimW 5000 control under %s", got.Control, href)
	}
	_, _, stored, err := x.dc.controls.ByMRID(ctx, got.Control.MRID)
	if err != nil || stored.DERControlBase == nil || stored.DERControlBase.OpModMaxLimW == nil || uint16(*stored.DERControlBase.OpModMaxLimW) != 5000 {
		t.Errorf("stored control = %+v, %v", stored, err)
	}
	page, err := x.dc.controls.List(ctx, derhref.ScopeKey("1", fsaID, got.Program), store.ListOptions{Unbounded: true})
	if err != nil || len(page.Items) != 1 {
		t.Errorf("controls in the program's scope = %d, %v, want 1", len(page.Items), err)
	}
}

func TestDeviceControl_TwoCallsReuseOneFSAAndProgram(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	first := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	second := decodeDeviceControl(t, x.post("1", scheduledBody(t, 3000)))

	if first.FSA != second.FSA || first.Program != second.Program || first.Primacy != second.Primacy {
		t.Errorf("second call = fsa %s program %s primacy %d, first = %s %s %d, want the same", second.FSA, second.Program, second.Primacy, first.FSA, first.Program, first.Primacy)
	}
	if first.Control.MRID == second.Control.MRID {
		t.Errorf("two calls returned one control %s", first.Control.MRID)
	}
	if n := len(x.f.h.AdminFSAs.List(ctx)); n != 2 {
		t.Errorf("%d admin FSAs, want 2: the fixture's fsa-A and one device-only FSA", n)
	}
	if ids := programIDsInFSA(t, x.f, "1", first.FSA); len(ids) != 1 || ids[0] != first.Program {
		t.Errorf("programs under the FSA = %v, want [%s]", ids, first.Program)
	}
	p, err := x.f.copies.Programs.Get(ctx, "1", first.Program)
	if err != nil || p.Version == nil || *p.Version != 0 {
		t.Errorf("program after a kept primacy = %+v, %v, want version 0", p, err)
	}
	if !strings.Contains(second.PrimacyReason, "kept") {
		t.Errorf("primacyReason = %q, want it to say the primacy was kept", second.PrimacyReason)
	}
	page, err := x.dc.controls.List(ctx, derhref.ScopeKey("1", first.FSA, first.Program), store.ListOptions{Unbounded: true})
	if err != nil || len(page.Items) != 2 {
		t.Errorf("controls in the scope = %d, %v, want 2", len(page.Items), err)
	}
}

// Every case seeds one other program on device 1 at primacy other, and the
// device-only program must come out strictly lower and unused.
func TestDeviceControl_PrimacyOutranksTheDevicesOtherPrograms(t *testing.T) {
	cases := []struct {
		name    string
		other   uint8
		want    uint8
		inRange bool
	}{
		{"above the user range", 200, 191, true},
		{"top of the user range", 191, 190, true},
		{"just above the floor", 66, 65, true},
		{"at the floor of the user range", 65, 2, false},
		{"standard primacy 2", 2, 1, false},
		{"standard primacy 1", 1, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newDeviceControlFixture(t)
			x.dc.seedProgram(t, "1", "0", "other", "OTHER-PROGRAM", tc.other, true)
			got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
			if got.Primacy != tc.want {
				t.Fatalf("primacy = %d, want %d (other program at %d)", got.Primacy, tc.want, tc.other)
			}
			p, err := x.f.copies.Programs.Get(context.Background(), "1", got.Program)
			if err != nil || p.Primacy != tc.want || p.Primacy >= tc.other {
				t.Errorf("stored primacy = %d, %v, want %d below %d", p.Primacy, err, tc.want, tc.other)
			}
			if outside := strings.Contains(got.PrimacyReason, "outside"); outside == tc.inRange {
				t.Errorf("primacyReason = %q, outside-the-user-range mention = %v, want %v", got.PrimacyReason, outside, !tc.inRange)
			}
		})
	}
}

func TestDeviceControl_NoPrimacyOutranksProgramZero_409_WritesNothing(t *testing.T) {
	x := newDeviceControlFixture(t)
	x.dc.seedProgram(t, "1", "0", "zero", "ZERO-PROGRAM", 0, true)
	w := x.post("1", scheduledBody(t, 5000))
	if code := decodeCode(t, w, http.StatusConflict); code != "primacy_cannot_outrank" {
		t.Errorf("code = %q, want primacy_cannot_outrank", code)
	}
	if !strings.Contains(w.Body.String(), "/edev/1/fsa/0/derp/zero") {
		t.Errorf("body does not name the program at 0: %s", w.Body.String())
	}
	x.nothingWritten(t, "1")
}

func TestDeviceControl_ReuseRepicksAPrimacyAnotherProgramNowBeats(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	first := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	if first.Primacy != 191 {
		t.Fatalf("first primacy = %d, want 191", first.Primacy)
	}
	x.dc.seedProgram(t, "1", "0", "late", "LATE-PROGRAM", 100, true)
	x.f.notifier.take()

	second := decodeDeviceControl(t, x.post("1", scheduledBody(t, 3000)))
	if second.Program != first.Program || second.Primacy != 99 {
		t.Fatalf("second = program %s primacy %d, want program %s primacy 99", second.Program, second.Primacy, first.Program)
	}
	p, err := x.f.copies.Programs.Get(ctx, "1", first.Program)
	if err != nil || p.Primacy != 99 || p.Version == nil || *p.Version != 1 {
		t.Errorf("stored program = %+v, %v, want primacy 99 at version 1", p, err)
	}
	if !strings.Contains(second.PrimacyReason, "re-picked") {
		t.Errorf("primacyReason = %q, want re-picked", second.PrimacyReason)
	}
	found := false
	for _, n := range x.f.notifier.take() {
		if n.href == "/edev/1/fsa/"+first.FSA+"/derp/"+first.Program {
			found = true
		}
	}
	if !found {
		t.Error("the program edit was not notified")
	}
}

func TestDeviceControl_Default_SetsTheProgramDefault(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	first := decodeDeviceControl(t, x.post("1", defaultBody(4000)))
	if first.Control != nil || first.DefaultControl == nil || first.DefaultControl.Type != "maxLimW" ||
		first.DefaultControl.DERControlBase.OpModMaxLimW == nil || *first.DefaultControl.DERControlBase.OpModMaxLimW != 4000 {
		t.Fatalf("reply = %+v, want only a maxLimW 4000 default control", first)
	}
	scope := derhref.ScopeKey("1", first.FSA, first.Program)
	d, err := x.f.copies.Defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if err != nil || d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || uint16(*d.DERControlBase.OpModMaxLimW) != 4000 {
		t.Fatalf("stored default = %+v, %v, want maxLimW 4000", d, err)
	}
	if c, _ := x.dc.storedCounts(t); c != 0 {
		t.Errorf("a default stored %d controls", c)
	}

	second := decodeDeviceControl(t, x.post("1", defaultBody(2500)))
	if second.FSA != first.FSA || second.Program != first.Program {
		t.Errorf("second default went to %s/%s, want %s/%s", second.FSA, second.Program, first.FSA, first.Program)
	}
	d, err = x.f.copies.Defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if err != nil || uint16(*d.DERControlBase.OpModMaxLimW) != 2500 || d.Version == nil || *d.Version != 1 {
		t.Errorf("default after the second call = %+v, %v, want 2500 at version 1", d, err)
	}
	p, err := x.f.copies.Programs.Get(ctx, "1", first.Program)
	if err != nil || p.Version == nil || *p.Version != 1 {
		t.Errorf("program after a default edit = %+v, %v, want version 1", p, err)
	}
}

func TestDeviceControl_ScheduledOnANewProgramLeavesAnEmptyDefault(t *testing.T) {
	x := newDeviceControlFixture(t)
	got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	d, err := x.f.copies.Defaults.Get(context.Background(), derhref.ScopeKey("1", got.FSA, got.Program), coresingleton.SingletonKey)
	if err != nil || d.DERControlBase == nil {
		t.Fatalf("default = %+v, %v, want a stored default with an empty DERControlBase", d, err)
	}
	if base := *d.DERControlBase; base.OpModMaxLimW != nil || base.OpModTargetW != nil || base.OpModConnect != nil {
		t.Errorf("a scheduled control set default values: %+v", base)
	}
}

func TestDeviceControl_Refusals_WriteNothing(t *testing.T) {
	cases := []struct {
		name   string
		dev    string
		body   string
		status int
	}{
		{"kind absent", "1", `{"type":"maxLimW","maxLimW":5000,"durationSeconds":600}`, 400},
		{"kind unknown", "1", `{"kind":"weekly","type":"maxLimW","maxLimW":5000,"durationSeconds":600}`, 400},
		{"program href is not accepted", "1", `{"kind":"scheduled","derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":5000,"durationSeconds":600}`, 400},
		{"scheduled without a duration", "1", `{"kind":"scheduled","type":"maxLimW","maxLimW":5000}`, 400},
		{"scheduled with a bad value", "1", `{"kind":"scheduled","type":"maxLimW","maxLimW":99999,"durationSeconds":600}`, 400},
		{"default with a duration", "1", `{"kind":"default","type":"maxLimW","maxLimW":5000,"durationSeconds":600}`, 400},
		{"default with a start", "1", `{"kind":"default","type":"maxLimW","maxLimW":5000,"startTime":1}`, 400},
		{"default with a bad value", "1", `{"kind":"default","type":"maxLimW","maxLimW":99999}`, 400},
		{"default with no value", "1", `{"kind":"default"}`, 400},
		{"unknown device", "nope", `{"kind":"default","type":"maxLimW","maxLimW":5000}`, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newDeviceControlFixture(t)
			if w := x.post(tc.dev, tc.body); w.Code != tc.status {
				t.Fatalf("status = %d body = %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			x.nothingWritten(t, tc.dev)
			if n := len(x.f.h.AdminFSAs.List(context.Background())); n != 1 {
				t.Errorf("%d admin FSAs, want only the fixture's", n)
			}
		})
	}
}

func TestDeviceControl_AnotherDeviceCannotJoinTheFSA_409(t *testing.T) {
	x := newDeviceControlFixture(t)
	got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	w := x.f.do(http.MethodPost, "/api/devices/0/fsa-assignment", `{"fsaHref":"/api/fsas/`+got.FSA+`"}`)
	if code := decodeCode(t, w, http.StatusConflict); code != "fsa_device_only" {
		t.Errorf("code = %q, want fsa_device_only", code)
	}
	if devs := x.f.h.AdminFSAs.Devices(context.Background(), got.FSA); len(devs) != 1 || devs[0] != "1" {
		t.Errorf("devices after the refused assignment = %v, want [1]", devs)
	}
	if _, err := x.f.h.DeviceFSAs.Get(context.Background(), "0", got.FSA); err == nil {
		t.Error("the refused device got the FSA in its own store")
	}
}

func TestDeviceControl_NoTemplateOnADeviceOnlyFSA_409(t *testing.T) {
	x := newDeviceControlFixture(t)
	got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	w := x.f.do(http.MethodPost, "/api/fsas/"+got.FSA+"/der-programs", voltSupportBody)
	if code := decodeCode(t, w, http.StatusConflict); code != "fsa_device_only" {
		t.Errorf("code = %q, want fsa_device_only", code)
	}
	if n := len(x.f.h.AdminFSAs.Templates(context.Background(), got.FSA)); n != 0 {
		t.Errorf("%d templates stored on a device-only FSA", n)
	}
}

// The restart case: every store the path writes is persisted under one data
// dir, rebuilt from its file, and the next call must find the same FSA and
// program instead of making a second pair.
func TestDeviceControl_SurvivesARestartWithADataDir(t *testing.T) {
	dir := t.TempDir()
	build := func() *deviceControlFixture {
		t.Helper()
		programs, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dir, "programs.json"))
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](filepath.Join(dir, "defaults.json"), "DefaultDERControl")
		if err != nil {
			t.Fatal(err)
		}
		origins, err := memory.NewProgramOriginStore(filepath.Join(dir, "origins.json"))
		if err != nil {
			t.Fatal(err)
		}
		admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
		if err != nil {
			t.Fatal(err)
		}
		d := newDCHarnessWith(t, ptrU32(dcPEN), programs)
		f := newTemplateFixtureWith(t, programs, d.devices)
		f.h.AdminFSAs = admin
		f.h.Copies = &fsaprogram.Copies{Programs: programs, Defaults: defaults, Origins: origins}
		f.copies = *f.h.Copies
		return wireDeviceControl(d, f)
	}

	x := build()
	first := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))

	y := build()
	ctx := context.Background()
	if id, ok := y.f.h.AdminFSAs.DeviceOnlyFSA("1"); !ok || id != first.FSA {
		t.Fatalf("device-only FSA after restart = %q, %v, want %s", id, ok, first.FSA)
	}
	p, err := y.f.copies.Programs.Get(ctx, "1", first.Program)
	if err != nil || p.Primacy != first.Primacy || p.Href != first.ProgramHref {
		t.Fatalf("program after restart = %+v, %v, want primacy %d at %s", p, err, first.Primacy, first.ProgramHref)
	}
	second := decodeDeviceControl(t, y.post("1", scheduledBody(t, 3000)))
	if second.FSA != first.FSA || second.Program != first.Program {
		t.Errorf("after restart the path used %s/%s, want the same %s/%s", second.FSA, second.Program, first.FSA, first.Program)
	}
	if n := len(y.f.h.AdminFSAs.List(ctx)); n != 1 {
		t.Errorf("%d admin FSAs after restart, want only the device-only one", n)
	}
	if _, err := y.f.h.DeviceFSAs.Get(ctx, "1", first.FSA); err != nil {
		t.Errorf("the device's own FSA store was not completed after restart: %v", err)
	}
	w := y.f.do(http.MethodPost, "/api/devices/0/fsa-assignment", `{"fsaHref":"/api/fsas/`+first.FSA+`"}`)
	if code := decodeCode(t, w, http.StatusConflict); code != "fsa_device_only" {
		t.Errorf("assigning another device after restart: code %q, want fsa_device_only", code)
	}
}

// #1026: GET /api/devices/{id}/default-control returns the default the send
// reply returned.

func (x *deviceControlFixture) getDefault(dev string) *httptest.ResponseRecorder {
	return x.f.do(http.MethodGet, "/api/devices/"+dev+"/default-control", "")
}

func TestDeviceControl_GetDefault_ReturnsTheViewTheSendReplyUses(t *testing.T) {
	x := newDeviceControlFixture(t)
	sent := decodeDeviceControl(t, x.post("1", defaultBody(4000)))

	w := x.getDefault("1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", w.Code, w.Body.String())
	}
	var got handler.ProgramDefaultControlView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if got.Type != "maxLimW" || got.DERControlBase.OpModMaxLimW == nil || *got.DERControlBase.OpModMaxLimW != 4000 {
		t.Errorf("default = %+v, want a maxLimW 4000 default control", got)
	}
	if sent.DefaultControl == nil || got.Type != sent.DefaultControl.Type {
		t.Errorf("get type %q, send reply %+v, want the same view", got.Type, sent.DefaultControl)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"mRID", "type", "derControlBase"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("body lacks %q: %s", k, w.Body.String())
		}
	}

	x.post("1", defaultBody(2500))
	var after handler.ProgramDefaultControlView
	if err := json.Unmarshal(x.getDefault("1").Body.Bytes(), &after); err != nil || *after.DERControlBase.OpModMaxLimW != 2500 {
		t.Errorf("default after a second send = %+v, %v, want 2500", after, err)
	}
}

func TestDeviceControl_GetDefault_NotFound(t *testing.T) {
	x := newDeviceControlFixture(t)
	if w := x.getDefault("nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown device = %d %s, want 404", w.Code, w.Body.String())
	}
	if w := x.getDefault("1"); w.Code != http.StatusNotFound {
		t.Errorf("device with no program = %d %s, want 404", w.Code, w.Body.String())
	}
	// A scheduled send leaves the program's default empty, which is no default.
	decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	if w := x.getDefault("1"); w.Code != http.StatusNotFound {
		t.Errorf("device with only an empty default = %d %s, want 404", w.Code, w.Body.String())
	}
	x.nothingWrittenByGet(t)
}

// nothingWrittenByGet fails when a read changed the stored control count.
func (x *deviceControlFixture) nothingWrittenByGet(t *testing.T) {
	t.Helper()
	if c, _ := x.dc.storedCounts(t); c != 1 {
		t.Errorf("stored controls = %d after reads, want the 1 the scheduled send wrote", c)
	}
}

// The 404 for an empty default follows the base's own function set, so a
// default with any function is served and one with none is not, whatever
// type name the view gives them.
func TestDeviceControl_GetDefault_EmptinessFollowsTheBaseFunctions(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	sent := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	scope := derhref.ScopeKey("1", sent.FSA, sent.Program)
	d, err := x.f.copies.Defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(dercontrol.FunctionNames(d.DERControlBase)); n != 0 {
		t.Fatalf("scheduled send left a default with %d functions, want 0", n)
	}
	if w := x.getDefault("1"); w.Code != http.StatusNotFound {
		t.Fatalf("empty default = %d, want 404", w.Code)
	}

	on := true
	d.DERControlBase = &sep2.DERControlBase{OpModConnect: &on}
	if err := x.f.copies.Defaults.Update(ctx, scope, coresingleton.SingletonKey, d); err != nil {
		t.Fatal(err)
	}
	if n := len(dercontrol.FunctionNames(d.DERControlBase)); n != 1 {
		t.Fatalf("seeded default has %d functions, want 1", n)
	}
	if w := x.getDefault("1"); w.Code != http.StatusOK {
		t.Errorf("default with opModConnect = %d %s, want 200", w.Code, w.Body.String())
	}
}

// #1043 round 1: a device program whose default was cleared is an operator
// state, not a server fault.
func TestDeviceControl_ClearedDefault_IsAConflictForDefaultAndNoObstacleForPrimacy(t *testing.T) {
	x := newDeviceControlFixture(t)
	first := decodeDeviceControl(t, x.post("1", defaultBody(4000)))
	if w := x.f.do(http.MethodPut, devicePath("1", first.Program), clearBody); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}

	w := x.post("1", defaultBody(2500))
	if code := decodeCode(t, w, http.StatusConflict); code != "no_default_control" {
		t.Errorf("kind default after a clear: code = %q, want no_default_control", code)
	}
	if _, err := x.f.copies.Defaults.Get(context.Background(), derhref.ScopeKey("1", first.FSA, first.Program), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused default wrote one: %v", err)
	}

	x.dc.seedProgram(t, "1", "0", "late", "LATE-PROGRAM", 50, true)
	second := decodeDeviceControl(t, x.post("1", scheduledBody(t, 3000)))
	if second.Program != first.Program || second.Primacy != 2 {
		t.Errorf("scheduled after a clear = program %s primacy %d, want program %s primacy 2", second.Program, second.Primacy, first.Program)
	}
	p, _ := x.f.copies.Programs.Get(context.Background(), "1", first.Program)
	if p.Primacy != 2 || p.DefaultDERControlLink != nil {
		t.Errorf("program primacy %d link %+v, want 2 and no link", p.Primacy, p.DefaultDERControlLink)
	}
}
