package assembly_test

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// testFRPDeadline is the deadline fallback bound this file's tests run
// with: short enough to keep them fast, since #666 no longer answers a
// FlowReservationRequest synchronously on POST. waitForFRPList is how a
// test observes the fallback firing.
const testFRPDeadline = 20 * time.Millisecond

// waitForFRPList polls path for up to 2s until it carries at least want
// members, the poll-with-deadline idiom internal/server's admin tests use
// for an async effect with no signal channel of its own.
func waitForFRPList(t *testing.T, srv *httptest.Server, path string, want int) sep2.FlowReservationResponseList {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		var list sep2.FlowReservationResponseList
		decodeXML(t, resp, &list)
		if len(list.FlowReservationResponse) >= want {
			return list
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("GET %s: fewer than %d members within 2s", path, want)
	return sep2.FlowReservationResponseList{}
}

// The FlowReservation instance routes.
//
// POST /edev/{id}/frq hands back a Location at /edev/{id}/frq/{frqId} and
// stamps a sibling FlowReservationResponse href at /edev/{id}/frp/{frpId}.
// Before these routes existed both hrefs 404d, which is the defect the
// boot-time href assertion enumerates: the server advertises a URI it will not
// answer, and a conforming client reads that as a server fault.
//
// The WADL declares both resources. FlowReservationRequest at
// sep_wadl.xml:3954 (samplePath /edev/{id1}/frq/{id2}) has GET and HEAD mode M,
// PUT mode M, POST mode E, DELETE mode O. FlowReservationResponse at
// sep_wadl.xml:4031 (samplePath /edev/{id1}/frp/{id2}) has GET and HEAD mode M,
// PUT and POST mode E, DELETE mode D. This card mounts the Mandatory reads
// only; the Mandatory PUT on FlowReservationRequest is a write surface and is
// reported rather than added here.

// frqServer builds a fully wired router and returns it with its stores. The
// device ids this file addresses are seeded as the test identity's own.
func frqServer(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()
	return frqServerWithConfig(t, assembly.RouterConfig{})
}

// frqServerWithConfig is frqServer with the caller's own RouterConfig, so a
// test can exercise a configured RouterConfig.PEN through the real mounted
// route rather than only through HandlePostFlowReservationRequest called
// directly (#665 fix round 3: a mutant dropping cfg.PEN on the way to that
// call, anywhere across assembly.go, registerNewFunctionSetRoutes, or
// HandlePostFlowReservationRequest's own parameter, survived every other
// test because none of them read RouterConfig.PEN through this route).
func frqServerWithConfig(t *testing.T, cfg assembly.RouterConfig) (*httptest.Server, *assembly.Stores) {
	t.Helper()

	if cfg.FlowReservationDeadline == 0 {
		cfg.FlowReservationDeadline = testFRPDeadline
	}
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1", "deviceA", "deviceB")
	handler, _ := assembly.BuildProtocolRouter(
		cfg,
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

// postFlowReservationRequest posts a request under edevID and returns the
// Location header the server minted for it.
func postFlowReservationRequest(t *testing.T, srv *httptest.Server, edevID, mrid string) string {
	t.Helper()

	duration := uint16(3600)
	body, err := xml.Marshal(&sep2.FlowReservationRequest{
		MRID:              mrid,
		Description:       "fast charge",
		DurationRequested: &duration,
	})
	if err != nil {
		t.Fatalf("marshal FlowReservationRequest: %v", err)
	}

	resp, err := http.Post(srv.URL+"/edev/"+edevID+"/frq", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /edev/%s/frq: %v", edevID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/%s/frq status = %d, want 201", edevID, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /edev/{id}/frq returned no Location; there is no href to follow")
	}
	return loc
}

// TestFlowReservationRequest_LocationHeaderResolves follows the server's own
// Location header, which is the strongest form of the advertisement: the server
// told the client where the resource it just created lives.
//
// It asserts the served document's field values rather than only the status,
// because a 200 carrying a zero-valued resource would satisfy the routing check
// while telling the client the reservation it just made is empty.
func TestFlowReservationRequest_LocationHeaderResolves(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	const mrid = "0102030405060708090A0B0C0D0E0F10"
	loc := postFlowReservationRequest(t, srv, "e1", mrid)

	resp, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: the server minted this href itself", loc, resp.StatusCode)
	}

	var got sep2.FlowReservationRequest
	decodeXML(t, resp, &got)

	if got.Href != loc {
		t.Errorf("Href = %q, want %q: the served self href must be the one the Location named", got.Href, loc)
	}
	if got.MRID != mrid {
		t.Errorf("mRID = %q, want %q", got.MRID, mrid)
	}
	if got.Description != "fast charge" {
		t.Errorf("description = %q, want %q", got.Description, "fast charge")
	}
	if got.DurationRequested == nil || *got.DurationRequested != 3600 {
		t.Errorf("durationRequested = %v, want 3600", got.DurationRequested)
	}
	if got.CreationTime == 0 {
		t.Error("creationTime = 0: a served event with no creation instant cannot be ordered against another")
	}
}

// TestFlowReservationRequest_HoldsUntilTheConfiguredDeadline is #736's
// coverage MEDIUM: a route-level proof that POST answers nothing before
// its configured deadline elapses, distinct from every other test in this
// file, which only waits FOR the deadline and never checks the state
// before it. A wiring mutant that answers synchronously, or a deadline cap
// that fires at once despite a far-future start, would still pass those;
// this catches both.
func TestFlowReservationRequest_HoldsUntilTheConfiguredDeadline(t *testing.T) {
	t.Parallel()

	const deadline = 150 * time.Millisecond
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: deadline})

	duration := uint32(3600)
	farFuture := time.Now().Add(time.Hour).Unix()
	body, err := xml.Marshal(&sep2.FlowReservationRequest{
		MRID:              "7172737475767778797A7B7C7D7E7F80",
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 1000},
		IntervalRequested: &sep2.DateTimeInterval{Start: farFuture, Duration: duration},
	})
	if err != nil {
		t.Fatalf("marshal FlowReservationRequest: %v", err)
	}
	resp, err := http.Post(srv.URL+"/edev/e1/frq", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /edev/e1/frq: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/e1/frq status = %d, want 201", resp.StatusCode)
	}

	// Before the deadline: nothing answered yet.
	before, err := http.Get(srv.URL + "/edev/e1/frp")
	if err != nil {
		t.Fatalf("GET /edev/e1/frp: %v", err)
	}
	var beforeList sep2.FlowReservationResponseList
	decodeXML(t, before, &beforeList)
	if len(beforeList.FlowReservationResponse) != 0 {
		t.Fatalf("FlowReservationResponseList before the deadline has %d members, want 0: the request was answered synchronously or the deadline fired at once", len(beforeList.FlowReservationResponse))
	}

	// After the deadline: the fallback has answered.
	list := waitForFRPList(t, srv, "/edev/e1/frp", 1)
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("FlowReservationResponseList after the deadline has %d members, want 1", len(list.FlowReservationResponse))
	}
}

// TestFlowReservationResponse_HrefFromTheListResolves walks the link the client
// actually walks: POST the request, read the FlowReservationResponseList, take a
// member's own href, and follow it. The response href is where the server
// records its decision, so a client that cannot fetch it never learns whether
// the reservation was granted.
func TestFlowReservationResponse_HrefFromTheListResolves(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	const mrid = "1112131415161718191A1B1C1D1E1F20"
	postFlowReservationRequest(t, srv, "e1", mrid)

	// #666: no response until the deadline fallback answers, since nothing
	// here denies or grants explicitly.
	list := waitForFRPList(t, srv, "/edev/e1/frp", 1)
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("FlowReservationResponseList has %d members, want 1", len(list.FlowReservationResponse))
	}
	href := list.FlowReservationResponse[0].Href
	if href == "" {
		t.Fatal("the list member carries no href; there is nothing to follow")
	}

	resp, err := http.Get(srv.URL + href)
	if err != nil {
		t.Fatalf("GET %s: %v", href, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", href, resp.StatusCode)
	}

	var got sep2.FlowReservationResponse
	decodeXML(t, resp, &got)

	if got.Href != href {
		t.Errorf("Href = %q, want %q", got.Href, href)
	}
	if got.Subject != mrid {
		t.Errorf("subject = %q, want %q: the response must name the request it answers", got.Subject, mrid)
	}
	// #665: the response's own mRID (distinct from Subject, which names the
	// request), asserted through the real mounted route rather than only a
	// hand-wired copy of the handler.
	if len(got.MRID) != 32 {
		t.Errorf("MRID = %q, want 32 hex digits", got.MRID)
	} else if _, err := hex.DecodeString(got.MRID); err != nil || got.MRID != strings.ToUpper(got.MRID) {
		t.Errorf("MRID = %q, want 32 uppercase hex digits", got.MRID)
	}
	if got.MRID == mrid {
		t.Errorf("MRID = %q equals the request mRID; the response's own identity must not be the request's", got.MRID)
	}
	if got.CreationTime == 0 {
		t.Error("creationTime = 0 on the served FlowReservationResponse")
	}
	if got.EventStatus == nil {
		t.Fatal("EventStatus is absent: a client cannot tell whether the reservation was granted")
	}
	if got.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("EventStatus.currentStatus = %d, want %d", got.EventStatus.CurrentStatus, sep2.EventStatusActive)
	}
}

// TestFlowReservationResponse_ConfiguredPENReachesTheMountedRoute is fix
// round 3's coverage gap: every other PEN test calls
// HandlePostFlowReservationRequest or newFRPMRID directly, so a mutant that
// drops RouterConfig.PEN anywhere between BuildProtocolRouter and that call
// (assembly.go's own call site, registerNewFunctionSetRoutes' parameter, or
// HandlePostFlowReservationRequest's) passed every test but this one. This
// test goes through the real mounted route, exactly as an operator's
// SEP2_PEN reaches a running server.
func TestFlowReservationResponse_ConfiguredPENReachesTheMountedRoute(t *testing.T) {
	t.Parallel()

	pen := uint32(0x40732001)
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{PEN: &pen})
	const mrid = "2122232425262728292A2B2C2D2E2F31"
	postFlowReservationRequest(t, srv, "e1", mrid)

	list := waitForFRPList(t, srv, "/edev/e1/frp", 1)
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("FlowReservationResponseList has %d members, want 1", len(list.FlowReservationResponse))
	}

	got := list.FlowReservationResponse[0].MRID
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatalf("MRID %q is not hex: %v", got, err)
	}
	if gotPEN := binary.BigEndian.Uint32(raw[12:]); gotPEN != pen {
		t.Fatalf("low 32 bits of MRID %q = %#x, want configured PEN %#x", got, gotPEN, pen)
	}
}

// TestFlowReservationInstances_UnknownIDIsACleanNotFound: a miss is a 404 and
// never a synthesized zero-valued resource. Fabricating a 200 here would hand a
// client a reservation the server never granted.
//
// The known-good sibling is fetched first, in this same test, on purpose. A
// 404-only assertion is vacuous while the route is unmounted, because an
// unrouted path 404s too: it would have passed against the very defect this
// card fixes. Requiring a 200 from the same shape first is what makes the 404
// mean "the resource is absent" rather than "the route is".
func TestFlowReservationInstances_UnknownIDIsACleanNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	loc := postFlowReservationRequest(t, srv, "e1", "2122232425262728292A2B2C2D2E2F30")

	list := waitForFRPList(t, srv, "/edev/e1/frp", 1)
	if len(list.FlowReservationResponse) != 1 {
		t.Fatalf("FlowReservationResponseList has %d members, want 1", len(list.FlowReservationResponse))
	}

	for _, tc := range []struct{ name, present, missing, root string }{
		{"frq", loc, "/edev/e1/frq/nosuch", "<FlowReservationRequest"},
		{"frp", list.FlowReservationResponse[0].Href, "/edev/e1/frp/nosuch", "<FlowReservationResponse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			present, err := http.Get(srv.URL + tc.present)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.present, err)
			}
			_ = present.Body.Close()
			if present.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: without a served sibling the 404 below proves nothing",
					tc.present, present.StatusCode)
			}

			resp, err := http.Get(srv.URL + tc.missing)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.missing, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", resp.StatusCode)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if strings.Contains(string(body), tc.root) {
				t.Errorf("a 404 body must not carry a %s document, got: %s", tc.root, body)
			}
		})
	}
}

// TestFlowReservationInstances_ScopeBindsToTheDeviceInThePath asserts the
// instance is reachable only under the EndDevice it was stored beneath.
//
// This is store scoping, NOT caller ownership: nothing in core checks that the
// caller is the device named by {id}, and this test does not claim otherwise
// (ownership is a separate cross-cutting sweep).
func TestFlowReservationInstances_ScopeBindsToTheDeviceInThePath(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	loc := postFlowReservationRequest(t, srv, "deviceB", "3132333435363738393A3B3C3D3E3F40")

	// The same frq id, asked for under a different device.
	foreign := strings.Replace(loc, "/edev/deviceB/", "/edev/deviceA/", 1)
	if foreign == loc {
		t.Fatalf("test setup: could not rewrite %q to a foreign device", loc)
	}

	resp, err := http.Get(srv.URL + foreign)
	if err != nil {
		t.Fatalf("GET %s: %v", foreign, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s status = %d, want 404: device A must not reach device B's reservation", foreign, resp.StatusCode)
	}

	// And it is still there for its own device, so the 404 above is scoping
	// rather than a failed POST.
	own, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	_ = own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, own.StatusCode)
	}
}

// TestFlowReservationInstances_UnservedMethodsGet405WithAnAccurateAllow covers
// section 4.3 c) 4): a declared method this server does not implement needs an
// explicit 405, which an unmounted path cannot give because it 404s.
//
// Where the 405 comes from, stated plainly because a previous card in this file
// shipped an assertion that passed for the wrong reason: these mounts register a
// GET pattern only, so http.ServeMux answers a non-GET before the handler runs
// and derives Allow from the registered patterns itself. That is still the
// served method set, which is the property being asserted, but it means this
// test does not exercise scopedResourceHandler's own 405 branch. That branch is
// covered directly in scopedresource_internal_test.go against itemMethods.
//
// Neutralization check for the next reader: register a PUT pattern for either
// shape and this test fails with Allow "GET, HEAD, PUT".
func TestFlowReservationInstances_UnservedMethodsGet405WithAnAccurateAllow(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	loc := postFlowReservationRequest(t, srv, "e1", "4142434445464748494A4B4C4D4E4F50")
	frpPath := "/edev/e1/frp/frp-1"

	for _, path := range []string{loc, frpPath} {
		for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch} {
			t.Run(method+" "+path, func(t *testing.T) {
				req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				_ = resp.Body.Close()

				if resp.StatusCode != http.StatusMethodNotAllowed {
					t.Errorf("status = %d, want 405", resp.StatusCode)
				}
				if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
					t.Errorf("Allow = %q, want %q: an Allow that overstates what is served is barely better than a 404", got, "GET, HEAD")
				}
			})
		}
	}
}

// TestFlowReservationInstances_HEADIsServedByTheGETPattern pins the stdlib
// property that a GET pattern also matches HEAD. HEAD is Mandatory on both
// resources (sep_wadl.xml:3962 and 4039), so a future move off http.ServeMux
// fails here rather than dropping two Mandatory methods quietly.
func TestFlowReservationInstances_HEADIsServedByTheGETPattern(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	loc := postFlowReservationRequest(t, srv, "e1", "5152535455565758595A5B5C5D5E5F60")

	req, err := http.NewRequest(http.MethodHead, srv.URL+loc, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("HEAD returned a %d-byte body, want none: %s", len(body), body)
	}
}

// TestFlowReservationRequest_InvalidRequestStatusRefusedThroughTheMountedRoute
// is #692 exercised through the real mounted route rather than only against
// HandlePostFlowReservationRequest called directly: BuildProtocolRouter is
// what an operator's server actually runs, and nothing else in this package
// posts a RequestStatus the schema forbids.
func TestFlowReservationRequest_InvalidRequestStatusRefusedThroughTheMountedRoute(t *testing.T) {
	t.Parallel()

	srv, _ := frqServer(t)
	body := `<FlowReservationRequest xmlns="urn:ieee:std:2030.5:ns"><mRID>6162636465666768696A6B6C6D6E6F70</mRID>` +
		`<RequestStatus><dateTime>1727136000</dateTime><requestStatus>200</requestStatus></RequestStatus></FlowReservationRequest>`

	resp, err := http.Post(srv.URL+"/edev/e1/frq", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /edev/e1/frq: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	listResp, err := http.Get(srv.URL + "/edev/e1/frq")
	if err != nil {
		t.Fatalf("GET /edev/e1/frq: %v", err)
	}
	var list sep2.FlowReservationRequestList
	decodeXML(t, listResp, &list)
	if len(list.FlowReservationRequest) != 0 {
		t.Errorf("FlowReservationRequestList has %d members, want 0: a refused request must not be stored", len(list.FlowReservationRequest))
	}
}
