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
)

// The TextMessage instance route.
//
// POST /msg/{msgId}/tm hands back a Location at /msg/{msgId}/tm/{tmId}
// (handlers/messaging.HandlePostTextMessage) and nothing served it. The WADL
// declares the resource at sep_wadl.xml:2849 with GET and HEAD mode M, PUT and
// POST mode E, and DELETE mode D, so only the two Mandatory reads are mounted
// here.
//
// PATH SHAPE, recorded rather than changed: the WADL samplePath is
// /msg/{id1}/txt/{id2} and /msg/{id1}/txt for the list, while this server
// serves both under /tm. Hrefs are server-assigned, so the projection is not
// wrong on its own, but it is the same shape as the LogEvent /log versus /lel
// divergence fixed elsewhere. Renaming would move the mounted list
// and POST routes too, which is a separate decision from closing the dead link,
// so it is reported as a finding and the instance is mounted where the list
// already lives.

// tmServer builds a fully wired router and returns it with its stores.
func tmServer(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()

	stores := testStores()
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

// postTextMessage posts a message under msgID and returns the minted Location.
func postTextMessage(t *testing.T, srv *httptest.Server, msgID, text string) string {
	t.Helper()

	body, err := xml.Marshal(&sep2.TextMessage{
		Originator: "utility-ops",
		Priority:   1,
		TextBody:   text,
	})
	if err != nil {
		t.Fatalf("marshal TextMessage: %v", err)
	}

	resp, err := http.Post(srv.URL+"/msg/"+msgID+"/tm", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /msg/%s/tm: %v", msgID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /msg/%s/tm status = %d, want 201", msgID, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /msg/{msgId}/tm returned no Location; there is no href to follow")
	}
	return loc
}

// TestTextMessage_LocationHeaderResolves follows the server's own Location and
// asserts the served field values, not merely a 200. A zero-valued TextMessage
// would route correctly and still tell every polling device that the message
// body is empty.
func TestTextMessage_LocationHeaderResolves(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	loc := postTextMessage(t, srv, "m1", "planned outage 0200 to 0400")

	resp, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: the server minted this href itself", loc, resp.StatusCode)
	}

	var got sep2.TextMessage
	decodeXML(t, resp, &got)

	if got.Href != loc {
		t.Errorf("Href = %q, want %q", got.Href, loc)
	}
	if got.TextBody != "planned outage 0200 to 0400" {
		t.Errorf("textMessage = %q, want %q", got.TextBody, "planned outage 0200 to 0400")
	}
	if got.Originator != "utility-ops" {
		t.Errorf("originator = %q, want %q", got.Originator, "utility-ops")
	}
	if got.Priority != 1 {
		t.Errorf("priority = %d, want 1", got.Priority)
	}
	if got.CreationTime == 0 {
		t.Error("creationTime = 0: a served event with no creation instant cannot be ordered against another")
	}
}

// TestTextMessage_ScopeBindsToTheMessagingProgramInThePath is the assertion
// that the mount derives its scope from {msgId} and not from a wildcard the
// pattern does not declare.
//
// It matters more than the usual scoping test: r.PathValue on an undeclared
// wildcard returns "", which would key every lookup under the empty parent. The
// resource would then be unreachable under its own program and reachable under
// every other one, and no status-code-only test would tell the two apart.
func TestTextMessage_ScopeBindsToTheMessagingProgramInThePath(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	loc := postTextMessage(t, srv, "m1", "for program one only")

	foreign := strings.Replace(loc, "/msg/m1/", "/msg/m2/", 1)
	if foreign == loc {
		t.Fatalf("test setup: could not rewrite %q to a foreign program", loc)
	}

	resp, err := http.Get(srv.URL + foreign)
	if err != nil {
		t.Fatalf("GET %s: %v", foreign, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s status = %d, want 404: a message must not be reachable under a program it was not posted to", foreign, resp.StatusCode)
	}

	own, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, own.StatusCode)
	}
}

// TestTextMessage_UnknownIDIsACleanNotFound: a miss is a 404 and never a
// synthesized zero-valued resource.
//
// The present message is fetched first, in this same test, because a 404-only
// assertion is vacuous while the route is unmounted: an unrouted path 404s too,
// so the test would have passed against the defect this card fixes.
func TestTextMessage_UnknownIDIsACleanNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	loc := postTextMessage(t, srv, "m1", "a real message")

	present, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	present.Body.Close()
	if present.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: without a served sibling the 404 below proves nothing",
			loc, present.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/msg/m1/tm/nosuch")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "<TextMessage") {
		t.Errorf("a 404 body must not carry a TextMessage document, got: %s", body)
	}
}

// TestTextMessage_UnservedMethodsGet405WithAnAccurateAllow: PUT and POST are
// mode E and DELETE is mode D at sep_wadl.xml:2849, so each needs an explicit
// 405 rather than the 404 an unmounted path gives.
//
// As with the FlowReservation mounts, the 405 comes from http.ServeMux because
// only a GET pattern is registered; Allow is therefore derived from the
// registered method set. Neutralization check: register a PUT pattern for this
// shape and this test fails with Allow "GET, HEAD, PUT".
func TestTextMessage_UnservedMethodsGet405WithAnAccurateAllow(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	loc := postTextMessage(t, srv, "m1", "a real message")

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, srv.URL+loc, strings.NewReader(""))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
		})
	}
}

// TestTextMessage_HEADIsServedByTheGETPattern pins the stdlib property that a
// GET pattern also matches HEAD, which is Mandatory here (sep_wadl.xml:2857).
func TestTextMessage_HEADIsServedByTheGETPattern(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	loc := postTextMessage(t, srv, "m1", "a real message")

	req, err := http.NewRequest(http.MethodHead, srv.URL+loc, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

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
