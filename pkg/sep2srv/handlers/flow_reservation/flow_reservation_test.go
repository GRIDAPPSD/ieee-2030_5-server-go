package flow_reservation_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// stubSubmitter records every Submit call instead of scheduling a real
// deadline fallback: #666 moved response-building (mRID, subject, status,
// the deadline itself) into internal/flowreservation.Queue, which has its
// own tests. This package's tests only need to prove the handler stores the
// request correctly and hands it to Submitter exactly once, or not at all
// when validation refuses the body.
type stubSubmitter struct {
	mu    sync.Mutex
	calls []submitCall
}

type submitCall struct {
	edevID, frqID string
	frq           sep2.FlowReservationRequest
	createdAt     int64
}

func (s *stubSubmitter) Submit(edevID, frqID string, frq sep2.FlowReservationRequest, createdAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, submitCall{edevID, frqID, frq, createdAt})
}

func (s *stubSubmitter) submitted() []submitCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]submitCall(nil), s.calls...)
}

// TestHandlePostFlowReservationRequest_SubmitsStoredRequestToQueue is #666's
// first criterion at the handler's boundary: POST stores the request, stamps
// its own creationTime, and hands it to Submitter exactly once with the
// same createdAt, building no response itself.
func TestHandlePostFlowReservationRequest_SubmitsStoredRequestToQueue(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	sub := &stubSubmitter{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

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

	calls := sub.submitted()
	if len(calls) != 1 {
		t.Fatalf("Submit calls = %d, want 1; the handler must build no response itself", len(calls))
	}
	call := calls[0]
	if call.edevID != "dev1" {
		t.Errorf("Submit edevID = %q, want %q", call.edevID, "dev1")
	}
	if call.frq.MRID != "FRQ001" {
		t.Errorf("Submit frq.MRID = %q, want %q", call.frq.MRID, "FRQ001")
	}
	if call.createdAt != stored.Items[0].CreationTime {
		t.Errorf("Submit createdAt = %d, want the stored request's CreationTime %d", call.createdAt, stored.Items[0].CreationTime)
	}
}

// TestHandlePostResponse_CarriesCreatedDateTime covers the sibling Response
// resource on the same handler file. Response is NOT Event-derived: it carries
// createdDateTime, a distinct field. It has always had a producer; this locks
// that in alongside the request-side assertion above.
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
	sub := &stubSubmitter{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

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
	if len(sub.submitted()) != 0 {
		t.Errorf("Submit calls = %d, want 0 for a refused body", len(sub.submitted()))
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

// TestHandlePostFlowReservationRequest_MissingRequestMRIDRefused pins #665's
// fourth criterion: a request with no mRID is refused with 400 rather than
// stored and handed to the queue for a response nobody could address.
func TestHandlePostFlowReservationRequest_MissingRequestMRIDRefused(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	sub := &stubSubmitter{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

	energy := sep2.SignedRealEnergy{Value: 5000}
	frq := sep2.FlowReservationRequest{EnergyRequested: &energy} // no MRID
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
	}

	stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored requests: %v", err)
	}
	if len(stored.Items) != 0 {
		t.Errorf("stored request count = %d, want 0; a request with no mRID must not be stored", len(stored.Items))
	}
	if len(sub.submitted()) != 0 {
		t.Errorf("Submit calls = %d, want 0; no response should ever be built for a refused request", len(sub.submitted()))
	}
}

// TestHandlePostFlowReservationRequest_WhitespaceOnlyMRIDRefused extends the
// missing-mRID criterion: encoding/xml does not trim element content, so
// "<mRID>   </mRID>" unmarshals to a non-empty string that the bare "== """
// check let through, storing a request whose subject is meaningless
// whitespace. A whitespace-only mRID carries no more identity than an absent
// one and must be refused the same way.
//
// The body carries a valid RequestStatus (#692 fix round 1): without one, the
// RequestStatus check refuses the body first, and the 400 this test asserts
// would no longer say anything about the mRID path it is named for. The
// response body is asserted, not just the status, so a regression that swaps
// in a different 400 (RequestStatus's, say) is caught rather than passing for
// the wrong reason a second time.
func TestHandlePostFlowReservationRequest_WhitespaceOnlyMRIDRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mrid string
	}{
		{"spaces", "   "},
		{"newline", "\n"},
		{"tab", "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
			sub := &stubSubmitter{}

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

			body := `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>` + tc.mrid + `</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`
			req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", strings.NewReader(body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
			}
			if got := strings.TrimSpace(w.Body.String()); got != "FlowReservationRequest mRID is required" {
				t.Errorf("body = %q, want the mRID-specific message: a 400 for any other reason does not prove this path was reached", got)
			}

			stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list stored requests: %v", err)
			}
			if len(stored.Items) != 0 {
				t.Errorf("stored request count = %d, want 0; a whitespace-only mRID must not be stored", len(stored.Items))
			}
			if len(sub.submitted()) != 0 {
				t.Errorf("Submit calls = %d, want 0", len(sub.submitted()))
			}
		})
	}
}

// TestHandlePostFlowReservationRequest_RequestStatusRefused is #692's three
// acceptance criteria: a body with no RequestStatus, an out-of-range
// requestStatus, or a negative dateTime is refused, storing neither the
// request nor handing it to the queue.
//
// Each body is hand-crafted XML, not marshalled from sep2.FlowReservationRequest:
// RequestStatus is a value field there, so a struct literal always emits it
// with both children present, which cannot express "omitted" at all (#692).
func TestHandlePostFlowReservationRequest_RequestStatusRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "RequestStatus element absent",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID></FlowReservationRequest>`,
		},
		{
			name: "dateTime child absent",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "requestStatus child absent",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "requestStatus out of the defined set",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime><requestStatus>200</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "dateTime negative",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>-5</dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			// Boundary: -1 is refused by "< 0" but not by a mutant widened
			// to "< -4"; -5 alone cannot tell the two apart.
			name: "dateTime negative boundary",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>-1</dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			// Boundary: 2 is refused by the defined set {0, 1} but not by a
			// mutant widened to case 0, 1, 2; 200 alone cannot tell the two
			// apart.
			name: "requestStatus boundary just past the defined set",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime><requestStatus>2</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			// A dateTime this far ahead of the server clock cannot be "the
			// time at which the status change occurred": it is billions of
			// years in the future, not a client running a few seconds fast.
			name: "dateTime far in the future",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>9000000000000000000</dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			// encoding/xml allocates the pointer and sets 0 for a present
			// but empty numeric element (GOROOT src/encoding/xml/read.go:
			// 639-656), so a struct-only presence check cannot tell this
			// from a client that literally sent dateTime 0. The document
			// itself can: the child is present with no character data.
			name: "dateTime self-closed",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime/><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "dateTime open-close empty",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime></dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			// A comment carries no character data either, so this decodes
			// identically to the open-close-empty case above.
			name: "dateTime comment-only",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime><!-- x --></dateTime><requestStatus>0</requestStatus></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "requestStatus self-closed",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime><requestStatus/></RequestStatus></FlowReservationRequest>`,
		},
		{
			name: "requestStatus open-close empty",
			body: `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>` +
				`<RequestStatus><dateTime>1727136000</dateTime><requestStatus></requestStatus></RequestStatus></FlowReservationRequest>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
			sub := &stubSubmitter{}

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

			req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
			}

			stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list stored requests: %v", err)
			}
			if len(stored.Items) != 0 {
				t.Errorf("stored request count = %d, want 0; an invalid RequestStatus must not be stored", len(stored.Items))
			}
			if len(sub.submitted()) != 0 {
				t.Errorf("Submit calls = %d, want 0", len(sub.submitted()))
			}
		})
	}
}

// TestHandlePostFlowReservationRequest_RequestStatusValidValuesAccepted is
// the acceptance-side complement: requestStatus 0 (Requested) and 1
// (Cancelled) are both defined values and neither is refused. Asserts the
// stored request carries the posted values, not just a 201, so a handler
// that accepted the body but dropped RequestStatus on the way to the store
// would still fail this.
func TestHandlePostFlowReservationRequest_RequestStatusValidValuesAccepted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status uint8
	}{
		{"Requested", sep2.RequestStatusRequested},
		{"Cancelled", sep2.RequestStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
			sub := &stubSubmitter{}

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, sub))

			body := fmt.Sprintf(
				`<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>FRQ001</mRID>`+
					`<RequestStatus><dateTime>1727136000</dateTime><requestStatus>%d</requestStatus></RequestStatus></FlowReservationRequest>`,
				tc.status,
			)
			req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", strings.NewReader(body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
			}

			stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list stored requests: %v", err)
			}
			if len(stored.Items) != 1 {
				t.Fatalf("stored request count = %d, want 1", len(stored.Items))
			}
			if got := stored.Items[0].RequestStatus.DateTime; got != 1727136000 {
				t.Errorf("stored RequestStatus.dateTime = %d, want 1727136000", got)
			}
			if got := stored.Items[0].RequestStatus.RequestStatus; got != tc.status {
				t.Errorf("stored RequestStatus.requestStatus = %d, want %d", got, tc.status)
			}

			calls := sub.submitted()
			if len(calls) != 1 {
				t.Fatalf("Submit calls = %d, want 1", len(calls))
			}
			if calls[0].frq.RequestStatus.RequestStatus != tc.status {
				t.Errorf("Submit frq.RequestStatus.requestStatus = %d, want %d", calls[0].frq.RequestStatus.RequestStatus, tc.status)
			}
		})
	}
}
