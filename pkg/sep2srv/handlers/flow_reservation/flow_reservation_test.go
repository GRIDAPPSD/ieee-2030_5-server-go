package flow_reservation_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandlePostFlowReservationRequest_ResponseCarriesCreationTime asserts the
// auto-approved FlowReservationResponse gets a real creationTime.
//
// FlowReservationResponse embeds RandomizableEvent, so it inherits Event's
// required creationTime. The field has no omitempty by design: an unset value
// serializes as a visible <creationTime>0</creationTime> rather than vanishing.
// That makes a missing producer parseable instead of loud, which is worse, not
// better: IEEE 2030.5 resolves two overlapping events of equal primacy by
// comparing creationTime, and the EPRI reference client's block_supersede tests
// x->creationTime > y->creationTime. With both events at 0 the comparison is
// false in BOTH directions, neither event wins, and the INCOMING one is
// discarded. A server that leaves creationTime unset cannot replace a control it
// already issued.
//
// So the assertion here is deliberately NOT "the element is present": that would
// pass against the broken code. It asserts the value is non-zero and inside the
// window the request was served in.
func TestHandlePostFlowReservationRequest_ResponseCarriesCreationTime(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore))

	energy := sep2.SignedRealEnergy{Value: 10000}
	frq := sep2.FlowReservationRequest{
		MRID:            "FRQ001",
		EnergyRequested: &energy,
	}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	before := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	after := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// The request path already set its own creationTime; assert it still does,
	// so a regression there is caught by the same test that covers the response.
	stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored requests: %v", err)
	}
	if len(stored.Items) != 1 {
		t.Fatalf("stored request count = %d, want 1", len(stored.Items))
	}
	if ct := stored.Items[0].CreationTime; ct < before || ct > after {
		t.Errorf("stored request CreationTime = %d, want server clock in [%d, %d]", ct, before, after)
	}

	// The auto-approved response is the card's subject: it was constructed
	// without a creationTime producer.
	responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(responses.Items) != 1 {
		t.Fatalf("stored response count = %d, want 1", len(responses.Items))
	}
	frp := responses.Items[0]
	if frp.CreationTime == 0 {
		t.Errorf("stored FlowReservationResponse CreationTime = 0; a client comparing creationTime to resolve supersession will discard the incoming event in both directions")
	}
	if frp.CreationTime < before || frp.CreationTime > after {
		t.Errorf("stored FlowReservationResponse CreationTime = %d, want server clock in [%d, %d]", frp.CreationTime, before, after)
	}

	// Same assertion at the wire level: the value a client actually parses.
	// Marshal the stored resource the way a GET /edev/{id}/frp/{id} would.
	served, err := xml.Marshal(&frp)
	if err != nil {
		t.Fatalf("marshal stored response: %v", err)
	}
	if strings.Contains(string(served), "<creationTime>0</creationTime>") {
		t.Errorf("served FlowReservationResponse carries <creationTime>0</creationTime>:\n%s", served)
	}
}

// TestHandlePostResponse_CarriesCreatedDateTime covers the sibling Response
// resource on the same handler file. Response is NOT Event-derived: it carries
// createdDateTime, a distinct field. It has always had a producer; this locks
// that in alongside the Event-derived assertion above.
func TestHandlePostResponse_CarriesCreatedDateTime(t *testing.T) {
	t.Parallel()
	rspStore := memory.NewScopedStore[sep2.Response]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rsps/{rspsId}/rsp", flow_reservation.HandlePostResponse(rspStore))

	rsp := sep2.Response{Subject: "SUBJ001"}
	body, err := xml.Marshal(&rsp)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	before := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/rsps/set1/rsp", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	after := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	stored, err := rspStore.List(context.Background(), "set1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(stored.Items) != 1 {
		t.Fatalf("stored response count = %d, want 1", len(stored.Items))
	}
	if ct := stored.Items[0].CreatedDateTime; ct < before || ct > after {
		t.Errorf("stored Response CreatedDateTime = %d, want server clock in [%d, %d]", ct, before, after)
	}
}

// TestHandlePostFlowReservationRequest_InvalidXMLDoesNotLeakDecoderDetail pins
// the 400 path convention (#360): the body is a fixed message and the
// decoder's own complaint, which can quote attacker-supplied content, goes to
// the operator-facing log only.
func TestHandlePostFlowReservationRequest_InvalidXMLDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore))

	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", strings.NewReader("<"+marker+">bar</"+marker+">"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", body)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// TestHandlePostResponse_InvalidXMLDoesNotLeakDecoderDetail is
// HandlePostResponse's half of the 400 path convention (#360): DecodeResponse
// names the offending root element in its error, which is attacker-supplied
// content, so the client-visible body must stay fixed.
func TestHandlePostResponse_InvalidXMLDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	rspStore := memory.NewScopedStore[sep2.Response]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rsps/{rspsId}/rsp", flow_reservation.HandlePostResponse(rspStore))

	req := httptest.NewRequest(http.MethodPost, "/rsps/set1/rsp", strings.NewReader("<"+marker+">bar</"+marker+">"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "invalid XML" {
		t.Errorf("body = %q, want the fixed message", got)
	}
	if body := w.Body.String(); strings.Contains(body, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", body)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}
