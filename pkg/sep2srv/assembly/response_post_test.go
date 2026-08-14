package assembly_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The Mandatory Response POST, end to end.
//
// Two defects sat on this one path and each hid the other. We emitted
// DERControls carrying neither replyTo nor responseRequired, and the EPRI
// reference client gates device_response entirely on responseRequired
// (schedule.c:150-151), so it correctly posted nothing and the second defect
// stayed invisible: had it posted, HandlePostResponse would have decoded into
// sep2.Response, whose pinned XMLName rejects a <DERControlResponse> root, and
// answered a conforming client with 400.
//
// The base standard makes the first conditional ("If a response is desired to
// an event, then the event SHALL provide, in the replyTo field, a URI"), so a
// server that sets neither field is base-conformant. The certification profile
// is not conditional: CSIP CTP CORE-022 setup requires replyTo together with
// responseRequired="07", and that setup is repeated verbatim in BASIC-004
// through BASIC-015, all Server-required, each of which additionally asserts
// the server answers a POSTed Response with 201 Created and a Location header.
//
// These tests drive the real router rather than a handler in isolation,
// because the defect is only visible as a round trip: the href a client reads
// off an event has to be the href the POST route is mounted at, and the
// Location the POST returns has to resolve.

// readBody returns the response body and closes it.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

// TestServedDERControlRequestsAResponse asserts that a DERControl stored with
// neither field is SERVED carrying both, on both routes that emit one.
//
// It asserts the raw bytes, not the decoded struct, because responseRequired
// is a HexBinary8: a client reads "07", and a server that emitted "7" or a
// decimal 7 would decode back to the same Go value here while being wrong on
// the wire.
//
// Both fields are ATTRIBUTES (sep.xsd:5435, sep.xsd:5440). The stamping
// behaviour this test covers is unchanged by the element-versus-attribute
// fix; only the wire form the assertions look for moved, because the
// element form they previously searched for is the defect that fix closed.
// Asserting bytes is what let this test be updated meaningfully at all: a
// round-trip assertion would have stayed green across the fix and told us
// nothing.
func TestServedDERControlRequestsAResponse(t *testing.T) {
	t.Parallel()

	stores := testStores()
	want := seedDERControl(t, stores, testLFDI, "reply-0", 4100, 1785429793)
	srv := derControlRouter(t, stores)

	wantReplyTo := ` replyTo="` + coreresponse.ListHref(coreresponse.DefaultSetID) + `"`
	const wantRespReq = ` responseRequired="07"`

	for _, href := range []string{
		want.Href,
		"/edev/" + testLFDI + "/fsa/1/derp/1/derc",
	} {
		resp, err := http.Get(srv.URL + href)
		if err != nil {
			t.Fatalf("GET %s: %v", href, err)
		}
		body := string(readBody(t, resp))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d (body: %s)", href, resp.StatusCode, body)
		}
		if !strings.Contains(body, wantReplyTo) {
			t.Errorf("GET %s served a DERControl without %s, so a conforming client has nowhere to post its response:\n%s",
				href, wantReplyTo, body)
		}
		if !strings.Contains(body, wantRespReq) {
			t.Errorf("GET %s served a DERControl without %s, so the EPRI client's responseRequired gate never opens:\n%s",
				href, wantRespReq, body)
		}
		// The element form is what made the client abort the whole
		// DERControlList parse, taking the dera/dercap/derg PUTs and the
		// telemetry up-leg down with it.
		for _, forbidden := range []string{"<replyTo>", "<responseRequired>"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("GET %s served %s as a child element; the schema declares it as an attribute and a conforming client fails the parse:\n%s",
					href, forbidden, body)
			}
		}
	}
}

// TestServedDERControlKeepsAnExplicitResponsePolicy asserts the stamp is a
// DEFAULT and not an override. A consumer that stored its own replyTo, or that
// stored responseRequired="00" to say deliberately that it wants no response,
// keeps what it stored. responseRequired is a pointer precisely so "absent" and
// "explicitly none" are distinguishable; collapsing them would make the
// server's own policy unexpressible.
func TestServedDERControlKeepsAnExplicitResponsePolicy(t *testing.T) {
	t.Parallel()

	stores := testStores()
	ctrl := seedDERControl(t, stores, testLFDI, "reply-1", 4200, 1785429800)
	ctrl.ReplyTo = "/rsps/operator-set/rsp"
	none := sep2.HexBinary8(0)
	ctrl.ResponseRequired = &none
	if err := stores.DERControls.Update(
		context.Background(),
		derControlScopeKey(testLFDI, "1", "1"),
		"reply-1",
		ctrl,
	); err != nil {
		t.Fatalf("update seeded DERControl: %v", err)
	}

	srv := derControlRouter(t, stores)
	resp, err := http.Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", ctrl.Href, err)
	}
	body := string(readBody(t, resp))

	if !strings.Contains(body, ` replyTo="/rsps/operator-set/rsp"`) {
		t.Errorf("server overwrote a consumer-set replyTo:\n%s", body)
	}
	// The stored 0x00 is a non-nil pointer to zero. omitempty on an attribute
	// drops only the field type's own zero value, which for a pointer is nil,
	// so "explicitly none" still reaches the wire while "unset" still does
	// not. That distinction is the whole point of the pointer and is the
	// semantic this assertion guards.
	if !strings.Contains(body, ` responseRequired="00"`) {
		t.Errorf("server overwrote an explicit responseRequired=00 (deliberately no response):\n%s", body)
	}
}

// TestResponseSetIsSeededAndServed asserts the ResponseSet the replyTo points
// into actually exists and resolves. A replyTo naming a set that is neither
// listed nor served would be the advertised-but-unrouted defect one level up:
// the client would find the POST route but could never read back what the
// server recorded.
func TestResponseSetIsSeededAndServed(t *testing.T) {
	t.Parallel()

	srv := derControlRouter(t, testStores())

	listResp, err := http.Get(srv.URL + "/rsps")
	if err != nil {
		t.Fatalf("GET /rsps: %v", err)
	}
	var list sep2.ResponseSetList
	decodeXML(t, listResp, &list)
	if list.All != 1 || len(list.ResponseSet) != 1 {
		t.Fatalf("GET /rsps served all=%d with %d member(s), want the default set seeded",
			list.All, len(list.ResponseSet))
	}

	wantSetHref := coreresponse.SetHref(coreresponse.DefaultSetID)
	if list.ResponseSet[0].Href != wantSetHref {
		t.Errorf("seeded ResponseSet Href = %q, want %q", list.ResponseSet[0].Href, wantSetHref)
	}

	memberResp, err := http.Get(srv.URL + wantSetHref)
	if err != nil {
		t.Fatalf("GET %s: %v", wantSetHref, err)
	}
	var set sep2.ResponseSet
	decodeXML(t, memberResp, &set)
	if set.Href != wantSetHref {
		t.Errorf("served ResponseSet Href = %q, want %q", set.Href, wantSetHref)
	}
	if set.MRID == "" {
		t.Error("served ResponseSet has an empty mRID; ResponseSet extends IdentifiedObject")
	}
	if set.ResponseListLink == nil {
		t.Fatal("served ResponseSet has no ResponseListLink, so a client cannot find the list to post into")
	}
	wantListHref := coreresponse.ListHref(coreresponse.DefaultSetID)
	if set.ResponseListLink.Href != wantListHref {
		t.Errorf("ResponseListLink.Href = %q, want %q", set.ResponseListLink.Href, wantListHref)
	}
}

// TestPostDERControlResponseIsStoredAndRetrievable is the round trip the card
// is about: read the replyTo off a served DERControl, POST the
// DERControlResponse a conforming client would send there, and follow the
// Location back.
//
// It asserts the STORED field values rather than only the status code. A 201
// over a response whose subject was dropped would be worse than the 400 it
// replaces: the server would report success while holding a record it cannot
// match to any event, and subject is exactly how a response is matched to the
// event it answers.
func TestPostDERControlResponseIsStoredAndRetrievable(t *testing.T) {
	t.Parallel()

	stores := testStores()
	ctrl := seedDERControl(t, stores, testLFDI, "reply-2", 4300, 1785429810)
	srv := derControlRouter(t, stores)

	// Read replyTo the way a client does: off the served event.
	ctrlResp, err := http.Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", ctrl.Href, err)
	}
	var served sep2.DERControl
	decodeXML(t, ctrlResp, &served)
	if served.ReplyTo == "" {
		t.Fatal("served DERControl carries no replyTo; the POST has no advertised target")
	}

	const wantLFDI = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	status := sep2.ResponseStatusEventReceived
	body, err := xml.Marshal(&sep2.DERControlResponse{
		Response: sep2.Response{
			CreatedDateTime: 1785429815,
			EndDeviceLFDI:   wantLFDI,
			Status:          &status,
			Subject:         ctrl.MRID,
		},
	})
	if err != nil {
		t.Fatalf("marshal DERControlResponse: %v", err)
	}
	if !bytes.Contains(body, []byte("<DERControlResponse")) {
		t.Fatalf("test fixture is not a DERControlResponse document: %s", body)
	}

	postResp, err := http.Post(srv.URL+served.ReplyTo, "application/sep+xml", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", served.ReplyTo, err)
	}
	postBody := string(readBody(t, postResp))
	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: want 201 Created, got %d (body: %s)", served.ReplyTo, postResp.StatusCode, postBody)
	}
	location := postResp.Header.Get("Location")
	if location == "" {
		t.Fatal("POST returned no Location header; CTP asserts one on every event response")
	}

	// The stored record has to carry the fields that make it usable.
	stored, err := stores.Responses.List(context.Background(), coreresponse.DefaultSetID, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(stored.Items) != 1 {
		t.Fatalf("stored response count = %d, want 1", len(stored.Items))
	}
	got := stored.Items[0]
	if got.Subject != ctrl.MRID {
		t.Errorf("stored Subject = %q, want the event mRID %q; a response that cannot be matched to its event is not a response",
			got.Subject, ctrl.MRID)
	}
	if got.EndDeviceLFDI != wantLFDI {
		t.Errorf("stored EndDeviceLFDI = %q, want %q", got.EndDeviceLFDI, wantLFDI)
	}
	if got.Status == nil || *got.Status != sep2.ResponseStatusEventReceived {
		t.Errorf("stored Status = %v, want %d (Event Received)", got.Status, sep2.ResponseStatusEventReceived)
	}
	if got.Href != location {
		t.Errorf("stored Href = %q but Location was %q; the two must agree", got.Href, location)
	}

	// The Location the server minted has to resolve.
	locResp, err := http.Get(srv.URL + location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	locBody := readBody(t, locResp)
	if locResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s (the Location we minted): want 200, got %d (body: %s)", location, locResp.StatusCode, locBody)
	}
	var fetched sep2.Response
	if err := xml.Unmarshal(locBody, &fetched); err != nil {
		t.Fatalf("decode %s: %v\nbody: %s", location, err, locBody)
	}
	if fetched.Subject != ctrl.MRID {
		t.Errorf("Response at %s has Subject %q, want %q", location, fetched.Subject, ctrl.MRID)
	}

	// And it has to appear in the list the ResponseSet links to.
	listResp, err := http.Get(srv.URL + coreresponse.ListHref(coreresponse.DefaultSetID))
	if err != nil {
		t.Fatalf("GET response list: %v", err)
	}
	var rspList sep2.ResponseList
	decodeXML(t, listResp, &rspList)
	if len(rspList.Response) != 1 || rspList.Response[0].Subject != ctrl.MRID {
		t.Errorf("ResponseList served %d member(s) %+v, want the posted response", len(rspList.Response), rspList.Response)
	}
}

// TestPostBaseResponseStillAccepted guards the path that already worked. The
// subtype dispatch must not narrow what a <Response> body could do before it.
func TestPostBaseResponseStillAccepted(t *testing.T) {
	t.Parallel()

	stores := testStores()
	srv := derControlRouter(t, stores)

	body, err := xml.Marshal(&sep2.Response{Subject: "0123456789ABCDEF0123456789ABCDEF"})
	if err != nil {
		t.Fatalf("marshal Response: %v", err)
	}
	href := coreresponse.ListHref(coreresponse.DefaultSetID)
	resp, err := http.Post(srv.URL+href, "application/sep+xml", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", href, err)
	}
	got := string(readBody(t, resp))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s with a <Response> body: want 201, got %d (body: %s)", href, resp.StatusCode, got)
	}
}

// TestPostForeignRootStillRejected asserts the pin still bites through the
// handler. Accepting an arbitrary root element would have been the easy repair
// and the wrong one.
func TestPostForeignRootStillRejected(t *testing.T) {
	t.Parallel()

	srv := derControlRouter(t, testStores())
	href := coreresponse.ListHref(coreresponse.DefaultSetID)

	resp, err := http.Post(srv.URL+href, "application/sep+xml",
		strings.NewReader(`<DERSettings xmlns="`+sep2.Namespace+`"><setGradW>1</setGradW></DERSettings>`))
	if err != nil {
		t.Fatalf("POST %s: %v", href, err)
	}
	body := string(readBody(t, resp))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST %s with a <DERSettings> body: want 400, got %d (body: %s)", href, resp.StatusCode, body)
	}
}

// TestDefaultResponseRequiredIsTheCertificationValue pins the constant to the
// value CSIP CTP CORE-022 names in its setup, so a later edit that "tidies" it
// to a different bitmap fails here rather than in a certification lab.
func TestDefaultResponseRequiredIsTheCertificationValue(t *testing.T) {
	t.Parallel()

	if coreder.DefaultResponseRequired != 0x07 {
		t.Errorf("coreder.DefaultResponseRequired = %#x, want 0x07 (CTP CORE-022 setup)",
			uint8(coreder.DefaultResponseRequired))
	}
}
