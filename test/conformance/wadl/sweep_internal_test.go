package wadl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests need no WADL copy and no server under test. They guard the
// machinery the sweep's verdicts rest on, so a broken discriminator surfaces
// as a named failure here rather than as a conformance report that is quietly
// wrong.

// TestMuxNotFoundBodyIsStillTheStdlibMarker is the assumption check that keeps
// the primary unrouted discriminator honest.
//
// The sweep tells "route not mounted" from "route mounted, resource absent" by
// comparing the 404 body against the exact text net/http's ServeMux writes.
// That text is a stdlib implementation detail, not a documented contract, so a
// Go upgrade could change it. If it ever does, every unrouted row silently
// reclassifies as routed_not_found and the sweep under-reports missing routes
// while still passing.
//
// Rather than trusting the constant, this asks a real empty ServeMux what it
// writes today and compares. A Go release that changes the body fails here,
// naming the cause, instead of corrupting a conformance verdict.
func TestMuxNotFoundBodyIsStillTheStdlibMarker(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/definitely-not-mounted")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	r := response{status: resp.StatusCode, body: body}
	if !isMuxNotFound(r) {
		t.Fatalf("the stdlib ServeMux 404 body is now %q, but the unrouted discriminator matches %q; "+
			"every unrouted row would silently reclassify as routed_not_found",
			string(body), muxNotFoundBody)
	}
}

// TestIsMuxNotFoundRejectsHandlerAuthored404 proves the discriminator does not
// fire on a 404 a handler wrote itself. That is the whole distinction: an
// unrouted path is a spec gap, a handler-authored 404 is an empty store.
func TestIsMuxNotFoundRejectsHandlerAuthored404(t *testing.T) {
	tests := []struct {
		name string
		r    response
		want bool
	}{
		{name: "stdlib marker", r: response{status: 404, body: []byte("404 page not found\n")}, want: true},
		{name: "stdlib marker without trailing newline", r: response{status: 404, body: []byte("404 page not found")}, want: true},
		{name: "handler authored", r: response{status: 404, body: []byte("<Error><reasonCode>0</reasonCode></Error>")}, want: false},
		{name: "empty body", r: response{status: 404, body: nil}, want: false},
		{name: "marker text but not a 404", r: response{status: 200, body: []byte("404 page not found\n")}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMuxNotFound(tc.r); got != tc.want {
				t.Errorf("isMuxNotFound() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRouteIndexUsesRealServeMuxSemantics proves the second, independent
// discriminator answers with the same matcher the server uses, including
// method-specific patterns and wildcard segments.
func TestRouteIndexUsesRealServeMuxSemantics(t *testing.T) {
	ri := newRouteIndex([]string{
		"GET /dcap",
		"GET /edev",
		"POST /edev",
		"GET /edev/{id}",
	})

	tests := []struct {
		method, path string
		want         bool
	}{
		{method: "GET", path: "/dcap", want: true},
		{method: "GET", path: "/edev", want: true},
		{method: "POST", path: "/edev", want: true},
		{method: "GET", path: "/edev/7", want: true},
		// Registered for GET only, so a DELETE is refused, not unrouted.
		{method: "DELETE", path: "/dcap", want: false},
		{method: "GET", path: "/nope", want: false},
		// One wildcard segment does not swallow a deeper path.
		{method: "GET", path: "/edev/7/der", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			got := ri.mounted(tc.method, tc.path)
			if got == nil {
				t.Fatal("mounted() = nil, want a definite answer when patterns were supplied")
			}
			if *got != tc.want {
				t.Errorf("mounted(%q, %q) = %v, want %v", tc.method, tc.path, *got, tc.want)
			}
		})
	}
}

// TestRouteIndexUnknownWithoutPatterns proves absence of an enumeration is
// reported as "unknown" rather than as "not mounted".
//
// Defaulting to false would manufacture routing disagreements on every row for
// any caller that did not supply a pattern list.
func TestRouteIndexUnknownWithoutPatterns(t *testing.T) {
	ri := newRouteIndex(nil)
	if got := ri.mounted("GET", "/dcap"); got != nil {
		t.Errorf("mounted() = %v, want nil (unknown) when no enumeration was supplied", *got)
	}
}

// TestConcretize covers turning a WADL template path into a concrete one.
func TestConcretize(t *testing.T) {
	discovered := []string{"/dcap", "/edev/3", "/edev/3/der/1"}

	tests := []struct {
		name       string
		path       string
		wantPath   string
		wantSource string
	}{
		{name: "literal", path: "/dcap", wantPath: "/dcap", wantSource: "literal"},
		{name: "prefers a discovered url", path: "/edev/{id1}", wantPath: "/edev/3", wantSource: "discovered"},
		{name: "multi-slot discovered", path: "/edev/{id1}/der/{id2}", wantPath: "/edev/3/der/1", wantSource: "discovered"},
		{name: "falls back to synthetic", path: "/upt/{id1}/mr", wantPath: "/upt/0/mr", wantSource: "synthetic"},
		{name: "multi-slot synthetic", path: "/x/{id1}/y/{id2}", wantPath: "/x/0/y/0", wantSource: "synthetic"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotSource := concretize(tc.path, discovered)
			if gotPath != tc.wantPath || gotSource != tc.wantSource {
				t.Errorf("concretize(%q) = (%q, %q), want (%q, %q)",
					tc.path, gotPath, gotSource, tc.wantPath, tc.wantSource)
			}
		})
	}
}

// TestConcretizeDoesNotInterpretPathMetacharacters guards the escape-then-
// substitute ordering. A regexp metacharacter in a real path must be matched
// literally, or a template could match a URL it has nothing to do with.
func TestConcretizeDoesNotInterpretPathMetacharacters(t *testing.T) {
	// "." must not match an arbitrary character.
	got, source := concretize("/a.b/{id1}", []string{"/axb/1"})
	if source == "discovered" {
		t.Errorf("concretize matched %q against template /a.b/{id1}: the '.' was treated as a wildcard", got)
	}
}

// TestClassify pins the verdict table. Each row is a routing or representation
// decision the report depends on, so a change in the mapping shows up as a
// named failure rather than as a shifted defect count.
func TestClassify(t *testing.T) {
	mandatoryGet := Method{Verb: "GET", Mode: ModeMandatory, DeclaredElements: []string{"EndDeviceList"}}
	errModeDelete := Method{Verb: "DELETE", Mode: ModeError, DeclaredStatuses: []int{400, 405}}
	optionalPost := Method{Verb: "POST", Mode: ModeOptional, DeclaredStatuses: []int{200, 201}}

	tests := []struct {
		name string
		m    Method
		r    response
		root string
		want Verdict
	}{
		{
			name: "mandatory GET, stdlib 404, is a missing route",
			m:    mandatoryGet,
			r:    response{status: 404, body: []byte("404 page not found\n")},
			want: VerdictUnrouted,
		},
		{
			name: "mandatory GET, handler 404, is an empty store",
			m:    mandatoryGet,
			r:    response{status: 404, body: []byte("<Error/>")},
			want: VerdictRoutedNotFound,
		},
		{
			name: "mandatory GET returning its declared element is conformant",
			m:    mandatoryGet,
			r:    response{status: 200},
			root: "EndDeviceList",
			want: VerdictConformant,
		},
		{
			name: "mandatory GET returning another type is a representation defect",
			m:    mandatoryGet,
			r:    response{status: 200},
			root: "DeviceCapability",
			want: VerdictWrongRespType,
		},
		{
			name: "E-mode refused with 405 is conformant",
			m:    errModeDelete,
			r:    response{status: 405},
			want: VerdictConformant,
		},
		{
			name: "E-mode refused with 400 is conformant",
			m:    errModeDelete,
			r:    response{status: 400},
			want: VerdictConformant,
		},
		{
			name: "E-mode accepted contradicts the standard",
			m:    errModeDelete,
			r:    response{status: 204},
			want: VerdictEModeAccepted,
		},
		{
			name: "E-mode 404 is its own milder class",
			m:    errModeDelete,
			r:    response{status: 404, body: []byte("404 page not found\n")},
			want: VerdictEMode404,
		},
		{
			name: "405 on a mandatory method is a refusal, not a missing route",
			m:    mandatoryGet,
			r:    response{status: 405},
			want: VerdictMethodRefused,
		},
		{
			name: "403 is ACL, never folded into routing",
			m:    mandatoryGet,
			r:    response{status: 403},
			want: VerdictForbiddenACL,
		},
		{
			name: "5xx is a server error, not a conformance verdict",
			m:    mandatoryGet,
			r:    response{status: 500},
			want: VerdictServerError,
		},
		{
			name: "status outside the declared set",
			m:    optionalPost,
			r:    response{status: 202},
			want: VerdictWrongStatus,
		},
		{
			name: "declared status is accepted",
			m:    optionalPost,
			r:    response{status: 201},
			want: VerdictConformant,
		},
		{
			name: "transport failure is recorded, never swallowed",
			m:    mandatoryGet,
			r:    response{transportError: "connection refused"},
			want: VerdictTransportError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.m, "/edev", tc.r, tc.root, nil); got != tc.want {
				t.Errorf("classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassifyHEADBorrowsGETEvidence covers the case the body discriminator
// structurally cannot decide on its own.
//
// The stdlib strips the body from a HEAD response, so the ServeMux marker
// never arrives. Rather than defaulting HEAD to a class, the sweep borrows the
// evidence from the GET on the same path, and reports "indeterminate" when no
// GET evidence exists.
func TestClassifyHEADBorrowsGETEvidence(t *testing.T) {
	head := Method{Verb: "HEAD", Mode: ModeMandatory}
	bodiless := response{status: 404}

	tests := []struct {
		name  string
		byPat map[string]bool
		want  Verdict
	}{
		{name: "GET on this path was unrouted", byPat: map[string]bool{"/edev": true}, want: VerdictUnrouted},
		{name: "GET on this path was a handler 404", byPat: map[string]bool{"/edev": false}, want: VerdictRoutedNotFound},
		{name: "no GET evidence, so no guess", byPat: map[string]bool{}, want: VerdictUnroutedUnknown},
		{name: "nil evidence map", byPat: nil, want: VerdictUnroutedUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(head, "/edev", bodiless, "", tc.byPat); got != tc.want {
				t.Errorf("classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRoutingDisagreements proves the two discriminators are actually
// compared, rather than one being trusted and the other decorative.
func TestRoutingDisagreements(t *testing.T) {
	yes, no := true, false
	res := &Results{Rows: []Row{
		// Wire says unrouted, enumeration agrees nothing matched: no conflict.
		{Phase: PhaseWADL, Path: "/a", Verdict: VerdictUnrouted, MountedByRouter: &no},
		// Wire says unrouted, enumeration says a pattern matched: conflict.
		{Phase: PhaseWADL, Path: "/b", Verdict: VerdictUnrouted, MountedByRouter: &yes},
		// Wire says a handler ran, enumeration says nothing matched: conflict.
		{Phase: PhaseWADL, Path: "/c", Verdict: VerdictRoutedNotFound, MountedByRouter: &no},
		// Wire says a handler ran, enumeration agrees: no conflict.
		{Phase: PhaseWADL, Path: "/d", Verdict: VerdictRoutedNotFound, MountedByRouter: &yes},
		// No enumeration available: never a conflict.
		{Phase: PhaseWADL, Path: "/e", Verdict: VerdictUnrouted, MountedByRouter: nil},
		// A non-routing verdict is out of scope for this comparison.
		{Phase: PhaseWADL, Path: "/f", Verdict: VerdictConformant, MountedByRouter: &no},
	}}

	got := res.RoutingDisagreements()
	if len(got) != 2 {
		t.Fatalf("RoutingDisagreements() returned %d rows, want 2: %+v", len(got), got)
	}
	paths := map[string]bool{}
	for _, row := range got {
		paths[row.Path] = true
	}
	for _, want := range []string{"/b", "/c"} {
		if !paths[want] {
			t.Errorf("expected a disagreement on %q", want)
		}
	}
}

// TestParseLinksSkipsSelfHref proves a resource's own href is not mistaken for
// an advertised link, which would make discovery re-walk every resource and
// report a self-reference as a dead link.
func TestParseLinksSkipsSelfHref(t *testing.T) {
	body := []byte(`<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap">` +
		`<EndDeviceListLink href="/edev"/>` +
		`<TimeLink href="/tm"/>` +
		`</DeviceCapability>`)

	got := parseLinks(body, "/dcap")
	if len(got) != 2 {
		t.Fatalf("parseLinks returned %d links, want 2: %+v", len(got), got)
	}
	if got[0].href != "/edev" || got[0].expectedRoot != "EndDeviceList" {
		t.Errorf("link 0 = %+v, want href /edev expecting EndDeviceList", got[0])
	}
	if got[1].href != "/tm" || got[1].expectedRoot != "Time" {
		t.Errorf("link 1 = %+v, want href /tm expecting Time", got[1])
	}
}

// TestCheckRequiredFields proves the field check reports a missing required
// child on a well-formed 200, which is the defect class a status-code sweep
// structurally cannot see.
func TestCheckRequiredFields(t *testing.T) {
	// responseRequired is an attribute on the Event base type while ReplyTo
	// is a child element, so both spellings must satisfy the check.
	complete := []byte(`<DERControl xmlns="urn:ieee:std:2030.5:ns" responseRequired="03">` +
		`<ReplyTo>/rsps/1</ReplyTo></DERControl>`)
	var rows []Row
	checkRequiredFields("/derp/1/derc/1", complete, &rows)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Verdict != VerdictFieldsPresent {
		t.Errorf("verdict = %q, want %q (missing: %v)", rows[0].Verdict, VerdictFieldsPresent, rows[0].MissingFields)
	}

	missing := []byte(`<DERControl xmlns="urn:ieee:std:2030.5:ns"><mRID>ABC</mRID></DERControl>`)
	rows = nil
	checkRequiredFields("/derp/1/derc/2", missing, &rows)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Verdict != VerdictFieldsMissing {
		t.Fatalf("verdict = %q, want %q", rows[0].Verdict, VerdictFieldsMissing)
	}
	if len(rows[0].MissingFields) != 2 {
		t.Errorf("missing = %v, want both ReplyTo and responseRequired", rows[0].MissingFields)
	}
}

// TestRootElement covers the body-type reader used for representation checks.
func TestRootElement(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "namespaced root", body: `<EndDeviceList xmlns="urn:ieee:std:2030.5:ns"/>`, want: "EndDeviceList"},
		{name: "with xml declaration", body: `<?xml version="1.0"?><Time xmlns="urn:ieee:std:2030.5:ns"/>`, want: "Time"},
		{name: "not xml", body: `404 page not found`, want: ""},
		{name: "empty", body: ``, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootElement([]byte(tc.body)); got != tc.want {
				t.Errorf("rootElement() = %q, want %q", got, tc.want)
			}
		})
	}
}
