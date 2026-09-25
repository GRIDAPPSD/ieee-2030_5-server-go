package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestEndDeviceLinks_FlowReservationGateWinsOverAPreWrappedStore asserts the
// current mount gate decides what a served EndDevice advertises even when
// Stores.EndDevices arrives already wrapped in the other arm, the way an
// embedder that pre-wraps the store for a different Stores would hand it to
// this one. Registrations and LogEvents are left absent so the pre-wrapped
// value reaches the flow reservation decorator unwrapped by anything else.
func TestEndDeviceLinks_FlowReservationGateWinsOverAPreWrappedStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	stores := testStores()
	stores.Registrations = nil
	stores.LogEvents = nil
	// Pre-wrapped with the UNSERVED arm, as if built for a Stores where
	// FlowReservationRequests was absent. This Stores' own
	// FlowReservationRequests is present, so the served links must win.
	stores.EndDevices = memory.NewFlowReservationUnservedEndDeviceStore(memory.NewEndDeviceStore())
	if err := stores.EndDevices.Create(ctx, "1", sep2.EndDevice{}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	readers := assembly.NewReaderStores(stores)
	dev, err := readers.EndDevices.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dev.FlowReservationRequestListLink == nil || dev.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Errorf("FlowReservationRequestListLink = %v, want href %q: this Stores' own gate must win over the arm "+
			"a pre-wrapped store was built with", dev.FlowReservationRequestListLink, "/edev/1/frq")
	}
	if dev.FlowReservationResponseListLink == nil || dev.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Errorf("FlowReservationResponseListLink = %v, want href %q", dev.FlowReservationResponseListLink, "/edev/1/frp")
	}
}

// TestEndDeviceLinks_FlowReservationHrefsResolve proves discovery the way a
// conforming client does it: register a device, read the hrefs the server
// itself advertised on the served EndDevice, and follow them. A test that
// built "/edev/{id}/frq" from the id it already knew would prove nothing
// about whether the server actually mounted that route (#693).
func TestEndDeviceLinks_FlowReservationHrefsResolve(t *testing.T) {
	t.Parallel()

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), testAuthPolicy(), testSFDI, testLFDI, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL+"/edev", "application/sep+xml", nil)
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201", resp.StatusCode)
	}
	var dev sep2.EndDevice
	decodeXML(t, resp, &dev)

	// Nothing else on the served EndDevice changed.
	if dev.SFDI != testSFDI || dev.LFDI != testLFDI {
		t.Errorf("identity = SFDI %q LFDI %q, want %q %q", dev.SFDI, dev.LFDI, testSFDI, testLFDI)
	}
	if dev.Enabled == nil || !*dev.Enabled {
		t.Errorf("Enabled = %v, want true", dev.Enabled)
	}
	if dev.FunctionSetAssignmentsListLink == nil || dev.FunctionSetAssignmentsListLink.Href != dev.Href+"/fsa" {
		t.Errorf("FunctionSetAssignmentsListLink = %+v, want href %s/fsa", dev.FunctionSetAssignmentsListLink, dev.Href)
	}

	for _, tc := range []struct {
		name string
		link *sep2.ListLink
		want string
	}{
		{"FlowReservationRequestListLink", dev.FlowReservationRequestListLink, dev.Href + "/frq"},
		{"FlowReservationResponseListLink", dev.FlowReservationResponseListLink, dev.Href + "/frp"},
	} {
		if tc.link == nil {
			t.Fatalf("%s is absent on the served EndDevice", tc.name)
		}
		if tc.link.Href != tc.want {
			t.Errorf("%s.Href = %q, want %q", tc.name, tc.link.Href, tc.want)
		}
	}

	// Follow the request-list href from the document, and assert the list's
	// own fields rather than only its status (data-invariants rule 1).
	frqResp, err := srv.Client().Get(srv.URL + dev.FlowReservationRequestListLink.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", dev.FlowReservationRequestListLink.Href, err)
	}
	if frqResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: a link the server advertises must not 404, the way RegistrationLink once did (#693)", dev.FlowReservationRequestListLink.Href, frqResp.StatusCode)
	}
	var frqList sep2.FlowReservationRequestList
	decodeXML(t, frqResp, &frqList)
	if frqList.Href != dev.FlowReservationRequestListLink.Href {
		t.Errorf("FlowReservationRequestList.Href = %q, want %q", frqList.Href, dev.FlowReservationRequestListLink.Href)
	}
	if frqList.All != 0 {
		t.Errorf("FlowReservationRequestList.All = %d, want 0 (no reservation was made)", frqList.All)
	}

	frpResp, err := srv.Client().Get(srv.URL + dev.FlowReservationResponseListLink.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", dev.FlowReservationResponseListLink.Href, err)
	}
	if frpResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: a link the server advertises must not 404, the way RegistrationLink once did (#693)", dev.FlowReservationResponseListLink.Href, frpResp.StatusCode)
	}
	var frpList sep2.FlowReservationResponseList
	decodeXML(t, frpResp, &frpList)
	if frpList.Href != dev.FlowReservationResponseListLink.Href {
		t.Errorf("FlowReservationResponseList.Href = %q, want %q", frpList.Href, dev.FlowReservationResponseListLink.Href)
	}
	if frpList.All != 0 {
		t.Errorf("FlowReservationResponseList.All = %d, want 0 (no reservation was made)", frpList.All)
	}
}

// TestEndDeviceLinks_ManagedDeviceSeenByAggregatorMatchesOwner asserts that
// an aggregator managing a device reads the same EndDevice record the device
// itself does, so it must see both new links too. What it may POST to those
// links is a separately pinned decision (ownership_internal_test.go) that
// this change must not touch, so the refused write is reasserted here too.
func TestEndDeviceLinks_ManagedDeviceSeenByAggregatorMatchesOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	stores := testStores()
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(ctx, managerLFDI, victimLFDI); err != nil {
		t.Fatalf("assign %s -> %s: %v", managerLFDI, victimLFDI, err)
	}
	stores.EndDeviceManagers = managers

	srv := gateServer(t, stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", victimLFDI, "")
	if status != http.StatusCreated {
		t.Fatalf("register the managed device: status %d, want 201; body=%q", status, raw)
	}
	var registered sep2.EndDevice
	if err := xml.Unmarshal(raw, &registered); err != nil {
		t.Fatalf("unmarshal registration body: %v", err)
	}
	href := registered.Href

	ownerStatus, ownerRaw := gateRequest(t, srv, http.MethodGet, href, victimLFDI, "")
	if ownerStatus != http.StatusOK {
		t.Fatalf("owner GET %s: status %d, want 200; body=%q", href, ownerStatus, ownerRaw)
	}
	var owner sep2.EndDevice
	if err := xml.Unmarshal(ownerRaw, &owner); err != nil {
		t.Fatalf("unmarshal owner body: %v", err)
	}

	managerStatus, managerRaw := gateRequest(t, srv, http.MethodGet, href, managerLFDI, "")
	if managerStatus != http.StatusOK {
		t.Fatalf("manager GET %s: status %d, want 200; body=%q", href, managerStatus, managerRaw)
	}
	var managed sep2.EndDevice
	if err := xml.Unmarshal(managerRaw, &managed); err != nil {
		t.Fatalf("unmarshal manager body: %v", err)
	}

	if owner.FlowReservationRequestListLink == nil || managed.FlowReservationRequestListLink == nil ||
		owner.FlowReservationRequestListLink.Href != managed.FlowReservationRequestListLink.Href {
		t.Errorf("FlowReservationRequestListLink: owner %+v, manager %+v, want equal and present",
			owner.FlowReservationRequestListLink, managed.FlowReservationRequestListLink)
	}
	if owner.FlowReservationResponseListLink == nil || managed.FlowReservationResponseListLink == nil ||
		owner.FlowReservationResponseListLink.Href != managed.FlowReservationResponseListLink.Href {
		t.Errorf("FlowReservationResponseListLink: owner %+v, manager %+v, want equal and present",
			owner.FlowReservationResponseListLink, managed.FlowReservationResponseListLink)
	}

	// The write surface is unchanged: POSTing to the request list on behalf
	// of a managed device is still refused, per the allow-list this change
	// must not move (ownership_internal_test.go).
	writeStatus, _ := gateRequest(t, srv, http.MethodPost, href+"/frq", managerLFDI, "")
	if writeStatus != http.StatusForbidden {
		t.Errorf("manager POST %s/frq: status %d, want 403 (unchanged by this card)", href, writeStatus)
	}
}
