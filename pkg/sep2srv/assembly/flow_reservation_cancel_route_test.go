package assembly_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Client cancel of a flow reservation request (#667), through the assembled
// router.

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := xml.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// postWindowRequest posts a request with every client-owned field set and
// returns its href.
func postWindowRequest(t *testing.T, srv *httptest.Server, edevID string) string {
	t.Helper()
	duration := uint16(900)
	body := mustMarshal(t, &sep2.FlowReservationRequest{
		MRID:              "CAFE0000000000000000000000000001",
		Description:       "evening charge",
		DurationRequested: &duration,
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 5000},
		PowerRequested:    &sep2.ActivePower{Value: 3000},
		IntervalRequested: &sep2.DateTimeInterval{Start: time.Now().Add(time.Hour).Unix(), Duration: 900},
	})
	resp, err := http.Post(srv.URL+"/edev/"+edevID+"/frq", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST frq: %v", err)
	}
	_ = resp.Body.Close() // only the status and Location are read
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST frq status = %d, want 201", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func getRequest(t *testing.T, srv *httptest.Server, href string) sep2.FlowReservationRequest {
	t.Helper()
	resp, err := http.Get(srv.URL + href)
	if err != nil {
		t.Fatalf("GET %s: %v", href, err)
	}
	var frq sep2.FlowReservationRequest
	decodeXML(t, resp, &frq)
	return frq
}

func putRequest(t *testing.T, srv *httptest.Server, href string, frq sep2.FlowReservationRequest) int {
	t.Helper()
	return putRaw(t, srv, href, mustMarshal(t, &frq))
}

func putRaw(t *testing.T, srv *httptest.Server, href, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv.URL+href, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", href, err)
	}
	_ = resp.Body.Close() // only the status is read
	return resp.StatusCode
}

func cancelled(frq sep2.FlowReservationRequest, at int64) sep2.FlowReservationRequest {
	frq.RequestStatus = sep2.RequestStatus{DateTime: at, RequestStatus: sep2.RequestStatusCancelled}
	return frq
}

func listResponses(t *testing.T, srv *httptest.Server, edevID string) sep2.FlowReservationResponseList {
	t.Helper()
	resp, err := http.Get(srv.URL + "/edev/" + edevID + "/frp")
	if err != nil {
		t.Fatalf("GET frp: %v", err)
	}
	var list sep2.FlowReservationResponseList
	decodeXML(t, resp, &list)
	return list
}

func TestFlowReservationCancel_PendingRequestIsDeniedOnceAndMarkedCancelled(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	href := postWindowRequest(t, srv, "e1")
	posted := getRequest(t, srv, href)

	at := time.Now().Unix()
	if got := putRequest(t, srv, href, cancelled(posted, at)); got != http.StatusNoContent {
		t.Fatalf("owner PUT cancel status = %d, want 204", got)
	}

	after := getRequest(t, srv, href)
	if after.RequestStatus != (sep2.RequestStatus{DateTime: at, RequestStatus: sep2.RequestStatusCancelled}) {
		t.Errorf("RequestStatus = %+v, want cancelled at %d", after.RequestStatus, at)
	}
	want := posted
	want.RequestStatus = after.RequestStatus
	if after.MRID != want.MRID || after.Description != want.Description ||
		*after.EnergyRequested != *want.EnergyRequested || *after.PowerRequested != *want.PowerRequested ||
		*after.IntervalRequested != *want.IntervalRequested || *after.DurationRequested != *want.DurationRequested {
		t.Errorf("a cancel changed a client-owned field: before %+v, after %+v", posted, after)
	}

	list := listResponses(t, srv, "e1")
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("responses = %d, want exactly 1", len(list.FlowReservationResponse))
	}
	frp := list.FlowReservationResponse[0]
	if frp.Interval == nil || frp.Interval.Duration != 0 || frp.Interval.Start != posted.IntervalRequested.Start {
		t.Errorf("response interval = %+v, want duration 0 at the requested start %d", frp.Interval, posted.IntervalRequested.Start)
	}
	if frp.Subject != posted.MRID {
		t.Errorf("response subject = %q, want %q", frp.Subject, posted.MRID)
	}
}

func TestFlowReservationCancel_AnsweredGrantReadsCancelled(t *testing.T) {
	t.Parallel()
	srv, _ := frqServer(t)
	href := postWindowRequest(t, srv, "e1")
	granted := waitForFRPList(t, srv, "/edev/e1/frp", 1).FlowReservationResponse[0]
	if granted.Interval == nil || granted.Interval.Duration != 900 {
		t.Fatalf("setup: the deadline fallback answered %+v, want a grant of 900 s", granted.Interval)
	}

	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), time.Now().Unix())); got != http.StatusNoContent {
		t.Fatalf("PUT cancel status = %d, want 204", got)
	}

	list := listResponses(t, srv, "e1")
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("responses = %d, want the one grant kept", len(list.FlowReservationResponse))
	}
	got := list.FlowReservationResponse[0]
	if got.MRID != granted.MRID || got.EventStatus == nil || got.EventStatus.CurrentStatus != 2 {
		t.Errorf("served grant %s status %+v, want mRID %s and EventStatus Cancelled (2)", got.MRID, got.EventStatus, granted.MRID)
	}
}

// TestFlowReservationCancel_ChangingAnyOtherFieldIs400 PUTs the posted
// request back with one field changed and the status Cancelled. Each is
// refused, nothing is stored, and no response is created.
func TestFlowReservationCancel_ChangingAnyOtherFieldIs400(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *sep2.FlowReservationRequest){
		"energy":         func(f *sep2.FlowReservationRequest) { f.EnergyRequested = &sep2.SignedRealEnergy{Value: 9999} },
		"power":          func(f *sep2.FlowReservationRequest) { f.PowerRequested = &sep2.ActivePower{Value: 1} },
		"interval":       func(f *sep2.FlowReservationRequest) { f.IntervalRequested.Duration++ },
		"duration":       func(f *sep2.FlowReservationRequest) { d := uint16(901); f.DurationRequested = &d },
		"mRID":           func(f *sep2.FlowReservationRequest) { f.MRID = "CAFE0000000000000000000000000002" },
		"description":    func(f *sep2.FlowReservationRequest) { f.Description = "morning charge" },
		"version":        func(f *sep2.FlowReservationRequest) { v := uint16(3); f.Version = &v },
		"omitted energy": func(f *sep2.FlowReservationRequest) { f.EnergyRequested = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
			href := postWindowRequest(t, srv, "e1")
			posted := getRequest(t, srv, href)

			body := cancelled(posted, time.Now().Unix())
			iv := *body.IntervalRequested
			body.IntervalRequested = &iv
			change(&body)
			if got := putRequest(t, srv, href, body); got != http.StatusBadRequest {
				t.Fatalf("PUT changing %s status = %d, want 400", name, got)
			}

			after := getRequest(t, srv, href)
			if after.RequestStatus != posted.RequestStatus {
				t.Errorf("RequestStatus = %+v after a refused PUT, want the original %+v", after.RequestStatus, posted.RequestStatus)
			}
			if after.MRID != posted.MRID || after.Description != posted.Description || after.Version != nil ||
				*after.EnergyRequested != *posted.EnergyRequested ||
				*after.PowerRequested != *posted.PowerRequested || *after.IntervalRequested != *posted.IntervalRequested ||
				*after.DurationRequested != *posted.DurationRequested {
				t.Errorf("a refused PUT changed the stored request: before %+v, after %+v", posted, after)
			}
			if n := len(listResponses(t, srv, "e1").FlowReservationResponse); n != 0 {
				t.Errorf("a refused PUT created %d response(s), want 0", n)
			}
		})
	}
}

func TestFlowReservationCancel_ACancelledRequestCannotBeRequestedAgain(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	href := postWindowRequest(t, srv, "e1")
	at := time.Now().Unix()
	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), at)); got != http.StatusNoContent {
		t.Fatalf("PUT cancel status = %d, want 204", got)
	}

	revive := getRequest(t, srv, href)
	revive.RequestStatus = sep2.RequestStatus{DateTime: at, RequestStatus: sep2.RequestStatusRequested}
	if got := putRequest(t, srv, href, revive); got != http.StatusBadRequest {
		t.Errorf("PUT Requested over a cancelled request status = %d, want 400", got)
	}
	if after := getRequest(t, srv, href); after.RequestStatus.RequestStatus != sep2.RequestStatusCancelled {
		t.Errorf("RequestStatus = %d after the refused revive, want Cancelled", after.RequestStatus.RequestStatus)
	}
}

// TestFlowReservationCancel_OnlyTheOwnerMayPut: the owner's PUT is accepted
// and a manager's is refused (403), leaving the request as it was.
func TestFlowReservationCancel_OnlyTheOwnerMayPut(t *testing.T) {
	t.Parallel()
	path := "/edev/" + victimID + "/frq/" + victimID
	body := mustMarshal(t, &sep2.FlowReservationRequest{
		Resource:      sep2.Resource{Href: path},
		RequestStatus: sep2.RequestStatus{DateTime: time.Now().Unix(), RequestStatus: sep2.RequestStatusCancelled},
	})

	t.Run("owner accepted", func(t *testing.T) {
		t.Parallel()
		fleet := newManagementFleet(t)
		srv := gateServer(t, fleet.stores, gateTestPolicy())
		if status, raw := gateRequest(t, srv, http.MethodPut, path, victimLFDI, body); status != http.StatusNoContent {
			t.Fatalf("owner PUT status = %d, want 204; body=%s", status, raw)
		}
		stored, err := fleet.stores.FlowReservationRequests.Get(t.Context(), victimID, victimID)
		if err != nil {
			t.Fatalf("read stored request: %v", err)
		}
		if stored.RequestStatus.RequestStatus != sep2.RequestStatusCancelled {
			t.Errorf("stored RequestStatus = %d after the owner's PUT, want Cancelled", stored.RequestStatus.RequestStatus)
		}
	})

	t.Run("manager refused", func(t *testing.T) {
		t.Parallel()
		fleet := newManagementFleet(t)
		srv := gateServer(t, fleet.stores, gateTestPolicy())
		status, raw := gateRequest(t, srv, http.MethodPut, path, managerLFDI, body)
		if status != http.StatusForbidden {
			t.Fatalf("manager PUT status = %d, want 403; body=%s", status, raw)
		}
		assertNoFleetData(t, "manager PUT frq", raw)
		stored, err := fleet.stores.FlowReservationRequests.Get(t.Context(), victimID, victimID)
		if err != nil {
			t.Fatalf("read stored request: %v", err)
		}
		if stored.RequestStatus.RequestStatus != sep2.RequestStatusRequested {
			t.Errorf("stored RequestStatus = %d after the manager's refused PUT, want Requested", stored.RequestStatus.RequestStatus)
		}
	})
}

// TestFlowReservationCancel_PutLeavingRequestedChangesNothing: an echoed
// request whose status is still Requested is accepted and is not a cancel.
func TestFlowReservationCancel_PutLeavingRequestedChangesNothing(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	href := postWindowRequest(t, srv, "e1")
	posted := getRequest(t, srv, href)

	if got := putRequest(t, srv, href, posted); got != http.StatusNoContent {
		t.Fatalf("PUT of the unchanged request status = %d, want 204", got)
	}
	after := getRequest(t, srv, href)
	if after.RequestStatus != posted.RequestStatus || after.RequestStatus.RequestStatus != sep2.RequestStatusRequested {
		t.Errorf("RequestStatus = %+v, want the original Requested %+v", after.RequestStatus, posted.RequestStatus)
	}
	if n := len(listResponses(t, srv, "e1").FlowReservationResponse); n != 0 {
		t.Errorf("a no-op PUT created %d response(s), want 0: it must not be read as a cancel", n)
	}
}

func TestFlowReservationCancel_InvalidRequestStatusIs400(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	href := postWindowRequest(t, srv, "e1")
	posted := getRequest(t, srv, href)

	reserved := posted
	reserved.RequestStatus = sep2.RequestStatus{DateTime: time.Now().Unix(), RequestStatus: 7}
	if got := putRequest(t, srv, href, reserved); got != http.StatusBadRequest {
		t.Errorf("PUT requestStatus 7 status = %d, want 400", got)
	}
	noStatus := `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>` + posted.MRID + `</mRID></FlowReservationRequest>`
	if got := putRaw(t, srv, href, noStatus); got != http.StatusBadRequest {
		t.Errorf("PUT with no RequestStatus element status = %d, want 400", got)
	}
	if after := getRequest(t, srv, href); after.RequestStatus != posted.RequestStatus {
		t.Errorf("RequestStatus = %+v after refused PUTs, want %+v", after.RequestStatus, posted.RequestStatus)
	}
	if n := len(listResponses(t, srv, "e1").FlowReservationResponse); n != 0 {
		t.Errorf("refused PUTs created %d response(s), want 0", n)
	}
}

func TestFlowReservationCancel_MissingRequestIs404(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	body := cancelled(sep2.FlowReservationRequest{}, time.Now().Unix())
	if got := putRequest(t, srv, "/edev/e1/frq/frq-absent", body); got != http.StatusNotFound {
		t.Errorf("PUT on a missing request status = %d, want 404", got)
	}
	if n := len(listResponses(t, srv, "e1").FlowReservationResponse); n != 0 {
		t.Errorf("a PUT on a missing request created %d response(s), want 0", n)
	}
}
