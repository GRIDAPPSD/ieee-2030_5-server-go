package assembly_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The DER instance route.
//
// Every assertion here is on a wire VALUE rather than on a status code alone,
// because the defect this route fixes is a client following an href the server
// itself minted. A test that proved only "does not 404" would pass on a response
// carrying the wrong links, which is the failure the route exists to prevent.

// derInstanceServer builds a fully wired router over stores the caller can seed.
func derInstanceServer(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()

	stores := testStores()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

// seedDER stores a DER exactly as the bridge's seeder writes one: a self href
// plus all four sub-resource links already stamped. This is the shape the
// byte-identical invariant is asserted against.
func seedDER(t *testing.T, stores *assembly.Stores, edevID, derID string) sep2.DER {
	t.Helper()

	base := derHref(edevID, derID)
	d := sep2.DER{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: base}},
		DERAvailabilityLink:  &sep2.Link{Href: base + "/dera"},
		DERCapabilityLink:    &sep2.Link{Href: base + "/dercap"},
		DERSettingsLink:      &sep2.Link{Href: base + "/derg"},
		DERStatusLink:        &sep2.Link{Href: base + "/ders"},
	}
	if err := stores.DERs.Create(context.Background(), edevID, derID, d); err != nil {
		t.Fatalf("seeding DER %s: %v", base, err)
	}
	return d
}

// seedBareDER stores a DER carrying nothing but its own href, which is the state
// serve-time derivation exists to complete.
func seedBareDER(t *testing.T, stores *assembly.Stores, edevID, derID string) {
	t.Helper()

	d := sep2.DER{SubscribableResource: sep2.SubscribableResource{
		Resource: sep2.Resource{Href: derHref(edevID, derID)},
	}}
	if err := stores.DERs.Create(context.Background(), edevID, derID, d); err != nil {
		t.Fatalf("seeding bare DER: %v", err)
	}
}

func derHref(edevID, derID string) string {
	return "/edev/" + edevID + "/der/" + derID
}

// getDER issues a GET and decodes the body, failing on any non-200.
func getDER(t *testing.T, srv *httptest.Server, path string) sep2.DER {
	t.Helper()

	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET %s: status = %d, want 200", path, resp.StatusCode)
	}
	var d sep2.DER
	decodeXML(t, resp, &d)
	return d
}

// assertDERLinks asserts every sub-resource link is present and derived from the
// DER's own href. DERCapabilityLink is singled out in the failure text because
// 2018 section 10.10.5 p.125 makes it mandatory on every DER instance, and
// sep.xsd cannot enforce that: all seven link fields are minOccurs="0", so the
// schema gate has nothing to check and only a test can hold the line.
func assertDERLinks(t *testing.T, d sep2.DER, base string) {
	t.Helper()

	if d.DERCapabilityLink == nil {
		t.Fatalf("DERCapabilityLink is absent; 2018 section 10.10.5 requires every DER instance to link to a DERCapability")
	}

	links := []struct {
		field string
		got   *sep2.Link
	}{
		{"DERAvailabilityLink", d.DERAvailabilityLink},
		{"DERCapabilityLink", d.DERCapabilityLink},
		{"DERSettingsLink", d.DERSettingsLink},
		{"DERStatusLink", d.DERStatusLink},
	}
	suffixes := []string{"/dera", "/dercap", "/derg", "/ders"}

	for i, l := range links {
		if l.got == nil {
			t.Errorf("%s is absent", l.field)
			continue
		}
		if want := base + suffixes[i]; l.got.Href != want {
			t.Errorf("%s.Href = %q, want %q", l.field, l.got.Href, want)
		}
	}
}

// TestDERInstance_GETServesTheResourceWithItsSubResourceLinks covers both halves
// of the fill-absent rule against the wire: a seeded DER keeps the links it was
// stored with, and a DER stored without them is served with them derived.
func TestDERInstance_GETServesTheResourceWithItsSubResourceLinks(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")
	seedBareDER(t, stores, "7", "4")

	t.Run("a seeded DER is served with the links it was stored with", func(t *testing.T) {
		path := derHref("7", "3")
		d := getDER(t, srv, path)
		if d.Href != path {
			t.Errorf("Href = %q, want %q", d.Href, path)
		}
		assertDERLinks(t, d, path)
	})

	t.Run("a DER stored without links is served with them derived", func(t *testing.T) {
		path := derHref("7", "4")
		d := getDER(t, srv, path)
		if d.Href != path {
			t.Errorf("Href = %q, want %q", d.Href, path)
		}
		assertDERLinks(t, d, path)
	})

	t.Run("no link is emitted for a function set this server does not serve", func(t *testing.T) {
		// AssociatedDERProgramList is not mounted, and 2018 section 4.4 p.19
		// says a link to an unimplemented function set SHALL
		// NOT be included. Absence here is the conformant answer, not a gap.
		d := getDER(t, srv, derHref("7", "4"))
		if d.AssociatedDERProgramListLink != nil {
			t.Errorf("AssociatedDERProgramListLink = %q, want absent: no route serves it (2018 section 4.4)",
				d.AssociatedDERProgramListLink.Href)
		}
	})
}

// TestDERInstance_UnknownIDIsACleanNotFound pins the 404 and, more importantly,
// that nothing resembling a DER comes back with it.
//
// The synthesized-200 behaviour the singleton handlers use would be materially
// worse here: a synthesized DER carries sub-resource links, the EPRI reference
// client follows them and PUTs DERSettings into them, the singleton handler
// upserts on PUT, and a GET would have created store entries under an id nobody
// provisioned.
func TestDERInstance_UnknownIDIsACleanNotFound(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")

	resp, err := http.Get(srv.URL + derHref("7", "nosuch"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "<DER") {
		t.Errorf("a 404 body must not carry a DER document, got: %s", body)
	}
}

// TestDERInstance_ScopeBindsTheResourceToTheDeviceInThePath asserts a DER is
// reachable only under the EndDevice it was stored beneath.
//
// This is store scoping, NOT caller ownership: nothing in core checks that the
// caller is the device named by {id}. The two are different properties and this
// test proves only the former, which is why it does not claim otherwise.
func TestDERInstance_ScopeBindsTheResourceToTheDeviceInThePath(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "deviceB", "9")

	// The same derId, asked for under a different device.
	resp, err := http.Get(srv.URL + derHref("deviceA", "9"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: device A must not reach device B's DER", resp.StatusCode)
	}

	// And the resource is still there for its own device, so the 404 above is
	// scoping rather than a seeding failure.
	if got := getDER(t, srv, derHref("deviceB", "9")); got.Href != derHref("deviceB", "9") {
		t.Errorf("Href = %q, want %q", got.Href, derHref("deviceB", "9"))
	}
}

// TestDERInstance_UnservedMethodsGet405WithAnAccurateAllow covers section 4.3 c)
// 4): a method in mode E requires an explicit 400 or 405, which an unmounted
// path cannot give because it 404s. The Allow header is asserted exactly, since
// an Allow that overstates what is served is barely better than the 404.
func TestDERInstance_UnservedMethodsGet405WithAnAccurateAllow(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")
	path := srv.URL + derHref("7", "3")

	// POST is mode E (sep_wadl.xml:4122). DELETE is mode O and not yet
	// implemented, so until it lands it answers the same way.
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, path, strings.NewReader(""))
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
			if got := resp.Header.Get("Allow"); got != "GET, HEAD, PUT" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD, PUT")
			}
		})
	}
}

// TestDERInstance_HEADIsServedByTheGETPattern pins a property inherited from a
// stdlib documentation paragraph: "A pattern with the method GET matches both
// GET and HEAD requests". HEAD is Mandatory on all nine DER-scoped resources, so
// if a future refactor moves this router off http.ServeMux, this test fails
// loudly instead of nine Mandatory methods disappearing quietly.
func TestDERInstance_HEADIsServedByTheGETPattern(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")

	req, err := http.NewRequest(http.MethodHead, srv.URL+derHref("7", "3"), nil)
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

// TestDERInstance_PUTStoresTheServersOwnHrefAndDropsUnservedLinks is the write
// path's data-invariant assertion. A client PUTs a DER claiming a foreign href
// and carrying a link to a function set this server does not serve; the
// subsequent GET must show that neither reached the store.
func TestDERInstance_PUTStoresTheServersOwnHrefAndDropsUnservedLinks(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")
	path := derHref("7", "3")

	putBody := `<DER xmlns="urn:ieee:std:2030.5:ns" href="/edev/99/der/99">` +
		`<AssociatedDERProgramListLink href="/edev/7/der/3/derp"/>` +
		`</DER>`
	req, err := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader(putBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", resp.StatusCode)
	}

	got := getDER(t, srv, path)
	if got.Href != path {
		t.Errorf("Href = %q, want %q: the URI a resource was written to is the server's fact, not the client's", got.Href, path)
	}
	if got.AssociatedDERProgramListLink != nil {
		t.Errorf("AssociatedDERProgramListLink = %q, want absent: a client must not inject a link to a function set we do not serve (2018 section 4.4)",
			got.AssociatedDERProgramListLink.Href)
	}
	assertDERLinks(t, got, path)
}

// TestDERInstance_PUTCreatesAtTheIDThePathNames asserts PUT upserts without
// inventing an id: the resource lands at the derId in the request path and
// nowhere else.
func TestDERInstance_PUTCreatesAtTheIDThePathNames(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")

	path := derHref("7", "8")
	req, err := http.NewRequest(http.MethodPut, srv.URL+path,
		strings.NewReader(`<DER xmlns="urn:ieee:std:2030.5:ns"/>`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", resp.StatusCode)
	}

	stored, err := stores.DERs.Get(context.Background(), "7", "8")
	if err != nil {
		t.Fatalf("the PUT did not store a DER at derId 8: %v", err)
	}
	if stored.Href != path {
		t.Errorf("stored Href = %q, want %q", stored.Href, path)
	}
	if stored.DERCapabilityLink == nil || stored.DERCapabilityLink.Href != path+"/dercap" {
		t.Errorf("stored DERCapabilityLink = %+v, want href %q", stored.DERCapabilityLink, path+"/dercap")
	}
	// The DER that was already there is untouched.
	if other, err := stores.DERs.Get(context.Background(), "7", "3"); err != nil {
		t.Errorf("the pre-existing DER 3 disappeared: %v", err)
	} else if other.Href != derHref("7", "3") {
		t.Errorf("DER 3 Href = %q, want %q", other.Href, derHref("7", "3"))
	}
}

// TestDERInstance_MultipleDERsUnderOneEndDeviceStayIndependent is the multi-DER
// invariant. Nothing in this route may reintroduce a single-DER assumption, so
// two DERs under one EndDevice must serve distinct documents with distinct
// sub-resource hrefs, and the sub-resource stores keyed id + "/" + derId must
// stay separate.
func TestDERInstance_MultipleDERsUnderOneEndDeviceStayIndependent(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seedBareDER(t, stores, "7", "1")
	seedBareDER(t, stores, "7", "2")

	first := getDER(t, srv, derHref("7", "1"))
	second := getDER(t, srv, derHref("7", "2"))

	assertDERLinks(t, first, derHref("7", "1"))
	assertDERLinks(t, second, derHref("7", "2"))
	if first.DERCapabilityLink.Href == second.DERCapabilityLink.Href {
		t.Fatalf("both DERs advertise the same DERCapabilityLink %q", first.DERCapabilityLink.Href)
	}

	// The sub-resource routes under each DER address different store entries:
	// their scope key is id + "/" + derId, which this change must not disturb.
	ratings := []struct {
		derID  string
		rating int16
	}{
		{"1", 1000},
		{"2", 2000},
	}

	for _, tc := range ratings {
		put := derHref("7", tc.derID) + "/dercap"
		body := fmt.Sprintf(
			`<DERCapability xmlns="urn:ieee:std:2030.5:ns"><rtgMaxW><multiplier>0</multiplier><value>%d</value></rtgMaxW></DERCapability>`,
			tc.rating)
		req, err := http.NewRequest(http.MethodPut, srv.URL+put, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", put, err)
		}
		_ = resp.Body.Close()
	}

	for _, tc := range ratings {
		resp, err := http.Get(srv.URL + derHref("7", tc.derID) + "/dercap")
		if err != nil {
			t.Fatalf("GET dercap: %v", err)
		}
		var capability sep2.DERCapability
		decodeXML(t, resp, &capability)
		if capability.RTGMaxW == nil {
			t.Fatalf("DER %s: RTGMaxW is nil", tc.derID)
		}
		if got := capability.RTGMaxW.Value; got != tc.rating {
			t.Errorf("DER %s: RTGMaxW.Value = %d, want %d", tc.derID, got, tc.rating)
		}
	}
}

// TestDERList_SeededOutputIsByteIdenticalToTheUnfilledBuild is the
// byte-identical invariant for the single-DER case.
//
// Seeding already stamps all four links, so under the phase-1 policy the fill is
// a no-op and the DERList bytes must not move. This is asserted rather than
// argued: the two builders are run over the same store result and their
// marshalled bytes compared, and the served response is compared against the
// bytes the policy-free builder produces, which is what the wire carried before
// this change.
func TestDERList_SeededOutputIsByteIdenticalToTheUnfilledBuild(t *testing.T) {
	t.Parallel()

	srv, stores := derInstanceServer(t)
	seeded := seedDER(t, stores, "1", "1")

	const listPath = "/edev/1/der"
	policy := coreder.DERLinkPolicy{Capability: true, Settings: true, Status: true, Availability: true}

	// Each builder gets its own page: the members are completed in place, so
	// sharing one would let the first call decide the second call's input.
	page := func() store.ListResult[sep2.DER] {
		return store.ListResult[sep2.DER]{All: 1, Results: 1, Items: []sep2.DER{seeded.Copy()}}
	}

	enc := encoding.NewXMLEncoder()
	unfilled, err := enc.Marshal(coreder.BuildDERList(listPath, page(), 900))
	if err != nil {
		t.Fatalf("marshal unfilled list: %v", err)
	}
	filled, err := enc.Marshal(coreder.DERListBuilder(policy)(listPath, page(), 900))
	if err != nil {
		t.Fatalf("marshal filled list: %v", err)
	}
	if !bytes.Equal(unfilled, filled) {
		t.Errorf("link derivation changed the DERList bytes for a fully seeded DER.\n unfilled: %s\n   filled: %s", unfilled, filled)
	}

	resp, err := http.Get(srv.URL + listPath)
	if err != nil {
		t.Fatalf("GET %s: %v", listPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	served, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(served, unfilled) {
		t.Errorf("served DERList differs from the bytes the unfilled builder produces.\n served: %s\n golden: %s", served, unfilled)
	}
}

// TestDERList_MemberWithoutAnHrefIsLeftAlone asserts the fill refuses rather
// than guesses. A member with no href has no honest base: the list's own path
// cannot name the member's id, so deriving from it would mint hrefs pointing at
// a DER that is not the one being described.
func TestDERList_MemberWithoutAnHrefIsLeftAlone(t *testing.T) {
	t.Parallel()

	policy := coreder.DERLinkPolicy{Capability: true, Settings: true, Status: true, Availability: true}
	result := store.ListResult[sep2.DER]{All: 1, Results: 1, Items: []sep2.DER{{}}}

	list := coreder.DERListBuilder(policy)("/edev/1/der", result, 900)
	if len(list.DER) != 1 {
		t.Fatalf("len(DER) = %d, want 1", len(list.DER))
	}
	if got := list.DER[0]; got.DERCapabilityLink != nil {
		t.Errorf("DERCapabilityLink = %q, want absent: no href means no honest base to derive from", got.DERCapabilityLink.Href)
	}
}
