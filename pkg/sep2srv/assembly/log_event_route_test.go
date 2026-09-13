package assembly_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The LogEvent function set at its WADL address.
//
// The list and the POST were mounted at /edev/{id}/log and the instance was
// mounted nowhere, while the WADL declares the list at /edev/{id1}/lel
// (sep_wadl.xml:1358) and the instance at /edev/{id1}/lel/{id2}
// (sep_wadl.xml:1404). 2018 A.3.5.1 and A.3.5.2 give the same sample URIs, and
// Annex A.1 p.132 makes the WADL normative. So the data was served correctly at
// an address no conforming client looks for, and the instance the POST minted a
// Location for was served at no address at all.
//
// The /log routes are REMOVED rather than kept as an alias. Nothing advertised
// them: no production path assigned LogEventListLink before this, so the
// only way to reach /log was to know the string. An undeclared second address
// is a second surface to keep conformant forever, and a resource reachable at
// only one of two addresses shows a client a different LogEvent set depending
// on which it used. One address, the declared one, is the whole fix.
//
// Method coverage follows sep_wadl.xml:1358 and :1404. On the list GET, HEAD
// and POST are mode M; PUT and DELETE are mode E. On the instance GET, HEAD and
// DELETE are mode M; PUT is mode O and POST is mode E. Every Mandatory method
// is mounted. The mode E and mode O methods answer 405 from http.ServeMux,
// which derives Allow from the registered method set, so the Allow header
// cannot disagree with what is served.

// lelServer builds a fully wired router and returns it with its stores. The
// device ids this file addresses are seeded as the test identity's own.
func lelServer(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "d1", "d2", "7")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

// sampleLogEvent is a POPULATED fixture. A zero-value LogEvent marshals to a
// document whose every field is the zero value, so an assertion against it
// cannot tell a served field from a dropped one; that blind spot is how the
// v0.12.0 wire regression stayed invisible.
func sampleLogEvent(id uint16, code uint8) sep2.LogEvent {
	extended := int64(9000 + int64(id))
	return sep2.LogEvent{
		CreatedDateTime: 1604963587 + int64(id),
		// 18 characters. sep.xsd types details as String32, and the wire-format
		// gate does not enforce maxLength, so
		// a fixture longer than 32 would go undetected here while being invalid
		// on the standard's terms.
		Details:      "gen software alarm",
		ExtendedData: &extended,
		FunctionSet:  sep2.FunctionSetLogEvent,
		LogEventCode: code,
		LogEventID:   id,
		LogEventPEN:  54465,
		ProfileID:    2,
	}
}

// postLogEvent posts evt to the LogEventList of edevID and returns the minted
// Location. It posts to the WADL address, which is the address a client reaches
// by following EndDevice.LogEventListLink.
func postLogEvent(t *testing.T, srv *httptest.Server, edevID string, evt sep2.LogEvent) string {
	t.Helper()

	body, err := xml.Marshal(&evt)
	if err != nil {
		t.Fatalf("marshal LogEvent: %v", err)
	}

	resp, err := http.Post(srv.URL+"/edev/"+edevID+"/lel", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /edev/%s/lel: %v", edevID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/%s/lel status = %d, want 201", edevID, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /edev/{id}/lel returned no Location; there is no href to follow")
	}
	return loc
}

// getBytes issues a GET and returns the status and the raw body. The bytes are
// returned, not a decoded struct, because a round trip through encoding/xml
// cannot see an element emitted as an attribute or the reverse.
func getBytes(t *testing.T, srv *httptest.Server, path string) (int, []byte) {
	t.Helper()

	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body for GET %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// TestLogEvent_ListIsServedAtTheWADLAddress is the list half of the fix.
func TestLogEvent_ListIsServedAtTheWADLAddress(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	postLogEvent(t, srv, "d1", sampleLogEvent(1, 27))

	status, body := getBytes(t, srv, "/edev/d1/lel")
	if status != http.StatusOK {
		t.Fatalf("GET /edev/d1/lel status = %d, want 200 (sep_wadl.xml:1358 declares GET mode M)", status)
	}

	var list sep2.LogEventList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal LogEventList: %v\nbody: %s", err, body)
	}
	if list.Href != "/edev/d1/lel" {
		t.Errorf("LogEventList href = %q, want %q", list.Href, "/edev/d1/lel")
	}
	if list.All != 1 || list.Results != 1 {
		t.Errorf("all=%d results=%d, want 1 and 1", list.All, list.Results)
	}
	if len(list.LogEvent) != 1 {
		t.Fatalf("LogEvent count = %d, want 1", len(list.LogEvent))
	}
	if got := list.LogEvent[0].LogEventID; got != 1 {
		t.Errorf("logEventID = %d, want 1", got)
	}
}

// TestLogEvent_LocationHeaderResolves follows the server's own Location and
// asserts the served field values, not merely a 200.
//
// This is the assertion the card names: the Location POST returns must RESOLVE,
// verified by following it. Before this card the POST stamped an href under
// /log and no route existed at that path or at the WADL one, so the server
// handed every reporting device a URI it would answer 404 on.
func TestLogEvent_LocationHeaderResolves(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	want := sampleLogEvent(7, 27)
	loc := postLogEvent(t, srv, "d1", want)

	if !strings.HasPrefix(loc, "/edev/d1/lel/") {
		t.Fatalf("Location = %q, want a path under %q (sep_wadl.xml:1404)", loc, "/edev/d1/lel/")
	}

	status, body := getBytes(t, srv, loc)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: the server minted this href itself", loc, status)
	}

	var got sep2.LogEvent
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal LogEvent: %v\nbody: %s", err, body)
	}
	if got.Href != loc {
		t.Errorf("href = %q, want %q", got.Href, loc)
	}
	if got.CreatedDateTime != want.CreatedDateTime {
		t.Errorf("createdDateTime = %d, want %d", got.CreatedDateTime, want.CreatedDateTime)
	}
	if got.Details != want.Details {
		t.Errorf("details = %q, want %q", got.Details, want.Details)
	}
	if got.ExtendedData == nil || *got.ExtendedData != *want.ExtendedData {
		t.Errorf("extendedData = %v, want %d", got.ExtendedData, *want.ExtendedData)
	}
	if got.FunctionSet != want.FunctionSet {
		t.Errorf("functionSet = %d, want %d", got.FunctionSet, want.FunctionSet)
	}
	if got.LogEventCode != want.LogEventCode {
		t.Errorf("logEventCode = %d, want %d", got.LogEventCode, want.LogEventCode)
	}
	if got.LogEventID != want.LogEventID {
		t.Errorf("logEventID = %d, want %d", got.LogEventID, want.LogEventID)
	}
	if got.LogEventPEN != want.LogEventPEN {
		t.Errorf("logEventPEN = %d, want %d", got.LogEventPEN, want.LogEventPEN)
	}
	if got.ProfileID != want.ProfileID {
		t.Errorf("profileID = %d, want %d", got.ProfileID, want.ProfileID)
	}
}

// TestLogEvent_ScopeBindsToTheEndDeviceInThePath seeds TWO EndDevices.
//
// One device cannot distinguish correct scoping from a handler that ignores
// scope and returns everything: with a single parent both behaviours produce
// the same list. Two devices, each with its own event, separate them.
func TestLogEvent_ScopeBindsToTheEndDeviceInThePath(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	locA := postLogEvent(t, srv, "d1", sampleLogEvent(11, 27))
	locB := postLogEvent(t, srv, "d2", sampleLogEvent(22, 28))

	for _, tc := range []struct {
		device string
		wantID uint16
	}{{"d1", 11}, {"d2", 22}} {
		status, body := getBytes(t, srv, "/edev/"+tc.device+"/lel")
		if status != http.StatusOK {
			t.Fatalf("GET /edev/%s/lel status = %d, want 200", tc.device, status)
		}
		var list sep2.LogEventList
		if err := xml.Unmarshal(body, &list); err != nil {
			t.Fatalf("unmarshal LogEventList for %s: %v", tc.device, err)
		}
		if len(list.LogEvent) != 1 {
			t.Fatalf("GET /edev/%s/lel returned %d events, want exactly 1: a handler that ignored scope would return both",
				tc.device, len(list.LogEvent))
		}
		if list.LogEvent[0].LogEventID != tc.wantID {
			t.Errorf("GET /edev/%s/lel returned logEventID %d, want %d",
				tc.device, list.LogEvent[0].LogEventID, tc.wantID)
		}
	}

	// The instance is reachable only under the device it was posted to.
	foreign := strings.Replace(locA, "/edev/d1/", "/edev/d2/", 1)
	if foreign == locA {
		t.Fatalf("test setup: could not rewrite %q to a foreign device", locA)
	}
	if status, _ := getBytes(t, srv, foreign); status != http.StatusNotFound {
		t.Errorf("GET %s status = %d, want 404: an event must not be reachable under a device it was not posted to",
			foreign, status)
	}
	if status, _ := getBytes(t, srv, locB); status != http.StatusOK {
		t.Errorf("GET %s status = %d, want 200: without a served sibling the 404 above proves nothing", locB, status)
	}
}

// TestLogEvent_UnknownIDIsACleanNotFound: a miss is a 404 and never a
// synthesized zero-valued resource.
//
// The present event is fetched first, in this same test. A 404-only assertion
// is vacuous while the route is unmounted, because an unrouted path 404s too:
// that exact test was once green against the very defect it was meant to
// catch.
func TestLogEvent_UnknownIDIsACleanNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	loc := postLogEvent(t, srv, "d1", sampleLogEvent(3, 27))

	if status, _ := getBytes(t, srv, loc); status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: without a served sibling the 404 below proves nothing", loc, status)
	}

	status, body := getBytes(t, srv, "/edev/d1/lel/nosuch")
	if status != http.StatusNotFound {
		t.Fatalf("GET /edev/d1/lel/nosuch status = %d, want 404", status)
	}
	if strings.Contains(string(body), "<LogEvent") {
		t.Errorf("a 404 body must not carry a LogEvent document, got: %s", body)
	}
}

// TestLogEvent_DeleteIsServedAndScoped covers DELETE on the instance, which is
// mode M at sep_wadl.xml:1430.
//
// DELETE is a WRITE, and this mount carries no ownership binding: any
// authenticated caller that knows a path can delete the event under it. That
// is a separate cross-cutting gap (retargeted to server-go by ADR-002), not
// something this route invented; the store scoping asserted below is what
// this layer does enforce, and it is not the same property.
func TestLogEvent_DeleteIsServedAndScoped(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	locA := postLogEvent(t, srv, "d1", sampleLogEvent(41, 27))
	locB := postLogEvent(t, srv, "d2", sampleLogEvent(42, 27))

	// A DELETE aimed at the foreign device's path must not reach the event.
	foreign := strings.Replace(locA, "/edev/d1/", "/edev/d2/", 1)
	if code := statusOf(t, http.MethodDelete, srv.URL+foreign); code != http.StatusNotFound {
		t.Errorf("DELETE %s status = %d, want 404: the id belongs to another device's scope", foreign, code)
	}
	if status, _ := getBytes(t, srv, locA); status != http.StatusOK {
		t.Fatalf("GET %s status = %d after a foreign DELETE, want 200: the event must survive", locA, status)
	}

	if code := statusOf(t, http.MethodDelete, srv.URL+locA); code != http.StatusNoContent {
		t.Fatalf("DELETE %s status = %d, want 204", locA, code)
	}
	if status, _ := getBytes(t, srv, locA); status != http.StatusNotFound {
		t.Errorf("GET %s status = %d after DELETE, want 404", locA, status)
	}

	// The list reflects the removal, and the other device is untouched.
	status, body := getBytes(t, srv, "/edev/d1/lel")
	if status != http.StatusOK {
		t.Fatalf("GET /edev/d1/lel status = %d, want 200", status)
	}
	var list sep2.LogEventList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal LogEventList: %v", err)
	}
	if list.All != 0 || len(list.LogEvent) != 0 {
		t.Errorf("after DELETE the list holds all=%d results=%d items=%d, want an empty list",
			list.All, list.Results, len(list.LogEvent))
	}
	if status, _ := getBytes(t, srv, locB); status != http.StatusOK {
		t.Errorf("GET %s status = %d, want 200: deleting d1's event must not touch d2's", locB, status)
	}

	// A second DELETE of the same id is a clean 404, not a 204 that pretends
	// to have removed something.
	if code := statusOf(t, http.MethodDelete, srv.URL+locA); code != http.StatusNotFound {
		t.Errorf("second DELETE %s status = %d, want 404", locA, code)
	}
}

// TestLogEvent_InstanceUnservedMethodsGet405WithAnAccurateAllow: PUT is mode O
// and POST is mode E at sep_wadl.xml:1404, and neither is implemented, so each
// needs an explicit 405 rather than the 404 an unmounted path gives (section
// 4.3 c) 4)).
//
// The 405 comes from http.ServeMux, which derives Allow from the registered
// method set, so Allow cannot lie about what is served. Neutralization check:
// register a PUT pattern for this shape and this test fails on the Allow value.
func TestLogEvent_InstanceUnservedMethodsGet405WithAnAccurateAllow(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	loc := postLogEvent(t, srv, "d1", sampleLogEvent(5, 27))

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, srv.URL+loc, strings.NewReader(""))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != "DELETE, GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "DELETE, GET, HEAD")
			}
		})
	}
}

// TestLogEvent_ListEModeMethodsAreRefusedExplicitly records the mode E methods
// on the LIST (sep_wadl.xml:1358): PUT and DELETE.
//
// Neither is implemented and neither is in scope here: mounting PUT on a list
// is a separate WADL-declared-method sweep. What this test DOES require
// is that the refusal is an explicit 405 with an accurate Allow rather than the
// 404 an unmounted path produces, because a 404 is indistinguishable from "this
// server has no LogEvent function set at all" and would let a missing route
// pass as a conformant refusal. The assertion is written against the status
// that is actually served so a regression to 404 fails here.
func TestLogEvent_ListEModeMethodsAreRefusedExplicitly(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	postLogEvent(t, srv, "d1", sampleLogEvent(6, 27))

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, srv.URL+"/edev/d1/lel", strings.NewReader(""))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s /edev/d1/lel returned 404: a mode E method needs an explicit refusal, "+
					"and a 404 here means the route is not mounted at all", method)
			}
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s /edev/d1/lel status = %d, want 405", method, resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != "GET, HEAD, POST" {
				t.Errorf("%s /edev/d1/lel Allow = %q, want %q", method, got, "GET, HEAD, POST")
			}
		})
	}
}

// TestLogEvent_HEADIsServedOnListAndInstance pins the stdlib property that a
// GET pattern also matches HEAD, which is mode M on both shapes
// (sep_wadl.xml:1378 for the list, :1411 for the instance).
func TestLogEvent_HEADIsServedOnListAndInstance(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	loc := postLogEvent(t, srv, "d1", sampleLogEvent(8, 27))

	for _, path := range []string{"/edev/d1/lel", loc} {
		req, err := http.NewRequest(http.MethodHead, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("HEAD %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD %s status = %d, want 200", path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("HEAD %s returned a %d-byte body, want none: %s", path, len(body), body)
		}
	}
}

// TestLogEvent_SerializedBytesCarryElementsNotAttributes asserts the WIRE FORM
// of both documents, not a round trip.
//
// A round trip cannot see an element-versus-attribute error: encoding/xml will
// happily write logEventCode as an attribute and read it back into the same
// field, and every value assertion still passes. That is exactly how a real
// regression once shipped. sep.xsd declares every LogEvent field below
// as an ELEMENT of the sequence, and href, all, results and pollRate as
// ATTRIBUTES, so both directions are pinned here.
func TestLogEvent_SerializedBytesCarryElementsNotAttributes(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	loc := postLogEvent(t, srv, "d1", sampleLogEvent(9, 27))

	t.Run("instance", func(t *testing.T) {
		status, body := getBytes(t, srv, loc)
		if status != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", loc, status)
		}
		got := string(body)

		for _, want := range []string{
			"<createdDateTime>1604963596</createdDateTime>",
			"<details>gen software alarm</details>",
			"<extendedData>9009</extendedData>",
			"<functionSet>10</functionSet>",
			"<logEventCode>27</logEventCode>",
			"<logEventID>9</logEventID>",
			"<logEventPEN>54465</logEventPEN>",
			"<profileID>2</profileID>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("served LogEvent does not contain %s\nbody: %s", want, got)
			}
		}
		if !strings.Contains(got, `href="`+loc+`"`) {
			t.Errorf("href must reach the wire as an attribute; body: %s", got)
		}
		// The mirror image: no LogEvent sequence element may be emitted as an
		// attribute of the root element.
		for _, forbidden := range []string{
			"createdDateTime=", "functionSet=", "logEventCode=",
			"logEventID=", "logEventPEN=", "profileID=", "details=", "extendedData=",
		} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%q reached the wire as an attribute; sep.xsd declares it as a sequence element\nbody: %s",
					forbidden, got)
			}
		}
		if !strings.Contains(got, `xmlns="urn:ieee:std:2030.5:ns"`) {
			t.Errorf("served LogEvent carries no sep2 namespace; body: %s", got)
		}
	})

	t.Run("list", func(t *testing.T) {
		status, body := getBytes(t, srv, "/edev/d1/lel")
		if status != http.StatusOK {
			t.Fatalf("GET /edev/d1/lel status = %d, want 200", status)
		}
		got := string(body)

		for _, want := range []string{
			`href="/edev/d1/lel"`,
			`all="1"`,
			`results="1"`,
			`pollRate="900"`,
			"<LogEvent ",
			"<logEventID>9</logEventID>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("served LogEventList does not contain %s\nbody: %s", want, got)
			}
		}
		if strings.Contains(got, "<all>") || strings.Contains(got, "<results>") || strings.Contains(got, "<pollRate>") {
			t.Errorf("all, results and pollRate are attributes in sep.xsd, not elements\nbody: %s", got)
		}
	})
}

// TestLogEvent_UndeclaredLogAddressIsGone records the decision to REMOVE the
// non-WADL /log alias rather than keep it.
//
// The invariant that forced the choice: no resource may be reachable at only
// one of two addresses, or a client sees a different LogEvent set depending on
// which path it used. Serving both correctly means keeping the list, the POST
// and the instance in step at two addresses forever, for an address the WADL
// does not declare and that nothing advertised. Removing it satisfies the
// invariant trivially and leaves one conformant surface.
func TestLogEvent_UndeclaredLogAddressIsGone(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	// A served sibling at the declared address first, so the 404s below are
	// evidence the alias is gone and not that the function set is unmounted.
	loc := postLogEvent(t, srv, "d1", sampleLogEvent(10, 27))
	if status, _ := getBytes(t, srv, loc); status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, status)
	}

	for _, path := range []string{"/edev/d1/log", "/edev/d1/log/" + strings.TrimPrefix(loc, "/edev/d1/lel/")} {
		if status, _ := getBytes(t, srv, path); status != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404: the undeclared /log alias is removed", path, status)
		}
	}
	resp, err := http.Post(srv.URL+"/edev/d1/log", "application/sep+xml", strings.NewReader("<LogEvent/>"))
	if err != nil {
		t.Fatalf("POST /edev/d1/log: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /edev/d1/log status = %d, want 404", resp.StatusCode)
	}
}

// TestEndDevice_AdvertisesLogEventListLinkWhenTheFunctionSetIsServed is the
// mount-and-advertise pair.
//
// Before this card no production path assigned LogEventListLink at all: the
// only assignments in the tree were in a wire-order test fixture and nowhere
// else, so even the wrong address was unadvertised and CSIP BASIC-027 step 2
// ("find the LogEventListLink in its EndDevice") could not pass. Mounting a
// route without advertising it leaves the function set dark, which is why both
// halves are asserted here and in the same test.
func TestEndDevice_AdvertisesLogEventListLinkWhenTheFunctionSetIsServed(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)

	resp, err := http.Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201", resp.StatusCode)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)

	if created.LogEventListLink == nil {
		t.Fatal("the created EndDevice carries no LogEventListLink; CSIP BASIC-027 step 2 cannot pass")
	}
	want := created.Href + "/lel"
	if created.LogEventListLink.Href != want {
		t.Errorf("LogEventListLink href = %q, want %q (sep_wadl.xml:1358)", created.LogEventListLink.Href, want)
	}

	// The link is served on the READ paths too, not only on the POST response.
	// A link present only on the creation response is invisible to a client
	// that re-reads its own EndDevice, which is what a client does on restart.
	status, body := getBytes(t, srv, created.Href)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", created.Href, status)
	}
	var reread sep2.EndDevice
	if err := xml.Unmarshal(body, &reread); err != nil {
		t.Fatalf("unmarshal EndDevice: %v", err)
	}
	if reread.LogEventListLink == nil || reread.LogEventListLink.Href != want {
		t.Errorf("GET %s LogEventListLink = %v, want href %q", created.Href, reread.LogEventListLink, want)
	}
	// The wire form, not a round trip: LogEventListLink is an ELEMENT carrying
	// href as an ATTRIBUTE (sep.xsd ListLink extends Link). encoding/xml would
	// read either spelling back into the same field, so only the bytes can tell
	// them apart.
	if !strings.Contains(string(body), `<LogEventListLink href="`+want+`">`) {
		t.Errorf("LogEventListLink must reach the wire as an element with an href attribute\nbody: %s", body)
	}
	if strings.Contains(string(body), `LogEventListLink="`) {
		t.Errorf("LogEventListLink reached the wire as an attribute\nbody: %s", body)
	}

	// And the link RESOLVES: advertising a list nothing serves is the defect
	// class this card is closing, one function set over.
	if status, _ := getBytes(t, srv, want); status != http.StatusOK {
		t.Errorf("GET %s status = %d, want 200: an advertised link must resolve", want, status)
	}
}

// TestEndDevice_LogEventListLinkIsAbsentWhenTheFunctionSetIsNot is the other
// direction, and it is the one 2018 section 4.4 p.19 states outright: "If a
// function set is not implemented, Link elements to resources in that function
// set SHALL NOT be included."
//
// It also pins the coupling that makes the pair unable to drift: the link and
// the routes are enabled by ONE decision, Stores.LogEvents being non-nil, so a
// server that does not serve the function set cannot advertise it.
func TestEndDevice_LogEventListLinkIsAbsentWhenTheFunctionSetIsNot(t *testing.T) {
	t.Parallel()

	stores := testStores()
	stores.LogEvents = nil
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, p := range patterns {
		if strings.Contains(p, "/lel") {
			t.Fatalf("pattern %q is mounted with a nil LogEvents store; the route and the link must be one decision", p)
		}
	}

	resp, err := http.Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201", resp.StatusCode)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)

	if created.LogEventListLink != nil {
		t.Errorf("LogEventListLink = %q with no LogEvent store wired; section 4.4 p.19 forbids advertising an "+
			"unimplemented function set", created.LogEventListLink.Href)
	}
}

// TestLogEventListLink_IsNotClientForgeable asserts a client cannot install its
// own LogEventListLink through PUT /edev/{id}.
//
// The link says where THIS server serves the list. A client-supplied value
// would be served back to every other reader of that EndDevice, pointing them
// at an address this server does not answer, which is the advertised-but-
// unrouted defect with the client as the author.
func TestLogEventListLink_IsNotClientForgeable(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)

	resp, err := http.Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	var created sep2.EndDevice
	decodeXML(t, resp, &created)

	forged := created
	forged.LogEventListLink = &sep2.ListLink{Href: "http://attacker.example/lel"}
	body, err := xml.Marshal(&forged)
	if err != nil {
		t.Fatalf("marshal EndDevice: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+created.Href, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	put, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", created.Href, err)
	}
	_ = put.Body.Close()
	if put.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s status = %d, want 204", created.Href, put.StatusCode)
	}

	status, served := getBytes(t, srv, created.Href)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", created.Href, status)
	}
	if strings.Contains(string(served), "attacker.example") {
		t.Errorf("a client-supplied LogEventListLink was served back\nbody: %s", served)
	}
	var reread sep2.EndDevice
	if err := xml.Unmarshal(served, &reread); err != nil {
		t.Fatalf("unmarshal EndDevice: %v", err)
	}
	if reread.LogEventListLink == nil || reread.LogEventListLink.Href != created.Href+"/lel" {
		t.Errorf("LogEventListLink = %v, want href %q", reread.LogEventListLink, created.Href+"/lel")
	}
}

// TestLogEventListHref_IsTheOneWriteSideSpelling guards the addressing
// invariant at its source. The link an EndDevice advertises and the route the
// router mounts are the same string only because both derive from this helper.
func TestLogEventListHref_IsTheOneWriteSideSpelling(t *testing.T) {
	t.Parallel()

	if got, want := memory.LogEventListHref("7"), "/edev/7/lel"; got != want {
		t.Errorf("LogEventListHref(%q) = %q, want %q (sep_wadl.xml:1358)", "7", got, want)
	}
}

// statusOf issues a request with no body and returns the status code.
func statusOf(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
