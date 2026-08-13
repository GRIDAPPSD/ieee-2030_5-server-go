package assembly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// PUT and DELETE on the MirrorUsagePoint instance.
//
// These drive the router BuildProtocolRouter returns, with no wrapper ACL in
// the path, which is the boundary of what is tested here. Reachability
// through the bridge and server-go wrappers is NOT proven here and is not
// proven anywhere: both classify /mup as read-and-create and answer 405
// before the mux, so in those deployments these two Mandatory methods remain
// dark until the wrapper tables are removed upstream. That verdict is
// BLOCKED, not passing, and mounting the routes in core does not change it.

// mupInstanceRouter builds a bare-core router over fresh stores and returns it
// with the stores, so a test can assert what the request actually did to the
// data rather than only what it answered.
func mupInstanceRouter(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()
	stores := testStores()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

// createMirror POSTs a MirrorUsagePoint and returns the path from Location,
// which is how a real client learns the resource's address.
func createMirror(t *testing.T, srv *httptest.Server, mrid string) string {
	t.Helper()
	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>` + mrid + `</mRID>` +
		`<description>created</description></MirrorUsagePoint>`
	resp, err := http.Post(srv.URL+"/mup", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mup: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /mup status = %d, want 201", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /mup returned no Location header")
	}
	return loc
}

func doRequest(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, payload
}

// TestAssembly_MirrorInstancePutAndDeleteAreServedByBareCore walks the sequence
// a conforming client walks: POST /mup, follow the Location with PUT, then
// follow it with DELETE. Both methods are wx:mode="M" (sep_wadl.xml:2303 and
// 2323), so before this card the client's own Location header answered 405 for
// both.
//
// The whole walk runs against ONE router, so the PUT that succeeds is the proof
// that the subsequent 404-free DELETE is being served rather than merely routed.
func TestAssembly_MirrorInstancePutAndDeleteAreServedByBareCore(t *testing.T) {
	t.Parallel()

	srv, stores := mupInstanceRouter(t)
	path := createMirror(t, srv, "WALK_MRID")
	id := strings.TrimPrefix(path, "/mup/")

	// PUT the resource the server itself named, with the same mRID, per rule
	// (a)(4): the new data is written over the existing record.
	putBody := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>WALK_MRID</mRID>` +
		`<description>updated through the router</description></MirrorUsagePoint>`
	putResp, putPayload := doRequest(t, http.MethodPut, srv.URL+path, putBody)
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s: status = %d, want 204; body = %s", path, putResp.StatusCode, putPayload)
	}
	if got := putResp.Header.Get("Location"); got != path {
		t.Errorf("PUT %s Location = %q, want %q", path, got, path)
	}

	stored, err := stores.MirrorUsagePoints.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get MirrorUsagePoint %q after PUT: %v", id, err)
	}
	if stored.Description != "updated through the router" {
		t.Errorf("stored Description = %q: the PUT reached the router but did not apply", stored.Description)
	}
	if stored.DeviceLFDI != testLFDI {
		t.Errorf("stored DeviceLFDI = %q, want the certificate identity %q", stored.DeviceLFDI, testLFDI)
	}

	// DELETE serves the stripped record back per sep_wadl.xml:2325 with rule (c).
	delResp, delPayload := doRequest(t, http.MethodDelete, srv.URL+path, "")
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE %s: status = %d, want 200; body = %s", path, delResp.StatusCode, delPayload)
	}
	served := string(delPayload)
	if !strings.Contains(served, "<MirrorUsagePoint") || !strings.Contains(served, "<mRID>WALK_MRID</mRID>") {
		t.Errorf("DELETE did not serve the deleted MirrorUsagePoint; body = %s", served)
	}
	if strings.Contains(served, "MirrorMeterReading") {
		t.Errorf("DELETE served a MirrorMeterReading child, violating rule (c); body = %s", served)
	}

	// It is gone from the store, not merely reported as gone.
	if _, err := stores.MirrorUsagePoints.Get(context.Background(), id); err == nil {
		t.Errorf("MirrorUsagePoint %q still present after DELETE through the router", id)
	}
}

// TestAssembly_MirrorInstanceDeleteCascadesThroughTheRouter proves the cascade
// is wired, not merely implemented. The readings store is a separate argument to
// the handler, so a mount that forgot it would still compile, still answer 200,
// and still leave every reading behind.
//
// Two mirrors are created and only one is deleted, because a cascade that
// emptied the whole readings store would be indistinguishable from a correct one
// against a single parent.
func TestAssembly_MirrorInstanceDeleteCascadesThroughTheRouter(t *testing.T) {
	t.Parallel()

	srv, stores := mupInstanceRouter(t)
	pathA := createMirror(t, srv, "CASCADE_A")
	pathB := createMirror(t, srv, "CASCADE_B")
	idA := strings.TrimPrefix(pathA, "/mup/")
	idB := strings.TrimPrefix(pathB, "/mup/")

	reading := `<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>R1</mRID>` +
		`<Reading><value>42</value></Reading></MirrorMeterReading>`
	for _, p := range []string{pathA, pathA + "/mr", pathB} {
		resp, payload := doRequest(t, http.MethodPost, srv.URL+p, reading)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: status = %d, want 201; body = %s", p, resp.StatusCode, payload)
		}
	}

	before, err := stores.MirrorMeterReadings.Count(context.Background(), idA)
	if err != nil {
		t.Fatalf("count readings under %q: %v", idA, err)
	}
	if before != 2 {
		t.Fatalf("test setup: reading count under %q = %d, want 2", idA, before)
	}

	resp, payload := doRequest(t, http.MethodDelete, srv.URL+pathA, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE %s: status = %d, want 200; body = %s", pathA, resp.StatusCode, payload)
	}

	// HasParent before Count: Count materialises the bucket it is asked about.
	present, err := stores.MirrorMeterReadings.HasParent(context.Background(), idA)
	if err != nil {
		t.Fatalf("HasParent %q: %v", idA, err)
	}
	if present {
		t.Errorf("readings collection under deleted parent %q survived the DELETE through the router", idA)
	}
	kept, err := stores.MirrorMeterReadings.Count(context.Background(), idB)
	if err != nil {
		t.Fatalf("count readings under %q: %v", idB, err)
	}
	if kept != 1 {
		t.Errorf("reading count under the untouched mirror %q = %d, want 1: the cascade was not scoped", idB, kept)
	}
}

// TestAssembly_MirrorInstanceAllowHeaderNamesExactlyTheServedMethods asserts the
// 405 Allow header on /mup/{id}, which section 4.3 c) 4) requires and which a
// client uses to decide what it may do next.
//
// The assertion runs both ways, and the second direction is the one that matters
// after this card. Every method the header NAMES is then issued against the same
// path, and must not answer 405: a header that advertises a method the server
// does not serve sends a conforming client to a door that is not there, which is
// the same defect class as an unrouted mintable href, one layer down. Before
// PUT and DELETE were mounted, Go's ServeMux derived this header from the
// registered patterns and correctly omitted them; the risk now is the reverse,
// a pattern registered against a handler that refuses the method it was
// registered for.
func TestAssembly_MirrorInstanceAllowHeaderNamesExactlyTheServedMethods(t *testing.T) {
	t.Parallel()

	srv, _ := mupInstanceRouter(t)
	path := createMirror(t, srv, "ALLOW_MRID")

	// PATCH is not declared for this resource in any edition of the WADL, so it
	// is the probe that draws the Allow header out of the mux.
	resp, payload := doRequest(t, http.MethodPatch, srv.URL+path, "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH %s: status = %d, want 405; body = %s", path, resp.StatusCode, payload)
	}

	allow := resp.Header.Get("Allow")
	if allow == "" {
		t.Fatal("405 carries no Allow header; RFC 9110 requires one and IEEE 2030.5 section 4.3 c) 4) relies on it")
	}
	got := strings.Split(strings.ReplaceAll(allow, " ", ""), ",")
	sort.Strings(got)

	want := []string{"DELETE", "GET", "HEAD", "POST", "PUT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Allow = %q, want exactly %v.\n"+
			"PUT and DELETE are wx:mode=\"M\" (sep_wadl.xml:2303, 2323); GET, HEAD and POST were already served.",
			allow, want)
	}

	// Every advertised method is actually served. HEAD and GET are read-only
	// probes; PUT is given a body that its handler accepts; DELETE is issued
	// last because it removes the resource the others need.
	putBody := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>ALLOW_MRID</mRID></MirrorUsagePoint>`
	readingBody := `<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>R1</mRID>` +
		`<Reading><value>1</value></Reading></MirrorMeterReading>`
	for _, c := range []struct {
		method string
		body   string
	}{
		{http.MethodGet, ""},
		{http.MethodHead, ""},
		{http.MethodPost, readingBody},
		{http.MethodPut, putBody},
		{http.MethodDelete, ""},
	} {
		r, p := doRequest(t, c.method, srv.URL+path, c.body)
		if r.StatusCode == http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered 405 while Allow advertises it (%q); body = %s",
				c.method, path, allow, p)
		}
	}
}

// TestAssembly_MirrorInstancePutRejectsARenameThroughTheRouter is the wiring
// half of the rename rejection. The handler suite proves the rule; this proves
// the route reaches the handler that carries it, and, as there, the assertion is
// on the store rather than on the status code.
func TestAssembly_MirrorInstancePutRejectsARenameThroughTheRouter(t *testing.T) {
	t.Parallel()

	srv, stores := mupInstanceRouter(t)
	path := createMirror(t, srv, "KEEP_MRID")
	id := strings.TrimPrefix(path, "/mup/")

	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>OTHER_MRID</mRID>` +
		`<description>renamed</description></MirrorUsagePoint>`
	resp, payload := doRequest(t, http.MethodPut, srv.URL+path, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT %s with a different mRID: status = %d, want 409; body = %s", path, resp.StatusCode, payload)
	}

	count, err := stores.MirrorUsagePoints.Count(context.Background())
	if err != nil {
		t.Fatalf("count MirrorUsagePoints: %v", err)
	}
	if count != 1 {
		t.Errorf("MirrorUsagePoint count = %d, want 1: the rejected PUT created a second record", count)
	}
	stored, err := stores.MirrorUsagePoints.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("the original record is gone after a rejected PUT: %v", err)
	}
	if stored.MRID != "KEEP_MRID" || stored.Description != "created" {
		t.Errorf("record altered by a rejected PUT: MRID = %q, Description = %q", stored.MRID, stored.Description)
	}
}
