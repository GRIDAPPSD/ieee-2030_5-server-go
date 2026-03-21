package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// --- DeviceInformation ---

func TestHandleDeviceInformation(t *testing.T) {
	h := handler.HandleDeviceInformation("AABBCCDD")
	req := httptest.NewRequest(http.MethodGet, "/sdev/sdi", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "DeviceInformation") {
		t.Error("missing DeviceInformation element")
	}
	if !strings.Contains(w.Body.String(), "AABBCCDD") {
		t.Error("missing LFDI")
	}
}

// --- Configuration ---

func TestHandleConfiguration(t *testing.T) {
	store := memory.NewScopedStore[sep2.Configuration]()
	h := handler.HandleConfiguration(store)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/cfg", h)
	mux.HandleFunc("PUT /edev/{id}/cfg", h)

	// GET returns default
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edev/dev1/cfg", nil))
	if w.Code != 200 {
		t.Fatalf("GET default status = %d", w.Code)
	}

	// PUT configuration
	cfg := sep2.Configuration{CurrentLocale: "en-US", UserDeviceName: "Test Device"}
	body, _ := xml.Marshal(&cfg)
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/dev1/cfg", bytes.NewReader(body)))
	if putW.Code != 204 {
		t.Fatalf("PUT status = %d", putW.Code)
	}

	// GET returns stored
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/dev1/cfg", nil))
	var got sep2.Configuration
	xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.CurrentLocale != "en-US" {
		t.Errorf("CurrentLocale = %q", got.CurrentLocale)
	}
}

// --- LogEvent ---

func TestHandlePostLogEvent(t *testing.T) {
	logStore := memory.NewScopedStore[sep2.LogEvent]()
	h := handler.HandlePostLogEvent(logStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/log", h)

	le := sep2.LogEvent{FunctionSet: sep2.FunctionSetDER, LogEventCode: 0x02, LogEventID: 1}
	body, _ := xml.Marshal(&le)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/edev/dev1/log", bytes.NewReader(body)))

	if w.Code != 201 {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("Location") == "" {
		t.Error("missing Location header")
	}

	count, _ := logStore.Count(context.Background(), "dev1")
	if count != 1 {
		t.Errorf("log event count = %d", count)
	}
}

func TestBuildLogEventList(t *testing.T) {
	result := store.ListResult[sep2.LogEvent]{All: 2, Results: 2, Items: []sep2.LogEvent{{}, {}}}
	list := handler.BuildLogEventList("/edev/1/log", result, 900)
	if list.All != 2 || len(list.LogEvent) != 2 {
		t.Errorf("list All=%d Items=%d", list.All, len(list.LogEvent))
	}
}

// --- PowerStatus ---

func TestHandlePowerStatus(t *testing.T) {
	psStore := memory.NewScopedStore[sep2.PowerStatus]()
	h := handler.HandlePowerStatus(psStore)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/ps", h)
	mux.HandleFunc("PUT /edev/{id}/ps", h)

	// PUT power status
	ps := sep2.PowerStatus{CurrentPowerSource: sep2.PowerSourceMains, ChangedTime: 1000}
	body, _ := xml.Marshal(&ps)
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(http.MethodPut, "/edev/dev1/ps", bytes.NewReader(body)))
	if putW.Code != 204 {
		t.Fatalf("PUT status = %d", putW.Code)
	}

	// GET returns stored
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/dev1/ps", nil))
	var got sep2.PowerStatus
	xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.CurrentPowerSource != sep2.PowerSourceMains {
		t.Errorf("CurrentPowerSource = %d", got.CurrentPowerSource)
	}
}

// --- Messaging ---

func TestHandleMessagingProgram(t *testing.T) {
	msgStore := memory.NewStore[sep2.MessagingProgram]()
	msgStore.Create(context.Background(), "msg1", sep2.MessagingProgram{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/msg/msg1"}},
		MRID: "msg1", Primacy: 1,
	})

	h := handler.HandleMessagingProgram(msgStore)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /msg/{msgId}", h)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/msg/msg1", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlePostTextMessage(t *testing.T) {
	tmStore := memory.NewScopedStore[sep2.TextMessage]()
	h := handler.HandlePostTextMessage(tmStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /msg/{msgId}/tm", h)

	tm := sep2.TextMessage{TextBody: "Test alert", Priority: sep2.PriorityHigh}
	body, _ := xml.Marshal(&tm)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/msg/msg1/tm", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
}

func TestBuildMessagingProgramList(t *testing.T) {
	result := store.ListResult[sep2.MessagingProgram]{All: 1, Results: 1, Items: []sep2.MessagingProgram{{}}}
	list := handler.BuildMessagingProgramList("/msg", result, 900)
	if list.All != 1 {
		t.Errorf("All = %d", list.All)
	}
}

// --- Flow Reservation ---

func TestHandlePostFlowReservationRequest(t *testing.T) {
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	h := handler.HandlePostFlowReservationRequest(frqStore, frpStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", h)

	energy := sep2.SignedRealEnergy{Value: 5000}
	power := sep2.ActivePower{Value: 3000}
	frq := sep2.FlowReservationRequest{
		MRID:            "frq-test",
		EnergyRequested: &energy,
		PowerRequested:  &power,
	}
	body, _ := xml.Marshal(&frq)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	// Verify auto-created response
	frpCount, _ := frpStore.Count(context.Background(), "dev1")
	if frpCount != 1 {
		t.Errorf("auto-created response count = %d, want 1", frpCount)
	}
}

func TestBuildFlowReservationRequestList(t *testing.T) {
	result := store.ListResult[sep2.FlowReservationRequest]{All: 1, Results: 1}
	list := handler.BuildFlowReservationRequestList("/frq", result, 900)
	if list.All != 1 {
		t.Errorf("All = %d", list.All)
	}
}

// --- Response ---

func TestHandlePostResponse(t *testing.T) {
	rspStore := memory.NewScopedStore[sep2.Response]()
	h := handler.HandlePostResponse(rspStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rsps/{rspsId}/rsp", h)

	status := uint8(sep2.ResponseStatusEventReceived)
	rsp := sep2.Response{Subject: "event-001", Status: &status}
	body, _ := xml.Marshal(&rsp)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/rsps/set1/rsp", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestBuildResponseSetList(t *testing.T) {
	result := store.ListResult[sep2.ResponseSet]{All: 2, Results: 2}
	list := handler.BuildResponseSetList("/rsps", result, 900)
	if list.All != 2 {
		t.Errorf("All = %d", list.All)
	}
}

// --- Metering ---

func TestHandleCreateUsagePoint(t *testing.T) {
	uptStore := memory.NewStore[sep2.UsagePoint]()
	h := handler.HandleCreateUsagePoint(uptStore)

	upt := sep2.UsagePoint{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{}},
		MRID: "upt-test", Description: "Test Meter", ServiceCategoryKind: 0, Status: 1,
	}
	body, _ := xml.Marshal(&upt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upt", bytes.NewReader(body))
	h.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Location") == "" {
		t.Error("missing Location header")
	}
}

func TestHandleUsagePoint(t *testing.T) {
	uptStore := memory.NewStore[sep2.UsagePoint]()
	upt := sep2.UsagePoint{MRID: "upt1", Description: "Meter"}
	upt.Href = "/upt/upt1"
	uptStore.Create(context.Background(), "upt1", upt)

	h := handler.HandleUsagePoint(uptStore)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt/{uptId}", h)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/upt/upt1", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleUsagePointNotFound(t *testing.T) {
	uptStore := memory.NewStore[sep2.UsagePoint]()
	h := handler.HandleUsagePoint(uptStore)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt/{uptId}", h)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/upt/missing", nil))
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestBuildUsagePointList(t *testing.T) {
	result := store.ListResult[sep2.UsagePoint]{All: 3, Results: 2, Items: []sep2.UsagePoint{{}, {}}}
	list := handler.BuildUsagePointList("/upt", result, 900)
	if list.All != 3 || list.Results != 2 {
		t.Errorf("All=%d Results=%d", list.All, list.Results)
	}
}

func TestBuildMeterReadingList(t *testing.T) {
	result := store.ListResult[sep2.MeterReading]{All: 1, Results: 1}
	list := handler.BuildMeterReadingList("/mr", result, 900)
	if list.All != 1 {
		t.Errorf("All = %d", list.All)
	}
}

func TestBuildReadingList(t *testing.T) {
	result := store.ListResult[sep2.Reading]{All: 5, Results: 5}
	list := handler.BuildReadingList("/r", result, 900)
	if list.All != 5 {
		t.Errorf("All = %d", list.All)
	}
}

func TestBuildReadingTypeList(t *testing.T) {
	result := store.ListResult[sep2.ReadingType]{All: 2, Results: 2}
	list := handler.BuildReadingTypeList("/rt", result, 900)
	if list.All != 2 {
		t.Errorf("All = %d", list.All)
	}
}
