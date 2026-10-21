package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// #968: a control carries a set of functions, named by their 2018
// DERControlBase element names, as an alternative to a single type.

func functionsBody(start int64, functions string) string {
	return fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","functions":%s,"startTime":%d,"durationSeconds":300}`, functions, start)
}

func TestDERControlCreate_TwoFunctions(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	start := futureStart(600)
	w := d.do(t, http.MethodPost, "/api/der/controls",
		functionsBody(start, `{"opModMaxLimW":5000,"opModFixedPFInjectW":{"displacement":900,"excitation":true}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
	}
	got := decodeCreated(t, w)

	if got.Type != "combined" {
		t.Errorf("type = %q, want combined", got.Type)
	}
	base := got.DERControlBase
	if base.OpModMaxLimW == nil || *base.OpModMaxLimW != 5000 ||
		base.OpModFixedPFInjectW == nil || base.OpModFixedPFInjectW.Displacement != 900 || !base.OpModFixedPFInjectW.Excitation {
		t.Errorf("derControlBase = %+v, want maxLimW 5000 and pf 900 over-excited", base)
	}
	if base.OpModConnect != nil || base.OpModTargetW != nil {
		t.Errorf("derControlBase carries unrequested functions: %+v", base)
	}
	var raw struct {
		Functions map[string]json.RawMessage `json:"functions"`
		Ended     *bool                      `json:"ended"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Functions) != 2 || string(raw.Functions["opModMaxLimW"]) != "5000" ||
		string(raw.Functions["opModFixedPFInjectW"]) != `{"displacement":900,"excitation":true}` {
		t.Errorf("functions = %v, want the two requested values", raw.Functions)
	}
	if raw.Ended == nil || *raw.Ended {
		t.Errorf("ended = %v, want false for a control that has not started", raw.Ended)
	}

	_, _, stored, err := d.controls.ByMRID(t.Context(), got.MRID)
	if err != nil {
		t.Fatalf("ByMRID: %v", err)
	}
	b := stored.DERControlBase
	if b == nil || b.OpModMaxLimW == nil || *b.OpModMaxLimW != 5000 || b.OpModFixedPFInjectW == nil ||
		b.OpModFixedPFInjectW.Displacement != 900 || !b.OpModFixedPFInjectW.Excitation || b.OpModFixedPFInjectW.Multiplier != -3 {
		t.Errorf("stored base = %+v, want both functions", b)
	}
}

func TestDERControlCreate_FunctionsAllFour(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	w := d.do(t, http.MethodPost, "/api/der/controls",
		functionsBody(futureStart(600), `{"opModConnect":true,"opModEnergize":false,"opModTargetW":{"value":-1500,"multiplier":1}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	_, _, stored, err := d.controls.ByMRID(t.Context(), decodeCreated(t, w).MRID)
	if err != nil {
		t.Fatal(err)
	}
	b := stored.DERControlBase
	if b.OpModConnect == nil || !*b.OpModConnect || b.OpModEnergize == nil || *b.OpModEnergize ||
		b.OpModTargetW == nil || *b.OpModTargetW != (sep2.ActivePower{Value: -1500, Multiplier: 1}) {
		t.Errorf("stored base = %+v, want connect true, energize false, targetW -1500e1", b)
	}
}

func TestDERControlCreate_FunctionsRefusals(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{"type and functions", `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","functions":{"opModConnect":true},"durationSeconds":300}`, "type and functions: give one, not both"},
		{"neither", `{"derProgramHref":"/edev/0/fsa/0/derp/0","durationSeconds":300}`, "type: required"},
		{"empty functions", functionsBody(0, `{}`), "functions: at least one function required"},
		{"unknown function", functionsBody(0, `{"opModBogus":1}`), "functions: unknown function"},
		{"not an object", functionsBody(0, `[1]`), "functions: must be an object"},
		{"wrong value type", functionsBody(0, `{"opModMaxLimW":"high"}`), "functions.opModMaxLimW: wrong value type"},
		{"null value", functionsBody(0, `{"opModMaxLimW":null}`), "functions.opModMaxLimW: wrong value type"},
		{"maxLimW range", functionsBody(0, `{"opModMaxLimW":10001}`), "maxLimW: must be 0 to 10000"},
		{"pf missing excitation", functionsBody(0, `{"opModFixedPFInjectW":{"displacement":900}}`), "powerFactor.excitation: required"},
		{"unknown key in value", functionsBody(0, `{"opModTargetW":{"value":1,"bogus":2}}`), "functions.opModTargetW: wrong value type"},
		{"top-level value beside functions", `{"derProgramHref":"/edev/0/fsa/0/derp/0","functions":{"opModConnect":true},"maxLimW":5,"durationSeconds":300}`, "maxLimW: not allowed with functions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			assertRefusal(t, w, http.StatusBadRequest, tc.message)
			if controls, lifecycles := d.storedCounts(t); controls != 0 || lifecycles != 0 {
				t.Errorf("a refused request stored %d controls and %d lifecycles", controls, lifecycles)
			}
		})
	}
}

func TestDERControlCreate_LegacyTypeViewUnchanged(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	for typ, want := range map[string]string{"connect": "connect", "disconnect": "disconnect"} {
		w := d.do(t, http.MethodPost, "/api/der/controls",
			fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":%q,"startTime":%d,"durationSeconds":300}`, typ, futureStart(600)))
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status %d body %s", typ, w.Code, w.Body.String())
		}
		got := decodeCreated(t, w)
		b := got.DERControlBase
		wantBool := typ == "connect"
		if got.Type != want || b.OpModConnect == nil || *b.OpModConnect != wantBool || b.OpModEnergize == nil || *b.OpModEnergize != wantBool {
			t.Errorf("%s: type = %q connect = %v energize = %v, want both elements %v", typ, got.Type, b.OpModConnect, b.OpModEnergize, wantBool)
		}
	}
}

func TestDERControlList_CarriesFunctionsAndEnded(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	if w := d.do(t, http.MethodPost, "/api/der/controls", functionsBody(futureStart(600), `{"opModMaxLimW":1000}`)); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	var list struct {
		Controls []struct {
			Functions map[string]json.RawMessage `json:"functions"`
			Ended     *bool                      `json:"ended"`
		} `json:"controls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Controls) != 1 {
		t.Fatalf("list = %s (%v), want one control", w.Body.String(), err)
	}
	c := list.Controls[0]
	if string(c.Functions["opModMaxLimW"]) != "1000" || c.Ended == nil || *c.Ended {
		t.Errorf("functions = %v ended = %v, want opModMaxLimW 1000 and ended false", c.Functions, c.Ended)
	}
}

func TestDERControlFunctions_ListsTheCatalog(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	d.mux.HandleFunc("GET /api/der/control-functions", d.h.HandleFunctions())
	w := d.do(t, http.MethodGet, "/api/der/control-functions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Functions []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"functions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got.Functions {
		if f.Value == "" {
			t.Errorf("%s: empty value shape", f.Name)
		}
		names = append(names, f.Name)
	}
	want := []string{"opModConnect", "opModEnergize", "opModMaxLimW", "opModFixedPFInjectW", "opModTargetW"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestDERControlList_EndedFollowsTheInterval(t *testing.T) {
	d := newDeliveryHarness(t)
	now := futureStart(0)
	d.seedControl(t, "past", now-600, 300, dercontrol.LifecycleRecord{})
	d.seedControl(t, "running", now-100, 300, dercontrol.LifecycleRecord{})
	w := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	var list struct {
		Controls []struct {
			Href  string `json:"href"`
			Ended bool   `json:"ended"`
		} `json:"controls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range list.Controls {
		got[c.Href] = c.Ended
	}
	want := map[string]bool{"/edev/0/fsa/0/derp/0/derc/past": true, "/edev/0/fsa/0/derp/0/derc/running": false}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ended by control = %v, want %v", got, want)
	}
}
