package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
)

// #968: a program's default control takes a functions set in place of type.

func functionsDefaultBody(functions string) string {
	return `{"description":"two functions","primacy":2,"defaultControl":{"functions":` + functions + `}}`
}

func TestCreateProgramTemplate_DefaultControlFunctions(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")

	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", functionsDefaultBody(`{"opModMaxLimW":6000,"opModTargetW":{"value":500,"multiplier":0}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var v struct {
		ID             string `json:"id"`
		DefaultControl struct {
			Type           string `json:"type"`
			DERControlBase struct {
				OpModMaxLimW *uint16 `json:"opModMaxLimW"`
				OpModTargetW *struct {
					Value int16 `json:"value"`
				} `json:"opModTargetW"`
			} `json:"derControlBase"`
		} `json:"defaultControl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if b := v.DefaultControl.DERControlBase; v.DefaultControl.Type != "combined" || b.OpModMaxLimW == nil || *b.OpModMaxLimW != 6000 ||
		b.OpModTargetW == nil || b.OpModTargetW.Value != 500 {
		t.Errorf("default control view = %+v, want combined with both values", v.DefaultControl)
	}

	stored, err := f.h.AdminFSAs.Template(t.Context(), "fsa-A", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := stored.DefaultControlBase
	if b.OpModMaxLimW == nil || *b.OpModMaxLimW != 6000 || b.OpModTargetW == nil || *b.OpModTargetW != (sep2.ActivePower{Value: 500}) {
		t.Errorf("stored template default = %+v, want maxLimW 6000 and targetW 500", b)
	}
	d, err := f.copies.Defaults.Get(t.Context(), "1/fsa-A/"+v.ID, coresingleton.SingletonKey)
	if err != nil {
		t.Fatalf("device copy default: %v", err)
	}
	if d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || *d.DERControlBase.OpModMaxLimW != 6000 ||
		d.DERControlBase.OpModTargetW == nil || d.DERControlBase.OpModTargetW.Value != 500 {
		t.Errorf("device copy default base = %+v, want both functions", d.DERControlBase)
	}
}

func TestProgramDefaultControl_TypeAndFunctionsRules(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")

	bad := map[string]string{
		"both":         `{"description":"d","primacy":1,"defaultControl":{"type":"connect","functions":{"opModConnect":true}}}`,
		"neither":      `{"description":"d","primacy":1,"defaultControl":{}}`,
		"empty":        functionsDefaultBody(`{}`),
		"unknown":      functionsDefaultBody(`{"opModBogus":1}`),
		"out of range": functionsDefaultBody(`{"opModMaxLimW":10001}`),
		"wrong type":   functionsDefaultBody(`{"opModConnect":"yes"}`),
		"value beside": `{"description":"d","primacy":1,"defaultControl":{"functions":{"opModConnect":true},"maxLimW":5}}`,
		"pf no excite": functionsDefaultBody(`{"opModFixedPFInjectW":{"displacement":900}}`),
	}
	for name, body := range bad {
		if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", body); w.Code != http.StatusBadRequest {
			t.Errorf("template create %s: status %d body %s, want 400", name, w.Code, w.Body.String())
		}
	}
	if w := f.do(http.MethodPost, "/api/devices/1/fsas/fsa-A/der-programs", bad["both"]); w.Code != http.StatusBadRequest {
		t.Errorf("device program both: status %d body %s, want 400", w.Code, w.Body.String())
	}
	if tmpls := f.h.AdminFSAs.Templates(t.Context(), "fsa-A"); len(tmpls) != 0 {
		t.Errorf("refused requests stored %d templates", len(tmpls))
	}
}

func TestUpdateProgramTemplate_DefaultControlFunctions(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	var v templateView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPut, "/api/fsas/fsa-A/der-programs/"+v.ID, functionsDefaultBody(`{"opModConnect":false,"opModMaxLimW":100}`))
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	stored, err := f.h.AdminFSAs.Template(t.Context(), "fsa-A", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := stored.DefaultControlBase
	if b.OpModConnect == nil || *b.OpModConnect || b.OpModMaxLimW == nil || *b.OpModMaxLimW != 100 || b.OpModEnergize != nil {
		t.Errorf("edited default = %+v, want connect false, maxLimW 100, no energize", b)
	}
}

func TestDeviceProgram_DefaultControlFunctionsCreateAndEdit(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")

	w := f.do(http.MethodPost, "/api/devices/1/fsas/fsa-A/der-programs", functionsDefaultBody(`{"opModMaxLimW":6000,"opModTargetW":{"value":500,"multiplier":0}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var v deviceProgramView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.DefaultControl.Type != "combined" {
		t.Errorf("created default type = %q, want combined", v.DefaultControl.Type)
	}
	key := "1/fsa-A/" + v.ID
	d, err := f.copies.Defaults.Get(t.Context(), key, coresingleton.SingletonKey)
	if err != nil || d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || *d.DERControlBase.OpModMaxLimW != 6000 ||
		d.DERControlBase.OpModTargetW == nil || d.DERControlBase.OpModTargetW.Value != 500 {
		t.Fatalf("stored default after create = %+v, %v, want maxLimW 6000 and targetW 500", d, err)
	}

	w = f.do(http.MethodPut, "/api/devices/1/der-programs/"+v.ID, functionsDefaultBody(`{"opModConnect":false,"opModEnergize":false}`))
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	d, err = f.copies.Defaults.Get(t.Context(), key, coresingleton.SingletonKey)
	b := d.DERControlBase
	if err != nil || b == nil || b.OpModConnect == nil || *b.OpModConnect || b.OpModEnergize == nil || *b.OpModEnergize ||
		b.OpModMaxLimW != nil || b.OpModTargetW != nil {
		t.Fatalf("stored default after edit = %+v, %v, want disconnect pair only", d, err)
	}
	p, err := f.copies.Programs.Get(t.Context(), "1", v.ID)
	if err != nil || p.Version == nil || *p.Version != 1 {
		t.Errorf("program after edit = %+v, %v, want version 1", p, err)
	}
}

// #1037: every view that serves a program's default control also serves the
// functions in the shape a form sends, curve ids included.

const defaultFunctionsSent = `{"opModMaxLimW":6000,"opModFixedPFInjectW":{"displacement":900,"excitation":true},"opModVoltVar":"c11"}`

type defaultControlRead struct {
	MRID           string         `json:"mRID"`
	Type           string         `json:"type"`
	Functions      map[string]any `json:"functions"`
	DERControlBase map[string]any `json:"derControlBase"`
}

func requireSentFunctions(t *testing.T, where string, got defaultControlRead) {
	t.Helper()
	var want map[string]any
	if err := json.Unmarshal([]byte(defaultFunctionsSent), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Functions, want) {
		t.Errorf("%s: functions = %v, want %v", where, got.Functions, want)
	}
	if got.Type != "combined" || got.MRID == "" {
		t.Errorf("%s: type %q mRID %q, want combined and a minted mRID", where, got.Type, got.MRID)
	}
	if got.DERControlBase["opModMaxLimW"] != float64(6000) {
		t.Errorf("%s: derControlBase = %v, want opModMaxLimW 6000", where, got.DERControlBase)
	}
	if link, _ := got.DERControlBase["opModVoltVar"].(map[string]any); link["href"] != "/dc/c11" {
		t.Errorf("%s: derControlBase opModVoltVar = %v, want the curve href", where, got.DERControlBase["opModVoltVar"])
	}
}

func TestProgramDefaultControl_FunctionsServedByEveryView(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.h.Curves = storedCurves(t)
	f.assign(t, "1", "fsa-A")
	body := functionsDefaultBody(defaultFunctionsSent)

	type withDefault struct {
		ID             string             `json:"id"`
		DefaultControl defaultControlRead `json:"defaultControl"`
	}
	decode := func(w *httptest.ResponseRecorder, want int) withDefault {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status %d body %s, want %d", w.Code, w.Body.String(), want)
		}
		var v withDefault
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	tmpl := decode(f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", body), http.StatusCreated)
	requireSentFunctions(t, "template create", tmpl.DefaultControl)

	w := f.do(http.MethodGet, "/api/fsas/fsa-A/der-programs", "")
	var list struct {
		Templates []withDefault `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %v %s", err, w.Body.String())
	}
	listed := list.Templates
	if len(listed) != 1 {
		t.Fatalf("listed %d templates, want 1: %s", len(listed), w.Body.String())
	}
	requireSentFunctions(t, "template list", listed[0].DefaultControl)

	edited := decode(f.do(http.MethodPut, "/api/fsas/fsa-A/der-programs/"+tmpl.ID, body), http.StatusOK)
	requireSentFunctions(t, "template edit", edited.DefaultControl)

	dev := decode(f.do(http.MethodPost, "/api/devices/1/fsas/fsa-A/der-programs", body), http.StatusCreated)
	requireSentFunctions(t, "device program create", dev.DefaultControl)

	devEdited := decode(f.do(http.MethodPut, "/api/devices/1/der-programs/"+dev.ID, body), http.StatusOK)
	requireSentFunctions(t, "device program edit", devEdited.DefaultControl)
}
