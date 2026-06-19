package handler_test

// Regression tests for IEEE-005 (GitHub Issue #4).
//
// Lock in the contract that POST /edev/{id}/frq:
//
//   - On success, returns 201 Created with a Location header pointing at
//     the created FlowReservationRequest and an XML body that round-trips
//     back into a FlowReservationRequest with the matching Href.
//   - When the FlowReservationResponse store's Create fails, the handler
//     surfaces 500 Internal Server Error with an opaque "internal error"
//     body, the underlying cause is logged server-side, and no Location
//     header leaks to the client. Exercised via the exported FRPCreator
//     interface (see flow_reservation.go). Matches the FRQ branch above
//     and the IEEE-009 5xx-logging discipline (see log5xx_test.go).

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
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

// failingFRPStore is a fake that satisfies handler.FRPCreator and always
// returns errFRPCreate from Create. Used to exercise the
// 500-on-frpStore-failure branch in HandlePostFlowReservationRequest.
type failingFRPStore struct {
	calls int
}

var errFRPCreate = errors.New("simulated frp store failure")

func (f *failingFRPStore) Create(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error {
	f.calls++
	return errFRPCreate
}

// TestHandlePostFlowReservationRequest_StoreCreateFailureReturns500 locks
// in the contract that a frpStore.Create failure surfaces as 500
// Internal Server Error with an OPAQUE "internal error" body (matching
// the FRQ branch above), the underlying cause is captured in the
// server-side log, and no Location header leaks. Regression for
// IEEE-005 (GitHub Issue #4); body-opacity guard for PR #241 review
// round 1 (Leon, MEDIUM).
func TestHandlePostFlowReservationRequest_StoreCreateFailureReturns500(t *testing.T) {
	// NOTE: not t.Parallel() — captureLog mutates the package-global
	// log writer, which would race with other parallel tests.

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := &failingFRPStore{}
	h := handler.HandlePostFlowReservationRequest(frqStore, frpStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", h)

	energy := sep2.SignedRealEnergy{Value: 1000}
	power := sep2.ActivePower{Value: 500}
	frq := sep2.FlowReservationRequest{
		MRID:            "frq-failure-probe",
		EnergyRequested: &energy,
		PowerRequested:  &power,
	}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/edev/dev99/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()

	logOutput := captureLog(func() { mux.ServeHTTP(w, req) })

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	// Wire body must be opaque. http.Error appends a newline.
	if got, want := w.Body.String(), "internal error\n"; got != want {
		t.Errorf("body = %q, want %q (opaque, matching FRQ branch)", got, want)
	}
	// And explicitly: the underlying error string must NOT be on the wire.
	if strings.Contains(w.Body.String(), errFRPCreate.Error()) {
		t.Errorf("body = %q, must NOT leak underlying error %q to mTLS client", w.Body.String(), errFRPCreate.Error())
	}
	// Server-side log must contain the wrapped cause for ops.
	if !strings.Contains(logOutput, errFRPCreate.Error()) {
		t.Errorf("log output does not contain underlying error %q\ngot: %q", errFRPCreate.Error(), logOutput)
	}
	if frpStore.calls != 1 {
		t.Errorf("failingFRPStore.calls = %d, want 1", frpStore.calls)
	}
	// FRQ store should still have the request (frq.Create succeeded before frp.Create failed).
	if frqCount, _ := frqStore.Count(context.Background(), "dev99"); frqCount != 1 {
		t.Errorf("frqStore count for dev99 = %d, want 1", frqCount)
	}
	// 500 path must not have written a Location header.
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty on 500", loc)
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
