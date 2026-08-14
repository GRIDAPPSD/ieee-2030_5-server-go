package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// derControlScopeKey mirrors the composite parent key the DERControl store is
// scoped by: id/fsaId/derpId. Both the list route and the single-resource
// route derive their store scope this way, which is what keeps one device's
// controls unreachable from another device's path.
func derControlScopeKey(edevID, fsaID, derpID string) string {
	return edevID + "/" + fsaID + "/" + derpID
}

// seedDERControl stores one DERControl under the (edev, fsa, derp) scope and
// returns it. The stored value carries every field class the served
// single-resource document must reproduce: identity (mRID), the activation
// window (interval), and an op-mode field.
//
// It deliberately does NOT set creationTime by name. That field is added to
// the Event base on a separate branch, and this route change must stand on
// its own against main. The whole-document byte comparison in
// TestSingleDERControlBytesMatchListMember covers it (and every other field)
// without naming it, because it compares the marshalled element rather than
// an enumerated field list: once creationTime exists, a divergence in it
// fails that test automatically.
// targetW is int16 because ActivePower.Value is xs:short on the wire; every
// magnitude used by these tests is well inside that range.
func seedDERControl(t *testing.T, stores *assembly.Stores, edevID, dercID string, targetW int16, eventTime int64) sep2.DERControl {
	t.Helper()

	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{
			OpModTargetW: &sep2.ActivePower{Value: targetW, Multiplier: 0},
		},
	}
	ctrl.Href = "/edev/" + edevID + "/fsa/1/derp/1/derc/" + dercID
	ctrl.MRID = "0123456789ABCDEF0123456789ABCDEF"
	ctrl.EventStatus = &sep2.EventStatus{
		CurrentStatus: sep2.EventStatusActive,
		DateTime:      eventTime,
	}
	ctrl.Interval = &sep2.DateTimeInterval{Start: eventTime, Duration: 900}

	if err := stores.DERControls.Create(
		context.Background(),
		derControlScopeKey(edevID, "1", "1"),
		dercID,
		ctrl,
	); err != nil {
		t.Fatalf("seed DERControl: %v", err)
	}
	return ctrl
}

func derControlRouter(t *testing.T, stores *assembly.Stores) *httptest.Server {
	t.Helper()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestSingleDERControlRouteServesActivatedEventHref asserts that a
// DERControl's OWN href resolves with 200.
//
// This is not cosmetic reachability. Once a client activates an event it arms
// a fast poll against that event's href (the EPRI reference client's
// activate_block sets event->poll_rate = active_poll_rate and calls
// poll_resource). With no route mounted, that GET returned 404, process_http
// turned the non-200 into RETRIEVE_FAIL, and der_client's RETRIEVE_FAIL arm
// called remove_stub, tearing the event down. The client then stopped polling
// and never observed any later control. So an unmounted single-resource route
// did not merely make one URL unavailable, it capped sustained control
// delivery to exactly one event.
func TestSingleDERControlRouteServesActivatedEventHref(t *testing.T) {
	t.Parallel()

	stores := testStores()
	want := seedDERControl(t, stores, testLFDI, "active-0", 5417, 1785429793)
	srv := derControlRouter(t, stores)

	resp, err := http.Get(srv.URL + want.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", want.Href, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("GET %s: want 200, got %d (body: %s)", want.Href, resp.StatusCode, body)
	}

	var got sep2.DERControl
	decodeXML(t, resp, &got)

	if got.Href != want.Href {
		t.Errorf("Href = %q, want %q", got.Href, want.Href)
	}
	if got.MRID != want.MRID {
		t.Errorf("MRID = %q, want %q", got.MRID, want.MRID)
	}
	if got.EventStatus == nil {
		t.Fatal("EventStatus is nil on the served single resource")
	}
	if got.EventStatus.CurrentStatus != want.EventStatus.CurrentStatus {
		t.Errorf("EventStatus.CurrentStatus = %d, want %d",
			got.EventStatus.CurrentStatus, want.EventStatus.CurrentStatus)
	}
	if got.Interval == nil {
		t.Fatal("Interval is nil on the served single resource")
	}
	if got.Interval.Start != want.Interval.Start || got.Interval.Duration != want.Interval.Duration {
		t.Errorf("Interval = {start %d, duration %d}, want {start %d, duration %d}",
			got.Interval.Start, got.Interval.Duration, want.Interval.Start, want.Interval.Duration)
	}
	if got.DERControlBase == nil || got.DERControlBase.OpModTargetW == nil {
		t.Fatal("DERControlBase.OpModTargetW is nil on the served single resource")
	}
	if got.DERControlBase.OpModTargetW.Value != want.DERControlBase.OpModTargetW.Value {
		t.Errorf("opModTargetW = %d, want %d",
			got.DERControlBase.OpModTargetW.Value, want.DERControlBase.OpModTargetW.Value)
	}
}

// TestSingleDERControlBytesMatchListMember asserts the single-resource
// document is field-for-field identical to the same control as the list
// serves it.
//
// A single-resource GET that serves a subtly different document than the list
// is a conformance trap: a client reconciling the two would see a spurious
// change, and (because creationTime is the supersession discriminator) a
// divergence there would make the client's supersession decision depend on
// which route it happened to read from. Comparing the marshalled member
// element rather than the structs catches a divergence introduced by field
// order or by a handler that rebuilds instead of serving the stored value.
func TestSingleDERControlBytesMatchListMember(t *testing.T) {
	t.Parallel()

	stores := testStores()
	want := seedDERControl(t, stores, testLFDI, "active-3", 8149, 1785429900)
	srv := derControlRouter(t, stores)

	singleResp, err := http.Get(srv.URL + want.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, singleResp, &single)

	listHref := "/edev/" + testLFDI + "/fsa/1/derp/1/derc"
	listResp, err := http.Get(srv.URL + listHref)
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	var list sep2.DERControlList
	decodeXML(t, listResp, &list)

	if len(list.DERControl) != 1 {
		t.Fatalf("list served %d controls, want 1", len(list.DERControl))
	}

	// Marshal both through the same encoder so any difference in the
	// element's own representation shows up as a byte difference.
	singleBytes, err := xml.Marshal(&single)
	if err != nil {
		t.Fatalf("marshal single: %v", err)
	}
	memberBytes, err := xml.Marshal(&list.DERControl[0])
	if err != nil {
		t.Fatalf("marshal list member: %v", err)
	}

	if string(singleBytes) != string(memberBytes) {
		t.Errorf("single-resource bytes differ from the list member's bytes\nsingle: %s\nmember: %s",
			singleBytes, memberBytes)
	}
}

// TestSingleDERControlRouteScopesByPathNotJustID asserts store SCOPING, not
// caller OWNERSHIP. Those are different properties.
//
// This test sends device B's OWN path (id=deviceB) carrying device A's exact
// dercId. It proves that the composite key id/fsaId/derpId means a dercId
// stored under A's scope is not visible under B's scope: a single-resource
// route that resolved by dercId alone, or that fell back to a global lookup
// on a scoped miss, would let any authenticated device enumerate every other
// device's controls by guessing an id.
//
// It does NOT prove that an authenticated caller is bound to the {id} in its
// own request path. Nothing in this test authenticates as device B and then
// requests a path naming device A's {id}; that is the actual cross-device
// read Leon's review demonstrated live against this same route, and closing
// it is a separate ownership-enforcement fix, not this route's scoping
// logic. Do not read a pass here as evidence that cross-device reads are
// rejected.
func TestSingleDERControlRouteScopesByPathNotJustID(t *testing.T) {
	t.Parallel()

	const deviceA = "AAAA1111BBBB2222CCCC3333DDDD4444EEEE5555"
	const deviceB = "FFFF9999EEEE8888DDDD7777CCCC6666BBBB5555"

	stores := testStores()
	victim := seedDERControl(t, stores, deviceA, "active-0", 7194, 1785430000)
	srv := derControlRouter(t, stores)

	// Sanity: the control IS readable on its owner's path, so a 404 on
	// device B's path below is scoping and not a broken seed.
	ownerResp, err := http.Get(srv.URL + victim.Href)
	if err != nil {
		t.Fatalf("GET owner path: %v", err)
	}
	_ = ownerResp.Body.Close()
	if ownerResp.StatusCode != http.StatusOK {
		t.Fatalf("owner path returned %d, want 200; seed or scope is wrong", ownerResp.StatusCode)
	}

	// The attack: device B's path, device A's dercId.
	crossHref := "/edev/" + deviceB + "/fsa/1/derp/1/derc/active-0"
	crossResp, err := http.Get(srv.URL + crossHref)
	if err != nil {
		t.Fatalf("GET cross-device path: %v", err)
	}
	body, _ := io.ReadAll(crossResp.Body)
	_ = crossResp.Body.Close()

	if crossResp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-device GET %s returned %d, want 404; device B must not read device A's control\nbody: %s",
			crossHref, crossResp.StatusCode, body)
	}
	if len(body) > 0 && xml.Unmarshal(body, new(sep2.DERControl)) == nil {
		t.Errorf("cross-device GET returned a parseable DERControl document; device A's control leaked to device B\nbody: %s", body)
	}
}

// TestSingleDERControlRouteReturnsNotFoundForAbsentID asserts a genuinely
// absent dercId is a clean 404, not a 500 and not an empty 200.
//
// A 500 would tell a client the server is broken rather than that the control
// is gone, and an empty 200 is worse still: the client would parse a
// zero-valued DERControl (creationTime 0, no interval) as a real event and
// could act on it. The EPRI client's own removal path is driven by a non-200,
// so a clean 404 is also what lets it retire an event that legitimately
// expired.
func TestSingleDERControlRouteReturnsNotFoundForAbsentID(t *testing.T) {
	t.Parallel()

	stores := testStores()
	// Seed one control so the scope exists but the requested id does not.
	seedDERControl(t, stores, testLFDI, "active-0", 2635, 1785430100)
	srv := derControlRouter(t, stores)

	absent := "/edev/" + testLFDI + "/fsa/1/derp/1/derc/active-999"
	resp, err := http.Get(srv.URL + absent)
	if err != nil {
		t.Fatalf("GET absent: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s returned %d, want 404\nbody: %s", absent, resp.StatusCode, body)
	}
	if len(body) > 0 && xml.Unmarshal(body, new(sep2.DERControl)) == nil {
		t.Errorf("absent-id GET returned a parseable DERControl document; want a plain not-found\nbody: %s", body)
	}
}

// TestSingleDERControlRouteRejectsNonGET asserts the route is read-only.
//
// The DOWN path writes controls through the store, never over HTTP, so
// mounting anything but GET/HEAD here would create a write surface no
// component needs and that no ownership check on this route would cover.
func TestSingleDERControlRouteRejectsNonGET(t *testing.T) {
	t.Parallel()

	stores := testStores()
	want := seedDERControl(t, stores, testLFDI, "active-0", 9371, 1785430200)
	srv := derControlRouter(t, stores)

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		req, err := http.NewRequest(method, srv.URL+want.Href, nil)
		if err != nil {
			t.Fatalf("build %s request: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s %s returned 200; the route must be read-only", method, want.Href)
		}
	}
}
