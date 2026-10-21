package handler_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1043: "defaultControl": null removes a program's default control on both
// PUT routes; a missing key keeps it.

const (
	clearBody = `{"description":"peak shave","primacy":70,"defaultControl":null}`
	keepBody  = `{"description":"peak shave","primacy":70}`
	setBody   = `{"description":"peak shave","primacy":70,"defaultControl":{"type":"maxLimW","maxLimW":4000}}`
)

// wireProgram is the DERProgram a device reads, as XML bytes.
func wireProgram(t *testing.T, f *templateFixture, dev, id string) string {
	t.Helper()
	p, err := f.copies.Programs.Get(context.Background(), dev, id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := xml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wireDefault is the body the real DefaultDERControl handler serves for the
// program.
func wireDefault(f *templateFixture, dev, fsa, id string) string {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", coreder.DefaultDERControlHandler(f.copies.Defaults.(store.ScopedStore[sep2.DefaultDERControl])))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edev/"+dev+"/fsa/"+fsa+"/derp/"+id+"/dderc", nil))
	return w.Body.String()
}

func requireNoDefault(t *testing.T, f *templateFixture, dev, fsa, id string) {
	t.Helper()
	if _, err := f.copies.Defaults.Get(context.Background(), derhref.ScopeKey(dev, fsa, id), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("device %s default store: %v, want not found", dev, err)
	}
	if xmlProg := wireProgram(t, f, dev, id); strings.Contains(xmlProg, "DefaultDERControlLink") {
		t.Errorf("device %s wire program still links a default: %s", dev, xmlProg)
	}
	if body := wireDefault(f, dev, fsa, id); strings.Contains(body, "opModMaxLimW") {
		t.Errorf("device %s wire default still carries a control: %s", dev, body)
	}
}

func requireDefaultServed(t *testing.T, f *templateFixture, dev, fsa, id string) {
	t.Helper()
	if xmlProg := wireProgram(t, f, dev, id); !strings.Contains(xmlProg, "DefaultDERControlLink") {
		t.Errorf("device %s wire program has no default link: %s", dev, xmlProg)
	}
	if body := wireDefault(f, dev, fsa, id); !strings.Contains(body, "opModMaxLimW") {
		t.Errorf("device %s wire default carries no control: %s", dev, body)
	}
}

func hasDefaultKey(t *testing.T, body []byte) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	_, ok := m["defaultControl"]
	return ok
}

func TestUpdateProgramTemplate_NullRemovesTheDefaultEverywhere(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	v := f.createTemplate(t)
	// The default is set and versioned first, so the removal has a version to keep.
	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), setBody); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	for _, dev := range []string{"1", "2"} {
		requireDefaultServed(t, f, dev, "fsa-A", v.ID)
	}
	f.notifier.take()

	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), clearBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT null: %d %s", w.Code, w.Body.String())
	}
	if hasDefaultKey(t, w.Body.Bytes()) {
		t.Errorf("PUT view still has defaultControl: %s", w.Body.String())
	}
	stored, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if err != nil || stored.DefaultControlBase != nil || stored.Version != 2 || stored.DefaultVersion != 1 || stored.DefaultMRID != v.DefaultControl.MRID {
		t.Errorf("stored template = %+v, %v, want no default, version 2, default version 1 and its mRID kept", stored, err)
	}
	for _, dev := range []string{"1", "2"} {
		requireNoDefault(t, f, dev, "fsa-A", v.ID)
		if p, _ := f.copies.Programs.Get(context.Background(), dev, v.ID); versionNum(p.Version) != 2 || p.Description != "peak shave" {
			t.Errorf("device %s copy version %d description %q, want 2", dev, versionNum(p.Version), p.Description)
		}
	}
	var want []recordedNotification
	for _, dev := range []string{"1", "2"} {
		want = append(want, edited("/edev/"+dev+"/fsa/fsa-A/derp/"+v.ID, true)...)
	}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}

	list := f.do(http.MethodGet, "/api/fsas/fsa-A/der-programs", "")
	var listed struct {
		Templates []json.RawMessage `json:"templates"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Templates) != 1 || hasDefaultKey(t, listed.Templates[0]) {
		t.Errorf("list = %s, %v, want one template with no defaultControl", list.Body.String(), err)
	}

	// Setting a value again restores the default under its old identity and
	// the next default version, and rewrites every copy.
	w = f.do(http.MethodPut, templatePath("fsa-A", v.ID), setBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT value after null: %d %s", w.Code, w.Body.String())
	}
	var back templateView
	if err := json.Unmarshal(w.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if stored.DefaultControlBase == nil || stored.DefaultVersion != 2 || back.DefaultControl.MRID != v.DefaultControl.MRID || *back.DefaultControl.DERControlBase.OpModMaxLimW != 4000 {
		t.Errorf("after setting again: stored %+v view %+v", stored, back.DefaultControl)
	}
	for _, dev := range []string{"1", "2"} {
		assertDeviceCopy(t, f.copies, dev, "fsa-A", back)
		requireDefaultServed(t, f, dev, "fsa-A", v.ID)
		if d := defaultOf(t, f, dev, "fsa-A", v.ID); versionNum(d.Version) != 2 {
			t.Errorf("device %s default version = %d, want 2", dev, versionNum(d.Version))
		}
	}
}

func TestUpdateProgramTemplate_MissingKeyKeepsTheDefault(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), keepBody)
	if w.Code != http.StatusOK || !hasDefaultKey(t, w.Body.Bytes()) {
		t.Fatalf("PUT without the key: %d %s, want 200 with defaultControl", w.Code, w.Body.String())
	}
	stored, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if stored.DefaultControlBase == nil || *stored.DefaultControlBase.OpModMaxLimW != 6000 || stored.DefaultVersion != 0 {
		t.Errorf("stored template = %+v, want the default untouched", stored)
	}
	var got templateView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	assertDeviceCopy(t, f.copies, "1", "fsa-A", got)
}

func TestUpdateProgramTemplate_NullOnAnEmptyDefaultIsANoOp(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), clearBody); w.Code != http.StatusOK {
		t.Fatalf("first null: %d %s", w.Code, w.Body.String())
	}
	before, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	f.notifier.take()

	w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), clearBody)
	if w.Code != http.StatusOK || hasDefaultKey(t, w.Body.Bytes()) {
		t.Fatalf("second null: %d %s, want 200 with no defaultControl", w.Code, w.Body.String())
	}
	after, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if after.Version != before.Version || after.DefaultControlBase != nil {
		t.Errorf("template after a second null = %+v, before %+v, want no change", after, before)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications = %+v, want none", got)
	}
	requireNoDefault(t, f, "1", "fsa-A", v.ID)
}

func TestProgramEdit_NullDefaultStillRefusesBadBodies(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	v := f.createTemplate(t)
	for name, body := range map[string]string{
		"unknown nested field": `{"description":"d","primacy":1,"defaultControl":{"type":"connect","bogus":1}}`,
		"empty object":         `{"description":"d","primacy":1,"defaultControl":{}}`,
		"wrong type":           `{"description":"d","primacy":1,"defaultControl":"x"}`,
		"unknown top field":    `{"description":"d","primacy":1,"defaultControl":null,"x":1}`,
	} {
		if w := f.do(http.MethodPut, templatePath("fsa-A", v.ID), body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	stored, _ := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if stored.DefaultControlBase == nil || stored.Version != 0 {
		t.Errorf("stored template = %+v after refusals, want it untouched", stored)
	}
	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", `{"description":"d","primacy":1,"defaultControl":null}`)
	if w.Code != http.StatusBadRequest || errorBody(t, w.Body.Bytes()) != "defaultControl: required" {
		t.Errorf("create with null: %d %s, want 400 defaultControl: required", w.Code, w.Body.String())
	}
}

func TestUpdateDeviceProgram_NullRemovesTheDefault(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	f.notifier.take()
	requireDefaultServed(t, f, "1", "fsa-A", created.ID)

	w := f.do(http.MethodPut, devicePath("1", created.ID), clearBody)
	if w.Code != http.StatusOK || hasDefaultKey(t, w.Body.Bytes()) {
		t.Fatalf("PUT null: %d %s, want 200 with no defaultControl", w.Code, w.Body.String())
	}
	requireNoDefault(t, f, "1", "fsa-A", created.ID)
	p, _ := f.copies.Programs.Get(context.Background(), "1", created.ID)
	if versionNum(p.Version) != 1 || p.Description != "peak shave" || p.Primacy != 70 {
		t.Errorf("program = version %d description %q primacy %d, want 1, peak shave, 70", versionNum(p.Version), p.Description, p.Primacy)
	}
	want := edited("/edev/1/fsa/fsa-A/derp/"+created.ID, true)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}

	// Null again changes nothing: no version, no notification.
	w = f.do(http.MethodPut, devicePath("1", created.ID), clearBody)
	if w.Code != http.StatusOK {
		t.Fatalf("second null: %d %s", w.Code, w.Body.String())
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "1", created.ID); versionNum(p.Version) != 1 {
		t.Errorf("version after a second null = %d, want 1", versionNum(p.Version))
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications after a second null = %+v, want none", got)
	}
}

func TestUpdateDeviceProgram_MissingKeyKeepsAndValueSets(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)

	w := f.do(http.MethodPut, devicePath("1", created.ID), keepBody)
	if w.Code != http.StatusOK || !hasDefaultKey(t, w.Body.Bytes()) {
		t.Fatalf("PUT without the key: %d %s", w.Code, w.Body.String())
	}
	d := defaultOf(t, f, "1", "fsa-A", created.ID)
	if d.DERControlBase == nil || *d.DERControlBase.OpModMaxLimW != 6000 || d.Version != nil {
		t.Errorf("default after a missing key = %+v, want it untouched", d)
	}

	w = f.do(http.MethodPut, devicePath("1", created.ID), setBody)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT value: %d %s", w.Code, w.Body.String())
	}
	d = defaultOf(t, f, "1", "fsa-A", created.ID)
	if d.DERControlBase == nil || *d.DERControlBase.OpModMaxLimW != 4000 || versionNum(d.Version) != 1 {
		t.Errorf("default after a value = %+v, want 4000 at version 1", d)
	}
	requireDefaultServed(t, f, "1", "fsa-A", created.ID)
}

func TestUpdateDeviceProgram_NullOnAFixtureProgram(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	seedFixtureProgram(t, f, "1", "0", "0", true)
	seedFixtureProgram(t, f, "2", "0", "0", false)

	if w := f.do(http.MethodPut, devicePath("1", "0"), `{"description":"fixture","primacy":1,"defaultControl":null}`); w.Code != http.StatusOK {
		t.Fatalf("fixture with a default: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.copies.Defaults.Get(context.Background(), derhref.ScopeKey("1", "0", "0"), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("fixture default after null: %v, want not found", err)
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "1", "0"); versionNum(p.Version) != 1 || p.MRID != "0000000000000000000000000000F1C5" {
		t.Errorf("fixture program = %+v", p)
	}

	// A program that never had a default: null with the same body is a no-op.
	if w := f.do(http.MethodPut, devicePath("2", "0"), `{"description":"fixture","primacy":1,"defaultControl":null}`); w.Code != http.StatusOK {
		t.Fatalf("fixture with none: %d %s", w.Code, w.Body.String())
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "2", "0"); p.Version != nil {
		t.Errorf("version = %d, want it untouched", versionNum(p.Version))
	}
}

// #1043: a program whose default was removed takes one again, with a newly
// minted mRID and a version above the removed default's.
func TestUpdateDeviceProgram_ClearThenSetServesANewDefault(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	if w := f.do(http.MethodPut, devicePath("1", created.ID), setBody); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	removed := defaultOf(t, f, "1", "fsa-A", created.ID)
	if w := f.do(http.MethodPut, devicePath("1", created.ID), clearBody); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	requireNoDefault(t, f, "1", "fsa-A", created.ID)
	f.notifier.take()

	w := f.do(http.MethodPut, devicePath("1", created.ID), `{"description":"peak shave","primacy":70,"defaultControl":{"type":"maxLimW","maxLimW":2500}}`)
	if w.Code != http.StatusOK || !hasDefaultKey(t, w.Body.Bytes()) {
		t.Fatalf("set again: %d %s", w.Code, w.Body.String())
	}
	d := defaultOf(t, f, "1", "fsa-A", created.ID)
	p, _ := f.copies.Programs.Get(context.Background(), "1", created.ID)
	if !penMRIDPattern.MatchString(d.MRID) || d.MRID == p.MRID {
		t.Errorf("default mRID = %q, want a minted mRID distinct from the program's %q", d.MRID, p.MRID)
	}
	if d.DERControlBase == nil || uint16(*d.DERControlBase.OpModMaxLimW) != 2500 || d.Href != "/edev/1/fsa/fsa-A/derp/"+created.ID+"/dderc" {
		t.Errorf("default = %+v", d)
	}
	if versionNum(d.Version) <= versionNum(removed.Version) || versionNum(d.Version) != versionNum(p.Version) {
		t.Errorf("default version %d, program version %d, removed default version %d: want it above the removed one", versionNum(d.Version), versionNum(p.Version), versionNum(removed.Version))
	}
	if p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != d.Href {
		t.Errorf("program link = %+v, want %s", p.DefaultDERControlLink, d.Href)
	}
	requireDefaultServed(t, f, "1", "fsa-A", created.ID)
	if !strings.Contains(wireDefault(f, "1", "fsa-A", created.ID), d.MRID) {
		t.Errorf("wire default does not carry mRID %s", d.MRID)
	}
	if got, want := f.notifier.take(), edited("/edev/1/fsa/fsa-A/derp/"+created.ID, true); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}
