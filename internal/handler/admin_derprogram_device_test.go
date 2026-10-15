package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #921: a DER program created on one device.

type deviceProgramView struct {
	ID             string `json:"id"`
	Href           string `json:"href"`
	MRID           string `json:"mRID"`
	Description    string `json:"description"`
	Primacy        uint8  `json:"primacy"`
	Version        uint16 `json:"version"`
	DefaultControl struct {
		MRID           string `json:"mRID"`
		Type           string `json:"type"`
		DERControlBase struct {
			OpModMaxLimW *uint16 `json:"opModMaxLimW"`
		} `json:"derControlBase"`
	} `json:"defaultControl"`
}

func deviceProgramPath(dev, fsa string) string {
	return "/api/devices/" + dev + "/fsas/" + fsa + "/der-programs"
}

func (f *templateFixture) createDeviceProgram(t *testing.T, dev, fsa, body string) deviceProgramView {
	t.Helper()
	w := f.do(http.MethodPost, deviceProgramPath(dev, fsa), body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create on device %s under %s: %d %s", dev, fsa, w.Code, w.Body.String())
	}
	var v deviceProgramView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *templateFixture) addAdminFSA(t *testing.T, id string) {
	t.Helper()
	if err := f.h.AdminFSAs.Create(context.Background(), id, sep2.FunctionSetAssignments{MRID: id, Description: id}); err != nil {
		t.Fatal(err)
	}
}

// programIDsInFSA is what the wire serves under /edev/{dev}/fsa/{fsa}/derp:
// the device's programs whose own href names the FSA.
func programIDsInFSA(t *testing.T, f *templateFixture, dev, fsa string) []string {
	t.Helper()
	list, err := f.copies.Programs.List(context.Background(), dev, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	prefix := "/edev/" + dev + "/fsa/" + fsa + "/derp/"
	for _, p := range list.Items {
		if id, ok := strings.CutPrefix(p.Href, prefix); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func TestCreateDeviceProgram_StoresAServedDeviceProgram(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.addAdminFSA(t, "fsa-B")
	f.assign(t, "1", "fsa-A")
	f.assign(t, "1", "fsa-B")
	f.assign(t, "2", "fsa-A")
	f.notifier.take()

	v := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	href := "/edev/1/fsa/fsa-A/derp/" + v.ID
	if !templateIDPattern.MatchString(v.ID) || !penMRIDPattern.MatchString(v.MRID) || !penMRIDPattern.MatchString(v.DefaultControl.MRID) || v.MRID == v.DefaultControl.MRID {
		t.Errorf("id %q mRID %q default mRID %q: want a derp- id and two distinct PEN mRIDs", v.ID, v.MRID, v.DefaultControl.MRID)
	}
	if v.Href != href || v.Description != "volt support" || v.Primacy != 2 || v.Version != 0 || v.DefaultControl.Type != "maxLimW" ||
		v.DefaultControl.DERControlBase.OpModMaxLimW == nil || *v.DefaultControl.DERControlBase.OpModMaxLimW != 6000 {
		t.Errorf("view = %+v, want href %s", v, href)
	}

	p, err := f.copies.Programs.Get(context.Background(), "1", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Href != href || p.MRID != v.MRID || p.Description != "volt support" || p.Primacy != 2 || p.Version == nil || *p.Version != 0 ||
		p.DERControlListLink == nil || p.DERControlListLink.Href != href+"/derc" ||
		p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != href+"/dderc" {
		t.Errorf("stored program = %+v", p)
	}
	d, err := f.copies.Defaults.Get(context.Background(), "1/fsa-A/"+v.ID, coresingleton.SingletonKey)
	if err != nil || d.Href != href+"/dderc" || d.MRID != v.DefaultControl.MRID ||
		d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || uint16(*d.DERControlBase.OpModMaxLimW) != 6000 {
		t.Errorf("stored default = %+v, %v", d, err)
	}
	o, err := f.copies.Origins.Get(context.Background(), "1", v.ID)
	if err != nil || o.Kind != memory.ProgramOriginDevice || o.FSAID != "fsa-A" || o.TemplateID != "" {
		t.Errorf("origin = %+v, %v, want a device origin under fsa-A", o, err)
	}

	if got := programIDsInFSA(t, f, "1", "fsa-A"); !slices.Equal(got, []string{v.ID}) {
		t.Errorf("fsa-A list = %v, want [%s]", got, v.ID)
	}
	if got := programIDsInFSA(t, f, "1", "fsa-B"); len(got) != 0 {
		t.Errorf("fsa-B list = %v, want none", got)
	}
	assertNoDeviceCopy(t, f.copies, "2")
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 0 {
		t.Errorf("%d templates stored, want 0: a device program is not a template", n)
	}
	want := []recordedNotification{{"/edev/1/fsa/fsa-A/derp", sep2.NotificationStatusDefault}}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}

func TestCreateDeviceProgram_UnderAStoreOnlyFSA_201(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	fx := sep2.FunctionSetAssignments{MRID: "fx"}
	if err := f.h.DeviceFSAs.Create(context.Background(), "1", "fx", fx); err != nil {
		t.Fatal(err)
	}
	v := f.createDeviceProgram(t, "1", "fx", voltSupportBody)
	if got := programIDsInFSA(t, f, "1", "fx"); !slices.Equal(got, []string{v.ID}) {
		t.Errorf("fx list = %v, want [%s]", got, v.ID)
	}
}

func TestCreateDeviceProgram_Refusals(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.notifier.take()
	cases := map[string]struct {
		dev, fsa, body string
		want           int
	}{
		// The wire serves a default FSA for any id; the store is what counts.
		"fsa the device does not have":       {"1", "fsa-Z", voltSupportBody, http.StatusNotFound},
		"fsa an admin knows, device lacks":   {"2", "fsa-A", voltSupportBody, http.StatusNotFound},
		"device unknown":                     {"9", "fsa-A", voltSupportBody, http.StatusNotFound},
		"missing description":                {"1", "fsa-A", `{"primacy":1,"defaultControl":{"type":"connect"}}`, http.StatusBadRequest},
		"bad primacy":                        {"1", "fsa-A", `{"description":"d","primacy":3,"defaultControl":{"type":"connect"}}`, http.StatusBadRequest},
		"unknown field":                      {"1", "fsa-A", `{"description":"d","primacy":1,"defaultControl":{"type":"connect"},"x":1}`, http.StatusBadRequest},
		"mRID reserved form":                 {"1", "fsa-A", `{"description":"d","primacy":1,"mRID":"FFFFFFFFFFFFFFFFFFFFFFFF00000001","defaultControl":{"type":"connect"}}`, http.StatusBadRequest},
		"default control missing":            {"1", "fsa-A", `{"description":"d","primacy":1}`, http.StatusBadRequest},
		"default control value out of range": {"1", "fsa-A", `{"description":"d","primacy":1,"defaultControl":{"type":"maxLimW"}}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := f.do(http.MethodPost, deviceProgramPath(tc.dev, tc.fsa), tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	for _, dev := range []string{"1", "2", "3"} {
		assertNoDeviceCopy(t, f.copies, dev)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications after refusals: %+v", got)
	}
}

func TestCreateDeviceProgram_MRIDAlreadyOnTheDevice_409_WritesNothing(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	const mrid = "0123456789ABCDEF0123456789ABCDEF"
	body := `{"description":"d","primacy":1,"mRID":"` + mrid + `","defaultControl":{"type":"connect"}}`

	taken := sep2.DERProgram{MRID: mrid}
	taken.Href = "/edev/1/fsa/fx/derp/0"
	if err := f.copies.Programs.Create(context.Background(), "1", "0", taken); err != nil {
		t.Fatal(err)
	}
	f.notifier.take()
	if w := f.do(http.MethodPost, deviceProgramPath("1", "fsa-A"), body); w.Code != http.StatusConflict {
		t.Fatalf("against a fixture program: %d %s, want 409", w.Code, w.Body.String())
	}
	list, _ := f.copies.Programs.List(context.Background(), "1", store.ListOptions{Unbounded: true})
	if len(list.Items) != 1 {
		t.Errorf("device 1 holds %d programs, want only the fixture", len(list.Items))
	}

	// Another device with the same mRID is no conflict, and the same mRID
	// twice on one device is.
	f.createDeviceProgram(t, "2", "fsa-A", body)
	if w := f.do(http.MethodPost, deviceProgramPath("2", "fsa-A"), strings.ToLower(body)); w.Code != http.StatusConflict {
		t.Fatalf("second on device 2: %d %s, want 409", w.Code, w.Body.String())
	}
	if got := programIDsInFSA(t, f, "2", "fsa-A"); len(got) != 1 {
		t.Errorf("device 2 holds %v, want exactly one", got)
	}
}

func TestCreateDeviceProgram_CappedPerDeviceAndFSA(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	for range 16 {
		f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	}
	if w := f.do(http.MethodPost, deviceProgramPath("1", "fsa-A"), voltSupportBody); w.Code != http.StatusConflict {
		t.Fatalf("17th: %d %s, want 409", w.Code, w.Body.String())
	}
	if got := programIDsInFSA(t, f, "1", "fsa-A"); len(got) != 16 {
		t.Errorf("%d programs stored, want 16", len(got))
	}
}

func TestUnassign_RefusedWhileDeviceProgramsSitUnderTheFSA(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	tmpl := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if tmpl.Code != http.StatusCreated {
		t.Fatalf("template: %d %s", tmpl.Code, tmpl.Body.String())
	}
	var tv templateView
	if err := json.Unmarshal(tmpl.Body.Bytes(), &tv); err != nil {
		t.Fatal(err)
	}
	v := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	f.notifier.take()

	w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), v.ID) {
		t.Fatalf("unassign: %d %s, want 409 naming %s", w.Code, w.Body.String(), v.ID)
	}
	if devs := f.h.AdminFSAs.Devices(context.Background(), "fsa-A"); !slices.Equal(devs, []string{"1"}) {
		t.Errorf("devices of fsa-A = %v, want [1]: the refusal must leave the assignment", devs)
	}
	assertDeviceCopy(t, f.copies, "1", "fsa-A", tv)
	if _, err := f.copies.Programs.Get(context.Background(), "1", v.ID); err != nil {
		t.Errorf("device program after the refusal: %v", err)
	}
	if _, err := f.h.DeviceFSAs.Get(context.Background(), "1", "fsa-A"); err != nil {
		t.Errorf("device FSA after the refusal: %v", err)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications after the refusal: %+v", got)
	}

	// The operator deletes the device program; the unassign then succeeds
	// and removes the template copy as before.
	ctx := context.Background()
	for _, step := range []error{
		f.copies.Programs.Delete(ctx, "1", v.ID),
		f.copies.Defaults.Delete(ctx, "1/fsa-A/"+v.ID, coresingleton.SingletonKey),
		f.copies.Origins.Delete(ctx, "1", v.ID),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	if w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unassign after delete: %d %s, want 204", w.Code, w.Body.String())
	}
	assertNoDeviceCopy(t, f.copies, "1")
}

func TestUnassign_DeviceProgramOfAnotherFSAOrDeviceDoesNotBlock(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.addAdminFSA(t, "fsa-B")
	f.assign(t, "1", "fsa-A")
	f.assign(t, "1", "fsa-B")
	f.assign(t, "2", "fsa-A")
	f.createDeviceProgram(t, "1", "fsa-B", voltSupportBody)
	f.createDeviceProgram(t, "2", "fsa-A", voltSupportBody)
	if w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unassign: %d %s, want 204", w.Code, w.Body.String())
	}
}

// The control issuer reads the program a device program writes, so a control
// created against it lands in its own derc list.
func TestCreateDeviceProgram_AControlCanBeIssuedOnIt(t *testing.T) {
	t.Parallel()
	d := newDCHarness(t, ptrU32(dcPEN))
	f := newTemplateFixtureWith(t, d.programs, d.devices)
	if err := f.h.DeviceFSAs.Create(context.Background(), dcDeviceB, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	v := f.createDeviceProgram(t, dcDeviceB, "fsa-A", voltSupportBody)

	start := time.Now().Unix() + 600
	body := fmt.Sprintf(`{"derProgramHref":%q,"type":"maxLimW","maxLimW":4000,"startTime":%d,"durationSeconds":300}`, v.Href, start)
	w := d.do(t, http.MethodPost, "/api/der/controls", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("control create: %d %s, want 201", w.Code, w.Body.String())
	}
	got := decodeCreated(t, w)
	if got.DERProgramHref != v.Href || got.DERControlListHref != v.Href+"/derc" {
		t.Errorf("control program %q list %q, want %q and %q", got.DERProgramHref, got.DERControlListHref, v.Href, v.Href+"/derc")
	}
	list, err := d.controls.List(context.Background(), dcDeviceB+"/fsa-A/"+v.ID, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].MRID != got.MRID {
		t.Errorf("controls under the new program = %+v, want one with mRID %s", list.Items, got.MRID)
	}
}

var _ handler.EndDeviceReader = (*memory.EndDeviceStore)(nil)
