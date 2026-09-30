package flow_reservation_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	corelisthandler "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/listhandler"
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
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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

// TestHandlePostFlowReservationRequest_ResponseMRIDViaListRoute is #665's
// first and second criteria: the auto-created response carries a 128-bit
// mRID as 32 uppercase hex digits, read back the way a client actually reads
// it (GET the list, not the store directly), and two responses created in
// the same run get distinct mRIDs.
//
// The list route is wired here the same way pkg/sep2srv/assembly mounts
// GET /edev/{id}/frp: a per-parent store.Under view fed to the generic list
// handler. Reproduced from exported symbols rather than importing assembly,
// to keep this a handler-package test.
func TestHandlePostFlowReservationRequest_ResponseMRIDViaListRoute(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))
	mux.HandleFunc("GET /edev/{id}/frp", func(w http.ResponseWriter, r *http.Request) {
		scoped := store.Under[sep2.FlowReservationResponse](frpStore, r.PathValue("id"))
		corelisthandler.ListHandler[sep2.FlowReservationResponse, sep2.FlowReservationResponseList](
			scoped, flow_reservation.BuildFlowReservationResponseList, 900,
		).ServeHTTP(w, r)
	})

	post := func(mrid string) {
		energy := sep2.SignedRealEnergy{Value: 10000}
		frq := sep2.FlowReservationRequest{MRID: mrid, EnergyRequested: &energy}
		body, err := xml.Marshal(&frq)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST status = %d, want 201, body: %s", w.Code, w.Body.String())
		}
	}
	post("FRQ001")
	post("FRQ002")

	req := httptest.NewRequest(http.MethodGet, "/edev/dev1/frp", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var list sep2.FlowReservationResponseList
	if err := xml.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v, body: %s", err, w.Body.String())
	}
	if len(list.FlowReservationResponse) != 2 {
		t.Fatalf("responses in list = %d, want 2", len(list.FlowReservationResponse))
	}

	const hexDigits = "0123456789ABCDEF"
	for i, frp := range list.FlowReservationResponse {
		if len(frp.MRID) != 32 {
			t.Errorf("response %d MRID = %q, want 32 hex digits", i, frp.MRID)
			continue
		}
		if _, err := hex.DecodeString(frp.MRID); err != nil {
			t.Errorf("response %d MRID = %q, not hex: %v", i, frp.MRID, err)
		}
		for _, c := range frp.MRID {
			if !strings.ContainsRune(hexDigits, c) {
				t.Errorf("response %d MRID = %q contains %q, want only uppercase hex digits", i, frp.MRID, c)
				break
			}
		}
	}
	if list.FlowReservationResponse[0].MRID == list.FlowReservationResponse[1].MRID {
		t.Errorf("both responses carry MRID %q; two responses created in the same run must differ", list.FlowReservationResponse[0].MRID)
	}
}

// TestHandlePostFlowReservationRequest_ResponseSubjectEqualsRequestMRID pins
// #665's third criterion: subject must equal the originating request's mRID.
// The handler already set this field; nothing asserted it.
func TestHandlePostFlowReservationRequest_ResponseSubjectEqualsRequestMRID(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

	energy := sep2.SignedRealEnergy{Value: 5000}
	frq := sep2.FlowReservationRequest{MRID: "REQMRID123", EnergyRequested: &energy}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(responses.Items) != 1 {
		t.Fatalf("stored response count = %d, want 1", len(responses.Items))
	}
	if got := responses.Items[0].Subject; got != "REQMRID123" {
		t.Errorf("stored FlowReservationResponse Subject = %q, want the request mRID %q", got, "REQMRID123")
	}
}

// TestHandlePostFlowReservationRequest_MissingRequestMRIDRefused pins #665's
// fourth criterion: a request with no mRID is refused with 400 rather than
// producing a response whose subject nobody can match to a bid.
func TestHandlePostFlowReservationRequest_MissingRequestMRIDRefused(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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
	responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(responses.Items) != 0 {
		t.Errorf("stored response count = %d, want 0; no response should be auto-created for a refused request", len(responses.Items))
	}
}

// TestHandlePostFlowReservationRequest_WhitespaceOnlyMRIDRefused extends the
// missing-mRID criterion: encoding/xml does not trim element content, so
// "<mRID>   </mRID>" unmarshals to a non-empty string that the bare "== """
// check let through, storing a response whose subject is meaningless
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
			frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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
			responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list stored responses: %v", err)
			}
			if len(responses.Items) != 0 {
				t.Errorf("stored response count = %d, want 0", len(responses.Items))
			}
		})
	}
}

// TestHandlePostFlowReservationRequest_WithPEN_LowBitsArePEN is fix round
// 2's PEN criterion, exercised through the handler HandlePostFlowReservationRequest
// is actually called with, not just the internal mint function: a
// configured PEN reaches the stored response's mRID with its value in the
// low 32 bits, the same place internal/dercontrol embeds one for a
// DERControl mRID.
func TestHandlePostFlowReservationRequest_WithPEN_LowBitsArePEN(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	pen := uint32(0x40732001)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, &pen))

	energy := sep2.SignedRealEnergy{Value: 10000}
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &energy}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(responses.Items) != 1 {
		t.Fatalf("stored response count = %d, want 1", len(responses.Items))
	}
	got := responses.Items[0].MRID
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatalf("MRID %q is not hex: %v", got, err)
	}
	if gotPEN := binary.BigEndian.Uint32(raw[12:]); gotPEN != pen {
		t.Fatalf("low 32 bits of MRID %q = %#x, want configured PEN %#x", got, gotPEN, pen)
	}
}

// TestHandlePostFlowReservationRequest_RequestStatusRefused is #692's three
// acceptance criteria: a body with no RequestStatus, an out-of-range
// requestStatus, or a negative dateTime is refused, storing neither the
// request nor an auto-approved response for it.
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
			frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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
			responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list stored responses: %v", err)
			}
			if len(responses.Items) != 0 {
				t.Errorf("stored response count = %d, want 0; no response should be auto-created for a refused request", len(responses.Items))
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
			frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /edev/{id}/frq", flow_reservation.HandlePostFlowReservationRequest(frqStore, frpStore, nil))

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
		})
	}
}
