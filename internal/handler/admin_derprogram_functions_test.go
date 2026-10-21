package handler_test

import (
	"encoding/json"
	"net/http"
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
