package handler_test

// Regression tests for IEEE-005 (GitHub Issue #4).
//
// Lock in the contract that POST /edev/{id}/frq:
//
//   - On success, returns 201 Created with a Location header pointing at
//     the created FlowReservationRequest and an XML body that round-trips
//     back into a FlowReservationRequest with the matching Href.
//
// The companion contract — that a frpStore.Create failure surfaces as
// 500 Internal Server Error rather than a silent 201 — is asserted at
// internal/handler/flow_reservation.go:94 but cannot be exercised here
// without a production-code change. HandlePostFlowReservationRequest
// takes a concrete *memory.ScopedStore[T], not an interface, and the
// FRP id is generated inside the handler from time.Now().UnixNano(),
// so a fresh ScopedStore can't be made to fail Create deterministically
// from a test. See IEEE-005 follow-up notes for the deferred refactor.

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// TestHandlePostFlowReservationRequest_SuccessPathAssertions strengthens
// the existing TestHandlePostFlowReservationRequest in
// new_function_sets_test.go by asserting the full 201-response contract:
// status, Location header, body XML round-trip, and the FRQ store side
// effect — not just the auto-created FRP count.
func TestHandlePostFlowReservationRequest_SuccessPathAssertions(t *testing.T) {
	t.Parallel()

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	h := handler.HandlePostFlowReservationRequest(frqStore, frpStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", h)

	energy := sep2.SignedRealEnergy{Value: 7500}
	power := sep2.ActivePower{Value: 4200}
	frq := sep2.FlowReservationRequest{
		MRID:            "frq-success-probe",
		EnergyRequested: &energy,
		PowerRequested:  &power,
	}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/edev/dev42/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/edev/dev42/frq/frq-") {
		t.Errorf("Location = %q, want /edev/dev42/frq/frq-...", loc)
	}

	var got sep2.FlowReservationRequest
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if got.Href != loc {
		t.Errorf("body Href = %q, want %q (matches Location)", got.Href, loc)
	}
	if got.MRID != "frq-success-probe" {
		t.Errorf("body MRID = %q, want %q", got.MRID, "frq-success-probe")
	}
	if got.CreationTime == 0 {
		t.Errorf("body CreationTime is zero — handler must stamp creation time")
	}

	// FRQ side effect: the request was persisted under edev "dev42".
	if frqCount, _ := frqStore.Count(context.Background(), "dev42"); frqCount != 1 {
		t.Errorf("frqStore count for dev42 = %d, want 1", frqCount)
	}
	// FRP side effect: the auto-approved response was persisted.
	if frpCount, _ := frpStore.Count(context.Background(), "dev42"); frpCount != 1 {
		t.Errorf("frpStore count for dev42 = %d, want 1", frpCount)
	}
}

// TestHandlePostFlowReservationRequest_InvalidXMLReturns400 covers the
// xml.Unmarshal error branch (line 65-68 of flow_reservation.go).
func TestHandlePostFlowReservationRequest_InvalidXMLReturns400(t *testing.T) {
	t.Parallel()

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	h := handler.HandlePostFlowReservationRequest(frqStore, frpStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", h)

	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewBufferString("<not-xml"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	// 400 path must not have written a Location header.
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty on 400", loc)
	}
}

// TestHandlePostFlowReservationRequest_NonPostReturns405 covers the
// method-guard branch (line 52-55 of flow_reservation.go).
func TestHandlePostFlowReservationRequest_NonPostReturns405(t *testing.T) {
	t.Parallel()

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	h := handler.HandlePostFlowReservationRequest(frqStore, frpStore)

	// Bypass the mux's method routing by calling the HandlerFunc directly
	// with a non-POST request. This exercises the in-handler guard.
	req := httptest.NewRequest(http.MethodGet, "/edev/dev1/frq", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
}
