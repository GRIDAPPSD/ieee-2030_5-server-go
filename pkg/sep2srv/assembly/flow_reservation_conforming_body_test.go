package assembly_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// conformingBody reads a request body from testdata/schema-conforming.
//
// The body is read rather than marshalled on purpose. A body this suite
// marshalled from a sep2 struct cannot fail when the struct is the wrong
// shape, because the same wrong shape encodes it and decodes it; that is what
// let a 400 on every conforming FlowReservationRequest sit behind a green
// build. See testdata/schema-conforming/README.md.
func conformingBody(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", "schema-conforming", name))
	if err != nil {
		t.Fatalf("read conforming fixture %s: %v", name, err)
	}
	return body
}

// TestFlowReservationRequest_ConformingBodyIsAccepted posts the mandatory
// element set the schema defines and asserts the server takes it and keeps it.
//
// It asserts the values of energyRequested, intervalRequested, powerRequested
// and RequestStatus, not just the 201. Those four are exactly the elements no
// other test in this package sends, so a server that accepted the document and
// dropped them would pass a status-only check while granting a reservation for
// no energy over no interval. The FlowReservationResponse assertions below are
// the same property read from the resource a client actually acts on.
func TestFlowReservationRequest_ConformingBodyIsAccepted(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	body := conformingBody(t, "flow_reservation_request.xml")

	resp, err := http.Post(srv.URL+"/edev/e1/frq", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /edev/e1/frq: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/e1/frq status = %d, want 201: the body is the element set the schema mandates, so a refusal here is ours, not the client's", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST returned no Location; there is no href to follow")
	}

	frqResp, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	defer func() { _ = frqResp.Body.Close() }()
	if frqResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, frqResp.StatusCode)
	}

	var frq sep2.FlowReservationRequest
	decodeXML(t, frqResp, &frq)

	if frq.MRID != "0102030405060708090A0B0C0D0E0F10" {
		t.Errorf("mRID = %q, want the posted value", frq.MRID)
	}
	// creationTime is the server's to stamp: the handler overwrites whatever
	// the client sent, so the posted 1727136000 is not the value to expect.
	if frq.CreationTime == 0 {
		t.Error("creationTime = 0 on the served request")
	}
	if frq.EnergyRequested == nil {
		t.Error("energyRequested is absent: the server dropped a mandatory element it accepted")
	} else if frq.EnergyRequested.Multiplier != 3 || frq.EnergyRequested.Value != 15 {
		t.Errorf("energyRequested = {%d, %d}, want {3, 15}", frq.EnergyRequested.Multiplier, frq.EnergyRequested.Value)
	}
	if frq.IntervalRequested == nil {
		t.Error("intervalRequested is absent")
	} else if frq.IntervalRequested.Duration != 10800 || frq.IntervalRequested.Start != 1727136000 {
		t.Errorf("intervalRequested = {%d, %d}, want {10800, 1727136000}", frq.IntervalRequested.Duration, frq.IntervalRequested.Start)
	}
	if frq.PowerRequested == nil {
		t.Error("powerRequested is absent")
	} else if frq.PowerRequested.Multiplier != 3 || frq.PowerRequested.Value != 7 {
		t.Errorf("powerRequested = {%d, %d}, want {3, 7}", frq.PowerRequested.Multiplier, frq.PowerRequested.Value)
	}
	// The element the Go type carried as a bare integer until core v0.19.0.
	// Asserting both children is what distinguishes "decoded" from "flattened":
	// a scalar field would have taken the 400, and a complex field that dropped
	// dateTime would serve the epoch, which is issue #692's shape.
	if frq.RequestStatus.DateTime != 1727136000 {
		t.Errorf("RequestStatus.dateTime = %d, want 1727136000: the client's status instant must survive the round trip", frq.RequestStatus.DateTime)
	}
	if frq.RequestStatus.RequestStatus != sep2.RequestStatusRequested {
		t.Errorf("RequestStatus.requestStatus = %d, want %d", frq.RequestStatus.RequestStatus, sep2.RequestStatusRequested)
	}

	// The server derives its own grant from those elements, so a silent drop
	// upstream shows up here as a reservation granting nothing.
	listResp, err := http.Get(srv.URL + "/edev/e1/frp")
	if err != nil {
		t.Fatalf("GET /edev/e1/frp: %v", err)
	}
	var list sep2.FlowReservationResponseList
	decodeXML(t, listResp, &list)
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("FlowReservationResponseList has %d members, want 1", len(list.FlowReservationResponse))
	}

	frp := list.FlowReservationResponse[0]
	if frp.Subject != "0102030405060708090A0B0C0D0E0F10" {
		t.Errorf("subject = %q, want the posted mRID", frp.Subject)
	}
	if frp.EnergyAvailable == nil || frp.EnergyAvailable.Value != 15 {
		t.Errorf("energyAvailable = %v, want value 15: the grant must carry the energy the request asked for", frp.EnergyAvailable)
	}
	if frp.PowerAvailable == nil || frp.PowerAvailable.Value != 7 {
		t.Errorf("powerAvailable = %v, want value 7", frp.PowerAvailable)
	}
	if frp.Interval == nil || frp.Interval.Duration != 10800 {
		t.Errorf("interval = %v, want duration 10800: a grant over no interval reserves nothing", frp.Interval)
	}
}
