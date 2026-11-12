package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// seedReservation stores a request and, when frpWindow is non-nil, its
// response and a lifecycle record, directly in the stores so no queue is
// involved.
func seedReservation(t *testing.T, stores *assembly.Stores, edevID, id string, frpWindow *sep2.DateTimeInterval) {
	t.Helper()
	ctx := context.Background()
	frq := sep2.FlowReservationRequest{}
	frq.Href = "/edev/" + edevID + "/frq/" + id
	frq.MRID = "0102030405060708090A0B0C0D0E0F10"
	if err := stores.FlowReservationRequests.Create(ctx, edevID, id, frq); err != nil {
		t.Fatalf("seed request %s/%s: %v", edevID, id, err)
	}
	if frpWindow == nil {
		return
	}
	frp := sep2.FlowReservationResponse{}
	frp.Interval = frpWindow
	frp.CreationTime = frpWindow.Start
	frp.Href = "/edev/" + edevID + "/frp/" + id
	frp.MRID = "1102030405060708090A0B0C0D0E0F10"
	if err := stores.FlowReservationResponses.Create(ctx, edevID, id, frp); err != nil {
		t.Fatalf("seed response %s/%s: %v", edevID, id, err)
	}
	if err := stores.FlowReservationResponseLifecycles.Create(ctx, edevID, id, dercontrol.LifecycleRecord{}); err != nil {
		t.Fatalf("seed lifecycle %s/%s: %v", edevID, id, err)
	}
}

func requestPresent(t *testing.T, stores *assembly.Stores, edevID, id string) bool {
	t.Helper()
	_, err := stores.FlowReservationRequests.Get(context.Background(), edevID, id)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		return false
	}
	t.Fatalf("get request %s/%s: %v", edevID, id, err)
	return false
}

func responsePresent(t *testing.T, stores *assembly.Stores, edevID, id string) bool {
	t.Helper()
	_, err := stores.FlowReservationResponses.Get(context.Background(), edevID, id)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		return false
	}
	t.Fatalf("get response %s/%s: %v", edevID, id, err)
	return false
}

// The owner's DELETE removes an ended request with its response and
// lifecycle record, leaves the device's other requests alone, and every count
// the lists report follows; a second DELETE is 404.
func TestFlowReservationRequest_DeleteRemovesAnEndedRequestAndItsChain(t *testing.T) {
	t.Parallel()

	srv, stores := frqServer(t)
	ended := &sep2.DateTimeInterval{Start: 1600000000, Duration: 3600}
	seedReservation(t, stores, "e1", "gone", ended)
	seedReservation(t, stores, "e1", "kept", ended)

	if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/e1/frq/gone"); code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", code)
	}

	if requestPresent(t, stores, "e1", "gone") || responsePresent(t, stores, "e1", "gone") {
		t.Error("request or response still stored after DELETE")
	}
	if _, err := stores.FlowReservationResponseLifecycles.Get(context.Background(), "e1", "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("lifecycle record after DELETE: err = %v, want ErrNotFound", err)
	}
	if !requestPresent(t, stores, "e1", "kept") || !responsePresent(t, stores, "e1", "kept") {
		t.Error("the device's other request or response was removed")
	}

	status, body := getBytes(t, srv, "/edev/e1/frq")
	if status != http.StatusOK {
		t.Fatalf("GET /edev/e1/frq status = %d", status)
	}
	var frqs sep2.FlowReservationRequestList
	if err := xml.Unmarshal(body, &frqs); err != nil {
		t.Fatalf("unmarshal request list: %v", err)
	}
	if frqs.All != 1 || frqs.Results != 1 || len(frqs.FlowReservationRequest) != 1 ||
		frqs.FlowReservationRequest[0].Href != "/edev/e1/frq/kept" {
		t.Errorf("request list after DELETE: all=%d results=%d items=%d, want 1 1 [kept]",
			frqs.All, frqs.Results, len(frqs.FlowReservationRequest))
	}
	status, body = getBytes(t, srv, "/edev/e1/frp")
	if status != http.StatusOK {
		t.Fatalf("GET /edev/e1/frp status = %d", status)
	}
	var frps sep2.FlowReservationResponseList
	if err := xml.Unmarshal(body, &frps); err != nil {
		t.Fatalf("unmarshal response list: %v", err)
	}
	if frps.All != 1 || frps.Results != 1 || len(frps.FlowReservationResponse) != 1 ||
		frps.FlowReservationResponse[0].Href != "/edev/e1/frp/kept" {
		t.Errorf("response list after DELETE: all=%d results=%d items=%d, want 1 1 [kept]",
			frps.All, frps.Results, len(frps.FlowReservationResponse))
	}
	for _, p := range []string{"/edev/e1/frq/gone", "/edev/e1/frp/gone"} {
		if code, _ := getBytes(t, srv, p); code != http.StatusNotFound {
			t.Errorf("GET %s after DELETE = %d, want 404", p, code)
		}
	}
	if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/e1/frq/gone"); code != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", code)
	}
}

// A request the standard requires the server to keep (a live response, or no
// response yet) is 409 and untouched.
func TestFlowReservationRequest_DeleteKeepsAnActiveOrUnansweredRequest(t *testing.T) {
	t.Parallel()

	srv, stores := frqServer(t)
	live := &sep2.DateTimeInterval{Start: time.Now().Add(-time.Hour).Unix(), Duration: 7200}
	seedReservation(t, stores, "e1", "live", live)
	seedReservation(t, stores, "e1", "pending", nil)

	for _, id := range []string{"live", "pending"} {
		if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/e1/frq/"+id); code != http.StatusConflict {
			t.Errorf("DELETE %s status = %d, want 409", id, code)
		}
		if !requestPresent(t, stores, "e1", id) {
			t.Errorf("request %s removed by a refused DELETE", id)
		}
	}
	if !responsePresent(t, stores, "e1", "live") {
		t.Error("live response removed by a refused DELETE")
	}
	if _, err := stores.FlowReservationResponseLifecycles.Get(context.Background(), "e1", "live"); err != nil {
		t.Errorf("live lifecycle record after refused DELETE: %v", err)
	}
}

// A device that does not own {id} is refused 403 by the ownership gate and
// its records are intact; the owner cannot reach another device's request
// through its own path.
func TestFlowReservationRequest_DeleteIsGatedAndScoped(t *testing.T) {
	t.Parallel()

	srv, stores := frqServer(t)
	seedDevice(t, stores.EndDevices, "d9", "someone-elses-lfdi", "")
	ended := &sep2.DateTimeInterval{Start: 1600000000, Duration: 3600}
	seedReservation(t, stores, "d9", "theirs", ended)

	if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/d9/frq/theirs"); code != http.StatusForbidden {
		t.Errorf("DELETE on a foreign device status = %d, want 403", code)
	}
	if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/e1/frq/theirs"); code != http.StatusNotFound {
		t.Errorf("DELETE of a foreign id under the owner's path status = %d, want 404", code)
	}
	if !requestPresent(t, stores, "d9", "theirs") || !responsePresent(t, stores, "d9", "theirs") {
		t.Error("foreign request or response removed by a refused DELETE")
	}
}

// DELETE on the response is Deprecated (mode D) and stays refused: removing a
// response alone would orphan its request.
func TestFlowReservationResponse_DeleteStaysRefused(t *testing.T) {
	t.Parallel()

	srv, stores := frqServer(t)
	seedReservation(t, stores, "e1", "r1", &sep2.DateTimeInterval{Start: 1600000000, Duration: 3600})
	if code := statusOf(t, http.MethodDelete, srv.URL+"/edev/e1/frp/r1"); code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE response status = %d, want 405", code)
	}
	if !responsePresent(t, stores, "e1", "r1") {
		t.Error("response removed by a refused DELETE")
	}
}
