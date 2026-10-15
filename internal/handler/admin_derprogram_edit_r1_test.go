package handler_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #922 fix round 1: notification of every changed representation, conflict
// devices, convergence after a partial template edit, retryable deletes.

func TestUpdateDeviceProgram_NotifiesEachChangedRepresentation(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	href := seedFixtureProgram(t, f, "1", "0", "0", true)

	if w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"edited","primacy":1}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got, want := f.notifier.take(), edited(href, false); !slices.Equal(got, want) {
		t.Errorf("program-only edit notified %+v, want %+v", got, want)
	}
	if w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"edited","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got, want := f.notifier.take(), edited(href, true); !slices.Equal(got, want) {
		t.Errorf("default edit notified %+v, want %+v", got, want)
	}
	if w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"edited","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("an unchanged edit notified %+v, want nothing", got)
	}
}

func TestUpdateDeviceProgram_RefusesAChangedMRID(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	seedFixtureProgram(t, f, "1", "0", "0", true)
	w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"x","primacy":1,"mRID":"0000000000000000000000000000A0B1"}`)
	if w.Code != http.StatusConflict || decodeCoded(t, w.Body.Bytes()).Code != "mrid_immutable" {
		t.Fatalf("PUT with another mRID: %d %s, want 409 mrid_immutable", w.Code, w.Body.String())
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "1", "0"); p.Description != "fixture" || p.Version != nil {
		t.Errorf("program = %+v, want it untouched", p)
	}
	// The same mRID in another case is the same mRID.
	if w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"x","primacy":1,"mRID":"0000000000000000000000000000f1c5"}`); w.Code != http.StatusOK {
		t.Errorf("PUT with its own mRID: %d %s, want 200", w.Code, w.Body.String())
	}
}

func TestUpdateDeviceProgram_VersionExhausted409(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newTemplateFixture(t)
	seedFixtureProgram(t, f, "1", "0", "0", true)
	seedFixtureProgram(t, f, "2", "0", "0", true)
	p, _ := f.copies.Programs.Get(ctx, "1", "0")
	p.Version = ptrU16(65535)
	if err := f.copies.Programs.Update(ctx, "1", "0", p); err != nil {
		t.Fatal(err)
	}
	d := defaultOf(t, f, "2", "0", "0")
	d.Version = ptrU16(65535)
	if err := f.copies.Defaults.Update(ctx, fsaprogram.DefaultScope("2", "0", "0"), coresingleton.SingletonKey, d); err != nil {
		t.Fatal(err)
	}
	for dev, body := range map[string]string{
		"1": `{"description":"x","primacy":1}`,
		"2": `{"description":"fixture","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`,
	} {
		w := f.do(http.MethodPut, devicePath(dev, "0"), body)
		if w.Code != http.StatusConflict || decodeCoded(t, w.Body.Bytes()).Code != "version_exhausted" {
			t.Errorf("device %s at 65535: %d %s, want 409 version_exhausted", dev, w.Code, w.Body.String())
		}
	}
	if got, _ := f.copies.Programs.Get(ctx, "1", "0"); got.Description != "fixture" {
		t.Errorf("device 1 program written: %+v", got)
	}
	if got := defaultOf(t, f, "2", "0", "0"); *got.SetGradW != 50 || got.DERControlBase != nil {
		t.Errorf("device 2 default written: %+v", got)
	}
}

// A device whose own program conflicts with a template holds no copy of it,
// as the boot rebuild treats it, so it does not block the template's edit or
// delete and is not touched by them.
func TestTemplateRoutes_SkipADeviceWithAConflictingProgram(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	other := sep2.DERProgram{MRID: v.MRID, Description: "own"}
	other.Href = "/edev/2/fsa/0/derp/0"
	if err := f.copies.Programs.Create(ctx, "2", "0", other); err != nil {
		t.Fatal(err)
	}
	if err := f.h.AdminFSAs.AssignDevice(ctx, "fsa-A", "2"); err != nil {
		t.Fatal(err)
	}
	f.notifier.take()

	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), peakShaveBody); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s, want 200", w.Code, w.Body.String())
	}
	if p, _ := f.copies.Programs.Get(ctx, "1", v.ID); p.Description != "peak shave" {
		t.Errorf("device 1 copy = %+v, want it edited", p)
	}
	if w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s, want 204", w.Code, w.Body.String())
	}
	if p, err := f.copies.Programs.Get(ctx, "2", "0"); err != nil || p.Description != "own" || p.Version != nil {
		t.Errorf("device 2's own program = %+v, %v; want it untouched", p, err)
	}
	for _, n := range f.notifier.take() {
		if strings.HasPrefix(n.href, "/edev/2/") {
			t.Errorf("device 2 was notified: %+v", n)
		}
	}
}

// faultyCopyPrograms fails every program Update on one device.
type faultyCopyPrograms struct {
	fsaprogram.CopyProgramStore
	device string
}

func (p faultyCopyPrograms) Update(ctx context.Context, parent, id string, r sep2.DERProgram) error {
	if parent == p.device {
		return errInjected
	}
	return p.CopyProgramStore.Update(ctx, parent, id, r)
}

// faultyCopyDefaults fails default Updates under prefix from the failFrom-th
// on.
type faultyCopyDefaults struct {
	fsaprogram.CopyDefaultStore
	prefix   string
	failFrom int
	mu       sync.Mutex
	calls    int
}

func (d *faultyCopyDefaults) Update(ctx context.Context, parent, id string, r sep2.DefaultDERControl) error {
	if strings.HasPrefix(parent, d.prefix) {
		d.mu.Lock()
		d.calls++
		n := d.calls
		d.mu.Unlock()
		if n >= d.failFrom {
			return errInjected
		}
	}
	return d.CopyDefaultStore.Update(ctx, parent, id, r)
}

// revisions records which default content each served version had, and
// fails when one version is seen with two contents.
type revisions map[int]uint16

func (rv revisions) see(t *testing.T, f *templateFixture, dev, id string) {
	t.Helper()
	d := defaultOf(t, f, dev, "fsa-A", id)
	v, w := versionNum(d.Version), uint16(*d.DERControlBase.OpModMaxLimW)
	if prev, ok := rv[v]; ok && prev != w {
		t.Errorf("device %s default version %d holds %d, but held %d before", dev, v, w, prev)
	}
	rv[v] = w
}

func TestUpdateProgramTemplate_PartialFailureConvergesWithoutReusingAVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	v := f.createTemplate(t)
	f.notifier.take()
	seen := revisions{}
	seen.see(t, f, "1", v.ID)

	healthy := *f.h.Copies
	f.h.Copies.Programs = faultyCopyPrograms{CopyProgramStore: healthy.Programs, device: "2"}
	f.h.Copies.Defaults = &faultyCopyDefaults{CopyDefaultStore: healthy.Defaults, prefix: "2/", failFrom: 2}
	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), peakShaveBody)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "devices 2") {
		t.Fatalf("PUT with device 2 failing: %d %s, want 500 naming device 2", w.Code, w.Body.String())
	}
	stored, _ := f.h.AdminFSAs.Template(ctx, "fsa-A", v.ID)
	if stored.Version != 1 || stored.DefaultVersion != 1 {
		t.Fatalf("template = v%d default v%d, want it saved at 1 and 1", stored.Version, stored.DefaultVersion)
	}
	if p, _ := healthy.Programs.Get(ctx, "1", v.ID); versionNum(p.Version) != 1 || p.Description != "peak shave" {
		t.Errorf("device 1 copy = %+v, want it at the edit", p)
	}
	if p, _ := healthy.Programs.Get(ctx, "2", v.ID); versionNum(p.Version) != 0 || p.Description != "volt support" {
		t.Errorf("device 2 program = %+v, want it at the old revision", p)
	}
	seen.see(t, f, "1", v.ID)
	seen.see(t, f, "2", v.ID)
	href2 := "/edev/2/fsa/fsa-A/derp/" + v.ID
	want := append(edited("/edev/1/fsa/fsa-A/derp/"+v.ID, true),
		recordedNotification{href2 + "/dderc", sep2.NotificationStatusDefault}, recordedNotification{"/edev/2/fsa/fsa-A/derp", sep2.NotificationStatusDefault})
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}

	// A restart's rebuild finishes device 2.
	*f.h.Copies = healthy
	if err := fsaprogram.ReconcileCopies(ctx, f.h.AdminFSAs, healthy, t.Logf); err != nil {
		t.Fatal(err)
	}
	if p, _ := healthy.Programs.Get(ctx, "2", v.ID); versionNum(p.Version) != 1 || p.Description != "peak shave" || p.Primacy != 70 {
		t.Errorf("device 2 program after the rebuild = %+v, want the edit", p)
	}
	seen.see(t, f, "2", v.ID)

	// The next edit takes fresh versions on every copy.
	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), `{"description":"peak shave","primacy":70,"defaultControl":{"type":"maxLimW","maxLimW":3000}}`); w.Code != http.StatusOK {
		t.Fatalf("second PUT: %d %s", w.Code, w.Body.String())
	}
	for _, dev := range []string{"1", "2"} {
		if p, _ := healthy.Programs.Get(ctx, dev, v.ID); versionNum(p.Version) != 2 {
			t.Errorf("device %s program version = %d, want 2", dev, versionNum(p.Version))
		}
		seen.see(t, f, dev, v.ID)
	}
	if seen[2] != 3000 || seen[1] != 4000 || seen[-1] != 6000 {
		t.Errorf("default revisions = %v, want none -> 6000, 1 -> 4000, 2 -> 3000", seen)
	}
}

// A repeat of a template edit that left a copy behind finishes that copy.
func TestUpdateProgramTemplate_ARepeatFinishesACopyLeftBehind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	v := f.createTemplate(t)
	healthy := *f.h.Copies
	f.h.Copies.Programs = faultyCopyPrograms{CopyProgramStore: healthy.Programs, device: "2"}
	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), peakShaveBody); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT with device 2 failing: %d %s, want 500", w.Code, w.Body.String())
	}
	*f.h.Copies = healthy
	f.notifier.take()
	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), peakShaveBody); w.Code != http.StatusOK {
		t.Fatalf("repeat: %d %s, want 200", w.Code, w.Body.String())
	}
	var tv templateView
	tv.ID, tv.MRID, tv.Description, tv.Primacy, tv.Version = v.ID, v.MRID, "peak shave", 70, 1
	tv.DefaultControl = v.DefaultControl
	tv.DefaultControl.DERControlBase.OpModMaxLimW = ptrU16(4000)
	assertDeviceCopy(t, healthy, "1", "fsa-A", tv)
	assertDeviceCopy(t, healthy, "2", "fsa-A", tv)
	if stored, _ := f.h.AdminFSAs.Template(ctx, "fsa-A", v.ID); stored.Version != 1 {
		t.Errorf("template version after the repeat = %d, want 1", stored.Version)
	}
	// The failed Sync put device 2's default back, so both parts are written.
	if got, want := f.notifier.take(), edited("/edev/2/fsa/fsa-A/derp/"+v.ID, true); !slices.Equal(got, want) {
		t.Errorf("repeat notified %+v, want %+v", got, want)
	}
}

// failingCopyDefaultDelete fails default Deletes under prefix while fail is
// set.
type failingCopyDefaultDelete struct {
	fsaprogram.CopyDefaultStore
	prefix string
	fail   bool
}

func (d *failingCopyDefaultDelete) Delete(ctx context.Context, parent, id string) error {
	if d.fail && strings.HasPrefix(parent, d.prefix) {
		return errInjected
	}
	return d.CopyDefaultStore.Delete(ctx, parent, id)
}

// A fixture program delete that fails keeps the program, so the retry finds
// it and finishes, and no default outlives its program.
func TestDeleteDeviceProgram_APartialFixtureDeleteIsRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newTemplateFixture(t)
	href := seedFixtureProgram(t, f, "1", "0", "0", true)
	fd := &failingCopyDefaultDelete{CopyDefaultStore: f.copies.Defaults, prefix: "1/", fail: true}
	f.h.Copies.Defaults = fd
	if w := f.do(http.MethodDelete, devicePath("1", "0"), ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE with the default failing: %d %s, want 500", w.Code, w.Body.String())
	}
	if _, err := f.copies.Programs.Get(ctx, "1", "0"); err != nil {
		t.Fatalf("program after the failure: %v, want kept", err)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("a failed delete notified %+v, want nothing: the program is still served", got)
	}
	fd.fail = false
	if w := f.do(http.MethodDelete, devicePath("1", "0"), ""); w.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s, want 204", w.Code, w.Body.String())
	}
	if _, err := f.copies.Defaults.Get(ctx, fsaprogram.DefaultScope("1", "0", "0"), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after the retry: %v, want gone", err)
	}
	if got, want := f.notifier.take(), deleted(href); !slices.Equal(got, want) {
		t.Errorf("retry notified %+v, want %+v", got, want)
	}
}

// A template delete that fails on a copy's default, after that copy's program
// is gone, still tells that device the program is gone.
func TestDeleteProgramTemplate_NotifiesACopyWhoseLaterPartFailed(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	v := f.createTemplate(t)
	f.notifier.take()
	f.h.Copies.Defaults = &failingCopyDefaultDelete{CopyDefaultStore: f.copies.Defaults, prefix: "2/", fail: true}
	w := f.do(http.MethodDelete, templatePath("fsa-A", v.ID), "")
	body := w.Body.String()
	if w.Code != http.StatusInternalServerError || !strings.Contains(body, "copy on device 2") || !strings.Contains(body, "devices 1, 2") {
		t.Fatalf("DELETE: %d %s, want 500 naming the failed copy and both removed devices", w.Code, body)
	}
	if _, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID); err != nil {
		t.Errorf("template: %v, want kept", err)
	}
	want := append(deleted("/edev/1/fsa/fsa-A/derp/"+v.ID), deleted("/edev/2/fsa/fsa-A/derp/"+v.ID)...)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}
