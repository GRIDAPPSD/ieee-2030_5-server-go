// Tests for the DER handler family.
// Ported from the reference server's internal/handler/der_test.go; no auth
// seam changes needed since DER handlers are auth-clean.
package der_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coredel "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestDERSingletonHandlersCapability: PUT then GET round-trips RTGMaxW.Value
// (data-invariants Rule 1: field-value assertion on the stored scalar).
func TestDERSingletonHandlersCapability(t *testing.T) {
	t.Parallel()

	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	dercap, _, _, _ := coredel.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dercap", dercap)

	// GET returns the default (empty) record.
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dercap", nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("GET default status = %d, want 200", getW.Code)
	}

	// PUT capability with RTGMaxW.
	maxW := sep2.ActivePower{Value: 10000}
	cap := sep2.DERCapability{RTGMaxW: &maxW}
	body, _ := xml.Marshal(&cap)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/dercap", bytes.NewReader(body)))
	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putW.Code)
	}

	// GET after PUT: field-value asserts on RTGMaxW.Value.
	getW2 := httptest.NewRecorder()
	mux.ServeHTTP(getW2, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dercap", nil))
	var got sep2.DERCapability
	if err := xml.Unmarshal(getW2.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal DERCapability: %v", err)
	}
	if got.RTGMaxW == nil || got.RTGMaxW.Value != 10000 {
		t.Errorf("RTGMaxW = %v, want Value=10000", got.RTGMaxW)
	}
}

// TestDERSingletonHandlersStatus: PUT then GET round-trips GenConnectStatus.Value.
func TestDERSingletonHandlersStatus(t *testing.T) {
	t.Parallel()

	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	_, _, ders, _ := coredel.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/ders", ders)

	// GET default before PUT: exercises the default-factory closure.
	defaultW := httptest.NewRecorder()
	mux.ServeHTTP(defaultW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/ders", nil))
	if defaultW.Code != http.StatusOK {
		t.Fatalf("GET default status = %d, want 200", defaultW.Code)
	}

	status := sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1, DateTime: 1604963587},
		ReadingTime:      1604963587,
	}
	body, _ := xml.Marshal(&status)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/ders", bytes.NewReader(body)))
	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putW.Code)
	}

	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/ders", nil))
	var got sep2.DERStatus
	if err := xml.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal DERStatus: %v", err)
	}
	if got.GenConnectStatus == nil || got.GenConnectStatus.Value != 1 {
		t.Errorf("GenConnectStatus = %v, want Value=1", got.GenConnectStatus)
	}
}

// TestDERSingletonHandlersSettings: PUT then GET round-trips DERSettings.
func TestDERSingletonHandlersSettings(t *testing.T) {
	t.Parallel()

	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	_, derg, _, _ := coredel.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/der/{derId}/derg", derg)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/derg", derg)

	// GET default before PUT: exercises the default-factory closure.
	defaultW := httptest.NewRecorder()
	mux.ServeHTTP(defaultW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/derg", nil))
	if defaultW.Code != http.StatusOK {
		t.Fatalf("GET default status = %d, want 200", defaultW.Code)
	}

	maxW := sep2.ActivePower{Value: 5000}
	derSet := sep2.DERSettings{SetMaxChargeRateW: &maxW}
	body, _ := xml.Marshal(&derSet)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/derg", bytes.NewReader(body)))
	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putW.Code)
	}

	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/derg", nil))
	var got sep2.DERSettings
	if err := xml.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal DERSettings: %v", err)
	}
	if got.SetMaxChargeRateW == nil || got.SetMaxChargeRateW.Value != 5000 {
		t.Errorf("SetMaxChargeRateW = %v, want Value=5000", got.SetMaxChargeRateW)
	}
}

// TestDERSingletonHandlersAvailability: PUT then GET round-trips DERAvailability.
func TestDERSingletonHandlersAvailability(t *testing.T) {
	t.Parallel()

	caps := memory.NewScopedStore[sep2.DERCapability]()
	settings := memory.NewScopedStore[sep2.DERSettings]()
	statuses := memory.NewScopedStore[sep2.DERStatus]()
	avails := memory.NewScopedStore[sep2.DERAvailability]()

	_, _, _, dera := coredel.DERSingletonHandlers(caps, settings, statuses, avails)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dera", dera)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dera", dera)

	// GET default before PUT: exercises the default-factory closure.
	defaultW := httptest.NewRecorder()
	mux.ServeHTTP(defaultW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dera", nil))
	if defaultW.Code != http.StatusOK {
		t.Fatalf("GET default status = %d, want 200", defaultW.Code)
	}

	dur := uint32(3600)
	avail := sep2.DERAvailability{AvailabilityDuration: &dur}
	body, _ := xml.Marshal(&avail)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/der/d1/dera", bytes.NewReader(body)))
	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putW.Code)
	}

	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/dera", nil))
	var got sep2.DERAvailability
	if err := xml.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal DERAvailability: %v", err)
	}
	if got.AvailabilityDuration == nil || *got.AvailabilityDuration != 3600 {
		t.Errorf("AvailabilityDuration = %v, want 3600", got.AvailabilityDuration)
	}
}

// TestDefaultDERControlHandler_GETServesTheStoredValue: the store is seeded
// directly (the utility-side path), and GET serves exactly what was seeded.
func TestDefaultDERControlHandler_GETServesTheStoredValue(t *testing.T) {
	t.Parallel()

	s := memory.NewScopedStore[sep2.DefaultDERControl]()
	connected := true
	seeded := sep2.DefaultDERControl{DERControlBase: &sep2.DERControlBase{OpModConnect: &connected}}
	if err := s.Create(context.Background(), "e1/f1/p1", "default", seeded); err != nil {
		t.Fatalf("seed DefaultDERControl: %v", err)
	}
	h := coredel.DefaultDERControlHandler(s)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", h)

	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/fsa/f1/derp/p1/dderc", nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getW.Code)
	}
	var got sep2.DefaultDERControl
	if err := xml.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal DefaultDERControl: %v", err)
	}
	if got.DERControlBase == nil || got.DERControlBase.OpModConnect == nil || !*got.DERControlBase.OpModConnect {
		t.Error("OpModConnect should be true, as seeded")
	}
}

// TestDefaultDERControlHandler_PUTIsRefusedAndLeavesTheStoreUntouched covers
// #456: DefaultDERControl is utility-set, so PUT is refused with 405 and the
// documented Allow set, and the store is never written even when a route
// mounts PUT to this handler directly.
func TestDefaultDERControlHandler_PUTIsRefusedAndLeavesTheStoreUntouched(t *testing.T) {
	t.Parallel()

	s := memory.NewScopedStore[sep2.DefaultDERControl]()
	connected := true
	original := sep2.DefaultDERControl{DERControlBase: &sep2.DERControlBase{OpModConnect: &connected}}
	if err := s.Create(context.Background(), "e1/f1/p1", "default", original); err != nil {
		t.Fatalf("seed DefaultDERControl: %v", err)
	}
	h := coredel.DefaultDERControlHandler(s)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", h)
	mux.HandleFunc("PUT /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc", h)

	forged := false
	body, _ := xml.Marshal(&sep2.DefaultDERControl{DERControlBase: &sep2.DERControlBase{OpModConnect: &forged}})

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/e1/fsa/f1/derp/p1/dderc", bytes.NewReader(body)))
	if putW.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", putW.Code)
	}
	if allow := putW.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}

	stored, err := s.Get(context.Background(), "e1/f1/p1", "default")
	if err != nil {
		t.Fatalf("read back DefaultDERControl: %v", err)
	}
	if stored.DERControlBase == nil || stored.DERControlBase.OpModConnect == nil || !*stored.DERControlBase.OpModConnect {
		t.Errorf("PUT changed the stored value: %+v, want OpModConnect still true", stored.DERControlBase)
	}
}

// TestBuildDERList: asserts All, Results, and slice length.
func TestBuildDERList(t *testing.T) {
	t.Parallel()

	result := store.ListResult[sep2.DER]{All: 3, Results: 2, Items: []sep2.DER{{}, {}}}
	list := coredel.BuildDERList("/edev/1/der", result, 900)
	if list.All != 3 {
		t.Errorf("All = %d, want 3", list.All)
	}
	if list.Results != 2 {
		t.Errorf("Results = %d, want 2", list.Results)
	}
	if len(list.DER) != 2 {
		t.Errorf("len(DER) = %d, want 2", len(list.DER))
	}
}

func TestBuildDERProgramList(t *testing.T) {
	t.Parallel()

	result := store.ListResult[sep2.DERProgram]{All: 1, Results: 1, Items: []sep2.DERProgram{{}}}
	list := coredel.BuildDERProgramList("/derp", result, 900)
	if list.All != 1 {
		t.Errorf("All = %d, want 1", list.All)
	}
	if list.Results != 1 {
		t.Errorf("Results = %d, want 1", list.Results)
	}
}

// TestDERProgramHref asserts the canonical builder emits the FSA-nested
// shape core actually mounts a DERProgram under: dropping the {fsaId}
// segment reproduced the exact defect found independently by Devi's WADL
// sweep and Frank's boot-fixture testing.
func TestDERProgramHref(t *testing.T) {
	t.Parallel()

	got := coredel.DERProgramHref("e1", "f1", "p1")
	want := "/edev/e1/fsa/f1/derp/p1"
	if got != want {
		t.Errorf("DERProgramHref() = %q, want %q", got, want)
	}
}

func TestBuildDERControlList(t *testing.T) {
	t.Parallel()

	result := store.ListResult[sep2.DERControl]{All: 0, Results: 0}
	list := coredel.BuildDERControlList("/derc", result, 900)
	if list.All != 0 {
		t.Errorf("All = %d, want 0", list.All)
	}
}

func TestBuildDERCurveList(t *testing.T) {
	t.Parallel()

	result := store.ListResult[sep2.DERCurve]{All: 2, Results: 2, Items: []sep2.DERCurve{{}, {}}}
	list := coredel.BuildDERCurveList("/dc", result, 900)
	if list.All != 2 {
		t.Errorf("All = %d, want 2", list.All)
	}
	if len(list.DERCurve) != 2 {
		t.Errorf("len(DERCurve) = %d, want 2", len(list.DERCurve))
	}
}
