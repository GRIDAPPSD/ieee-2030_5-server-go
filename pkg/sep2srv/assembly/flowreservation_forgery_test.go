package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The three defects the four rounds on PR 696 rediscovered in turn (#693):
// a client-forged link surviving to the wire, a link derived from the wrong
// identity, and an unwired deployment advertising an href it 404s on. Each
// test here pins one of them at the router, the way the discovery test
// already pins the fourth (that the advertised hrefs actually resolve).

// forgedFlowReservationBody is an EndDevice carrying attacker-chosen values
// for the two flow reservation links, plus LogEventListLink as a control:
// the server already corrects that one, so seeing it corrected in the same
// response proves the forged frq/frp values are not an artifact of the probe
// not being read.
const forgedFlowReservationBody = `<EndDevice xmlns="urn:ieee:std:2030.5:ns">` +
	`<FlowReservationRequestListLink href="/evil/frq"/>` +
	`<FlowReservationResponseListLink href="/evil/frp"/>` +
	`<LogEventListLink href="/evil/lel"/>` +
	`</EndDevice>`

// TestEndDeviceLinks_FlowReservationForgedBodyIsIgnored is the RED case for
// round 4's HIGH: a POST body carrying a forged FlowReservationRequestListLink
// was persisted and served back on the 201, a later GET, and the /edev list.
func TestEndDeviceLinks_FlowReservationForgedBodyIsIgnored(t *testing.T) {
	t.Parallel()

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), testAuthPolicy(), testSFDI, testLFDI, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(forgedFlowReservationBody))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201", resp.StatusCode)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)
	assertDerivedNotForged(t, "201 body", created)
	if created.LogEventListLink == nil || created.LogEventListLink.Href != created.Href+"/lel" {
		t.Errorf("control: LogEventListLink = %+v, want href %s/lel (the server's own correction must fire "+
			"in the same response for this probe to mean anything)", created.LogEventListLink, created.Href)
	}

	getResp, err := srv.Client().Get(srv.URL + created.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", created.Href, err)
	}
	var reGet sep2.EndDevice
	decodeXML(t, getResp, &reGet)
	assertDerivedNotForged(t, "later GET", reGet)

	listResp, err := srv.Client().Get(srv.URL + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	var list sep2.EndDeviceList
	decodeXML(t, listResp, &list)
	if len(list.EndDevice) != 1 {
		t.Fatalf("/edev list has %d devices, want 1", len(list.EndDevice))
	}
	assertDerivedNotForged(t, "/edev list", list.EndDevice[0])

	// The forged href must not even resolve: this server never routed it.
	forgedResp, err := srv.Client().Get(srv.URL + "/evil/frq")
	if err != nil {
		t.Fatalf("GET /evil/frq: %v", err)
	}
	if forgedResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /evil/frq status = %d, want 404 (control: the forged href was never mounted)", forgedResp.StatusCode)
	}
}

// assertDerivedNotForged asserts dev's flow reservation links are derived
// from its own Href, never the attacker's /evil/... values.
func assertDerivedNotForged(t *testing.T, where string, dev sep2.EndDevice) {
	t.Helper()
	wantReq, wantResp := dev.Href+"/frq", dev.Href+"/frp"
	if dev.FlowReservationRequestListLink == nil || dev.FlowReservationRequestListLink.Href != wantReq {
		t.Errorf("%s: FlowReservationRequestListLink = %+v, want href %q", where, dev.FlowReservationRequestListLink, wantReq)
	}
	if dev.FlowReservationResponseListLink == nil || dev.FlowReservationResponseListLink.Href != wantResp {
		t.Errorf("%s: FlowReservationResponseListLink = %+v, want href %q", where, dev.FlowReservationResponseListLink, wantResp)
	}
}

// TestEndDeviceLinks_FlowReservationForgedPUTIsIgnored is E3: the write path
// a POST-only probe cannot reach. PUT /edev/{id} replaces the record, and
// under the discarded handler-side helper a forged PUT body was never
// corrected because nothing re-derived after it.
func TestEndDeviceLinks_FlowReservationForgedPUTIsIgnored(t *testing.T) {
	t.Parallel()

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), testAuthPolicy(), testSFDI, testLFDI, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL+"/edev", "application/sep+xml", nil)
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)

	req, err := http.NewRequest(http.MethodPut, srv.URL+created.Href, strings.NewReader(forgedFlowReservationBody))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	putResp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", created.Href, err)
	}
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s status = %d, want 204", created.Href, putResp.StatusCode)
	}

	getResp, err := srv.Client().Get(srv.URL + created.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", created.Href, err)
	}
	var afterPut sep2.EndDevice
	decodeXML(t, getResp, &afterPut)
	assertDerivedNotForged(t, "GET after forged PUT", afterPut)
}

// TestEndDeviceLinks_KeyVersusHrefDisagreement is P9 (E4): a record's flow
// reservation links must come from the identity the read path actually has,
// never lifted from a neighbouring device's Href or a malformed one.
//
// Get, Create and Update have the store key in hand and derive from it
// directly. List, GetBySFDI and GetByLFDI do not: they recover a key from
// the device's own Href, and a record seeded off that addressing invariant
// must have both links stripped, not guessed.
func TestEndDeviceLinks_KeyVersusHrefDisagreement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const skewedLFDI = "1111111111111111111111111111111111111111"
	const malformedLFDI = "2222222222222222222222222222222222222222"

	stores := testStores()
	// Seeded directly into the undecorated store, under a key that disagrees
	// with the device's own Href, the way a record migrated or hand-edited
	// off the addressing invariant would arrive.
	skewed := sep2.EndDevice{LFDI: skewedLFDI}
	skewed.Href = "/edev/9"
	if err := stores.EndDevices.Create(ctx, "3", skewed); err != nil {
		t.Fatalf("seed skewed device: %v", err)
	}
	malformed := sep2.EndDevice{LFDI: malformedLFDI}
	malformed.Href = "/edev/3/rg" // names no key: keyFromEndDeviceHref rejects an extra segment.
	if err := stores.EndDevices.Create(ctx, "malformed", malformed); err != nil {
		t.Fatalf("seed malformed device: %v", err)
	}

	// gateServer/gateRequest, not a fixed testAuthPolicy: the ownership gate
	// on GET /edev/{id} and the caller-scoped GET /edev both compare the
	// caller's own identity against the record's LFDI, so each seeded
	// device is read as the identity that owns it.
	srv := gateServer(t, stores, gateTestPolicy())

	// Get(id): derives from the id in the URL, "3", not from the stored Href.
	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/3", skewedLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev/3 as its own LFDI: status %d, want 200; body=%q", status, raw)
	}
	var byID sep2.EndDevice
	if err := xml.Unmarshal(raw, &byID); err != nil {
		t.Fatalf("unmarshal GET /edev/3: %v", err)
	}
	if byID.FlowReservationRequestListLink == nil || byID.FlowReservationRequestListLink.Href != "/edev/3/frq" {
		t.Errorf("GET /edev/3: FlowReservationRequestListLink = %+v, want href \"/edev/3/frq\" (from the URL key, not the stored Href)",
			byID.FlowReservationRequestListLink)
	}

	// List: has no key in hand, recovers "9" from the stored Href, so the
	// derived link agrees with the self href in the same document, and
	// neither agrees with the store key "3". That is the bound this test
	// pins, not a defect: it is the same bound LogEventListLink already has.
	//
	// The caller-scoped HTTP GET /edev filters out a malformed-href record
	// entirely (owner.go's callerDevices: "no client could address it"),
	// which is a pre-existing, correct behavior orthogonal to this decorator.
	// So this reads the full store through NewReaderStores directly, the way
	// the design's own evidence for this bound was gathered (#693 E4).
	readers := assembly.NewReaderStores(stores)
	listed, err := readers.EndDevices.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("NewReaderStores List: %v", err)
	}
	var sawSkewed, sawMalformed bool
	for _, dev := range listed.Items {
		switch dev.Href {
		case "/edev/9":
			sawSkewed = true
			if dev.FlowReservationRequestListLink == nil || dev.FlowReservationRequestListLink.Href != "/edev/9/frq" {
				t.Errorf("List /edev/9: FlowReservationRequestListLink = %+v, want href \"/edev/9/frq\" (agrees with self href, not the store key)",
					dev.FlowReservationRequestListLink)
			}
		case "/edev/3/rg":
			sawMalformed = true
			if dev.FlowReservationRequestListLink != nil || dev.FlowReservationResponseListLink != nil {
				t.Errorf("List /edev/3/rg: links = %+v / %+v, want both nil: a key that cannot be recovered must strip, not guess",
					dev.FlowReservationRequestListLink, dev.FlowReservationResponseListLink)
			}
		}
	}
	if !sawSkewed {
		t.Error("the skewed device (href /edev/9) did not appear in the full store List")
	}
	if !sawMalformed {
		t.Error("the malformed-href device did not appear in the full store List")
	}

	// The exported read handle's GetByLFDI, per the same derivation.
	byLFDI, err := readers.EndDevices.GetByLFDI(ctx, malformedLFDI)
	if err != nil {
		t.Fatalf("NewReaderStores GetByLFDI(malformed): %v", err)
	}
	if byLFDI.FlowReservationRequestListLink != nil || byLFDI.FlowReservationResponseListLink != nil {
		t.Errorf("NewReaderStores GetByLFDI(malformed): links = %+v / %+v, want both nil",
			byLFDI.FlowReservationRequestListLink, byLFDI.FlowReservationResponseListLink)
	}
}

// TestEndDeviceLinks_UnwiredAnchorAdvertisesNeither is P13 (E7): a deployment
// that does not mount the flow reservation routes must not advertise either
// link, including a value a client tried to install through the POST body.
// The 404 on the un-mounted route is the control that the wiring really is
// absent, not merely that the fields happen to read nil.
func TestEndDeviceLinks_UnwiredAnchorAdvertisesNeither(t *testing.T) {
	t.Parallel()

	stores := testStores()
	stores.FlowReservationRequests = nil
	stores.FlowReservationResponses = nil

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), testSFDI, testLFDI, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(forgedFlowReservationBody))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201", resp.StatusCode)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)
	if created.FlowReservationRequestListLink != nil {
		t.Errorf("unwired deployment: FlowReservationRequestListLink = %+v, want nil", created.FlowReservationRequestListLink)
	}
	if created.FlowReservationResponseListLink != nil {
		t.Errorf("unwired deployment: FlowReservationResponseListLink = %+v, want nil", created.FlowReservationResponseListLink)
	}

	// Control: the route really is unmounted, so the nil fields above are
	// not an accident of some other check.
	forgedResp, err := srv.Client().Get(srv.URL + created.Href + "/frq")
	if err != nil {
		t.Fatalf("GET %s/frq: %v", created.Href, err)
	}
	if forgedResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s/frq status = %d, want 404 (control: the route must be unmounted at this wiring)", created.Href, forgedResp.StatusCode)
	}
}
