package handler_test

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

func TestDERSingletonHandlersCapability(t *testing.T) {
	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	dercap, _, _, _ := handler.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dercap", dercap)

	// GET returns default (empty)
	req := httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dercap", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("GET default status = %d", w.Code)
	}

	// PUT capability
	maxW := sep2.ActivePower{Value: 10000}
	cap := sep2.DERCapability{RTGMaxW: &maxW}
	body, _ := xml.Marshal(&cap)

	putReq := httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/dercap", bytes.NewReader(body))
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, putReq)

	if putW.Code != 204 {
		t.Fatalf("PUT status = %d", putW.Code)
	}

	// GET should return stored value
	getReq := httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dercap", nil)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)

	var got sep2.DERCapability
	_ = xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.RTGMaxW == nil || got.RTGMaxW.Value != 10000 {
		t.Errorf("RTGMaxW = %v", got.RTGMaxW)
	}
}

func TestDERSingletonHandlersStatus(t *testing.T) {
	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	_, _, ders, _ := handler.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/ders", ders)

	// PUT status
	status := sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1, DateTime: 1604963587},
		ReadingTime:      1604963587,
	}
	body, _ := xml.Marshal(&status)

	putReq := httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/ders", bytes.NewReader(body))
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, putReq)

	if putW.Code != 204 {
		t.Fatalf("PUT status = %d", putW.Code)
	}

	// GET
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/ders", nil))

	var got sep2.DERStatus
	_ = xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.GenConnectStatus == nil || got.GenConnectStatus.Value != 1 {
		t.Errorf("GenConnectStatus = %v", got.GenConnectStatus)
	}
}

func TestDefaultDERControlHandler(t *testing.T) {
	store := memory.NewScopedStore[sep2.DefaultDERControl]()
	h := handler.DefaultDERControlHandler(store)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", h)
	mux.HandleFunc("PUT /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", h)

	// GET returns default
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/fsa/f1/derp/p1/dderc", nil))

	if getW.Code != 200 {
		t.Fatalf("GET default status = %d", getW.Code)
	}

	// PUT DefaultDERControl
	connected := true
	dderc := sep2.DefaultDERControl{
		DERControlBase: &sep2.DERControlBase{OpModConnect: &connected},
	}
	body, _ := xml.Marshal(&dderc)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/fsa/f1/derp/p1/dderc", bytes.NewReader(body)))

	if putW.Code != 204 {
		t.Fatalf("PUT status = %d", putW.Code)
	}

	// GET returns stored
	getW2 := httptest.NewRecorder()
	mux.ServeHTTP(getW2, httptest.NewRequest(http.MethodGet, "/edev/e1/fsa/f1/derp/p1/dderc", nil))

	var got sep2.DefaultDERControl
	_ = xml.Unmarshal(getW2.Body.Bytes(), &got)
	if got.DERControlBase == nil || got.DERControlBase.OpModConnect == nil || !*got.DERControlBase.OpModConnect {
		t.Error("OpModConnect should be true after PUT")
	}
}

func TestBuildDERList(t *testing.T) {
	result := store.ListResult[sep2.DER]{All: 3, Results: 2, Items: []sep2.DER{{}, {}}}
	list := handler.BuildDERList("/edev/1/der", result, 900)

	if list.All != 3 || list.Results != 2 || len(list.DER) != 2 {
		t.Errorf("list = All:%d Results:%d Items:%d", list.All, list.Results, len(list.DER))
	}
}

func TestBuildDERProgramList(t *testing.T) {
	result := store.ListResult[sep2.DERProgram]{All: 1, Results: 1, Items: []sep2.DERProgram{{}}}
	list := handler.BuildDERProgramList("/derp", result, 900)

	if list.All != 1 {
		t.Errorf("All = %d", list.All)
	}
}

func TestBuildDERControlList(t *testing.T) {
	result := store.ListResult[sep2.DERControl]{All: 0, Results: 0}
	list := handler.BuildDERControlList("/derc", result, 900)

	if list.All != 0 {
		t.Errorf("All = %d", list.All)
	}
}

func TestBuildDERCurveList(t *testing.T) {
	result := store.ListResult[sep2.DERCurve]{All: 2, Results: 2, Items: []sep2.DERCurve{{}, {}}}
	list := handler.BuildDERCurveList("/dc", result, 900)

	if list.All != 2 || len(list.DERCurve) != 2 {
		t.Errorf("list = All:%d Items:%d", list.All, len(list.DERCurve))
	}
}
