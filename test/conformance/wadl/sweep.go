package wadl

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// This file walks a live IEEE 2030.5 server against the normative WADL.
//
// Four phases, in this order:
//
//	Seed.      Create a minimum of real server state, over the wire, so the
//	           read phases walk populated resources instead of an empty store.
//	Discovery. Breadth-first from /dcap, following every advertised href the
//	           way a conforming client does. This catches a link that is
//	           stamped but unrouted, and a link whose name promises one
//	           resource type and whose body delivers another.
//	Checks.    Required-field assertions on bodies discovery already fetched.
//	           This is the check class a status-code sweep structurally cannot
//	           make: a well-formed 200 of the right type that is still missing
//	           a field the standard requires.
//	WADL walk. Every (resource, method) pair the WADL declares, driven from
//	           the parsed model rather than a hand-written list. Any Location
//	           header is followed immediately, inline.
//
// Nothing is inferred from source code: every row records what came back on
// the wire. The one exception is the mounted-route cross-check, which is the
// router's own enumeration of what it registered, and which is compared
// against the wire evidence rather than substituted for it.

// muxNotFoundBody is exactly the body Go's net/http ServeMux writes when NO
// pattern matches the request path. A handler that ran and decided the
// resource does not exist writes its own message instead.
//
// That difference is the only wire-visible discriminator between "route not
// mounted" and "route mounted, resource absent", and the two are very
// different defects: the first is a spec gap, the second is an empty store.
// Every 404 in this harness is split on it rather than lumped together.
const muxNotFoundBody = "404 page not found"

// Verdict is the conformance class assigned to one observation. Classes are
// deliberately disjoint and ordered so the most diagnostic label wins:
// routing defects outrank representation defects, because an unrouted path
// cannot have a correct representation.
type Verdict string

const (
	VerdictConformant       Verdict = "conformant"
	VerdictUnrouted         Verdict = "unrouted"
	VerdictRoutedNotFound   Verdict = "routed_not_found"
	VerdictUnroutedUnknown  Verdict = "unrouted_indeterminate"
	VerdictMethodRefused    Verdict = "method_refused"
	VerdictWrongStatus      Verdict = "wrong_status"
	VerdictWrongRespType    Verdict = "wrong_response_type"
	VerdictWrongResType     Verdict = "wrong_resource_type"
	VerdictForbiddenACL     Verdict = "forbidden_acl"
	VerdictServerError      Verdict = "server_error"
	VerdictTransportError   Verdict = "transport_error"
	VerdictEModeAccepted    Verdict = "e_mode_accepted"
	VerdictEMode404         Verdict = "e_mode_404"
	VerdictMissingLocation  Verdict = "missing_required_location_header"
	VerdictDeadLinkUnrouted Verdict = "dead_link_unrouted"
	VerdictDeadLinkNotFound Verdict = "dead_link_not_found"
	VerdictLocationResolves Verdict = "location_resolves"
	VerdictLocationUnrouted Verdict = "location_unrouted"
	VerdictLocationNotFound Verdict = "location_not_found"
	VerdictFieldsPresent    Verdict = "fields_present"
	VerdictFieldsMissing    Verdict = "missing_required_fields"
	VerdictSeeded           Verdict = "seeded"
	VerdictSeedFailed       Verdict = "seed_failed"
)

// Phase names the stage of the sweep a row came from.
type Phase string

const (
	PhaseSeed       Phase = "seed"
	PhaseDiscovery  Phase = "discovery"
	PhaseFieldCheck Phase = "field_check"
	PhaseWADL       Phase = "wadl"
	PhaseLocation   Phase = "location"
)

// Row is one observation. Every field is either what was sent or what came
// back; nothing here is a guess.
type Row struct {
	Phase      Phase   `json:"phase"`
	ResourceID string  `json:"resource_id,omitempty"`
	WADLPath   string  `json:"wadl_path,omitempty"`
	Path       string  `json:"path"`
	Method     string  `json:"method,omitempty"`
	Mode       Mode    `json:"mode,omitempty"`
	IDSource   string  `json:"id_source,omitempty"`
	Verdict    Verdict `json:"verdict"`

	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Location    string `json:"location,omitempty"`
	RootElement string `json:"root_element,omitempty"`
	BodyBytes   int    `json:"body_bytes"`

	DeclaredStatuses []int    `json:"declared_statuses,omitempty"`
	DeclaredElements []string `json:"declared_elements,omitempty"`
	ExpectedRoot     string   `json:"expected_root,omitempty"`
	AdvertisedBy     string   `json:"advertised_by,omitempty"`
	LinkElement      string   `json:"link_element,omitempty"`
	MintedBy         string   `json:"minted_by,omitempty"`

	// BodyMinimal records that the request body was a minimal, namespace-correct
	// instance rather than a fully-populated one. A 400 on such a request
	// classifies routing, and is deliberately NOT a schema conformance verdict.
	BodyMinimal bool `json:"body_minimal,omitempty"`

	// MountedByRouter is the router's own answer to "is this path registered",
	// taken from the pattern list the router returned at build time. It is
	// recorded alongside the wire evidence so the two can be compared; see
	// Results.RoutingDisagreements.
	MountedByRouter *bool `json:"mounted_by_router,omitempty"`

	Element        string   `json:"element,omitempty"`
	RequiredFields []string `json:"required_fields,omitempty"`
	MissingFields  []string `json:"missing_fields,omitempty"`

	TransportError string `json:"transport_error,omitempty"`
}

// Results is the whole sweep.
type Results struct {
	Rows []Row
	// MountedPatterns is the router's boot-time route enumeration, when the
	// caller supplied one.
	MountedPatterns []string
}

// Target is the server under test.
type Target struct {
	// BaseURL is the scheme, host, and port, with no trailing slash.
	BaseURL string
	// Client performs the requests. For a live mTLS server this carries the
	// client certificate; for an in-process router it is the httptest client.
	Client *http.Client
	// MountedPatterns is the router's own enumeration of the patterns it
	// registered, as returned by assembly.BuildProtocolRouter. Supplying it
	// enables the second, independent unrouted discriminator. Empty is
	// allowed: the sweep then relies on the wire discriminator alone and
	// says so rather than pretending to a corroboration it does not have.
	MountedPatterns []string
}

// ---- HTTP ---------------------------------------------------------------

type response struct {
	status         int
	contentType    string
	location       string
	body           []byte
	transportError string
}

// maxBody caps how much of a response is read. Conformance decisions here
// are made on the status line, a header, and the root element, none of which
// live past the first few kilobytes, so a cap costs nothing and keeps a
// pathological body from exhausting the test process.
const maxBody = 8 << 20

func (t *Target) do(method, path string, body []byte) response {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, t.BaseURL+path, rdr)
	if err != nil {
		return response{transportError: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/sep+xml")
	if body != nil {
		req.Header.Set("Content-Type", "application/sep+xml")
	}

	resp, err := t.Client.Do(req)
	if err != nil {
		// Surfaced as a row, never swallowed: a transport failure is itself
		// an observation about the server under test.
		return response{transportError: fmt.Sprintf("%T: %v", err, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return response{
			status:         resp.StatusCode,
			transportError: fmt.Sprintf("reading body: %v", err),
		}
	}
	return response{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		location:    resp.Header.Get("Location"),
		body:        raw,
	}
}

func isMuxNotFound(r response) bool {
	return r.status == http.StatusNotFound &&
		strings.TrimSpace(string(r.body)) == muxNotFoundBody
}

// ---- route index --------------------------------------------------------

// routeIndex answers "did the router register a pattern matching this
// request" using Go's own ServeMux matcher.
//
// The patterns are replayed into a fresh ServeMux and matched with
// (*ServeMux).Handler, rather than reimplementing pattern matching here. A
// hand-rolled matcher would be a second, subtly different implementation of
// the very thing under test, and would report disagreements that are its own
// bugs. Replaying uses the exact matcher the server uses.
type routeIndex struct {
	mux   *http.ServeMux
	known bool
}

func newRouteIndex(patterns []string) *routeIndex {
	if len(patterns) == 0 {
		return &routeIndex{known: false}
	}
	mux := http.NewServeMux()
	seen := map[string]bool{}
	for _, p := range patterns {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	return &routeIndex{mux: mux, known: true}
}

// mounted reports whether a pattern matched, and nil when no enumeration was
// supplied. A nil result means "unknown", never "no".
func (ri *routeIndex) mounted(method, path string) *bool {
	if !ri.known {
		return nil
	}
	req, err := http.NewRequest(method, "http://localhost"+path, nil)
	if err != nil {
		return nil
	}
	_, pattern := ri.mux.Handler(req)
	ok := pattern != ""
	return &ok
}

// ---- XML helpers --------------------------------------------------------

func rootElement(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

type link struct {
	href         string
	linkElement  string
	expectedRoot string
	advertisedBy string
}

// parseLinks extracts every advertised href from a response body.
//
// The expected root is the link element name with a trailing "Link" stripped,
// which is the schema's own naming contract: a DERProgramListLink advertises a
// DERProgramList. That contract is what lets a body check catch a link that
// resolves but serves the wrong resource type.
func parseLinks(body []byte, sourcePath string) []link {
	if len(body) == 0 {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	var out []link
	first := true
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		isRoot := first
		first = false

		var href string
		for _, a := range se.Attr {
			if a.Name.Local == "href" {
				href = a.Value
				break
			}
		}
		if href == "" || isRoot {
			// A resource's own self-href is not an advertised link.
			continue
		}
		name := se.Name.Local
		expected := ""
		if strings.HasSuffix(name, "Link") {
			expected = strings.TrimSuffix(name, "Link")
		}
		out = append(out, link{
			href:         href,
			linkElement:  name,
			expectedRoot: expected,
			advertisedBy: sourcePath,
		})
	}
	return out
}

// ---- concretize ---------------------------------------------------------

var idSlot = regexp.MustCompile(`\\\{id\d+\\\}`)
var idSlotPlain = regexp.MustCompile(`\{id\d+\}`)

// concretize turns a WADL template path into a concrete path.
//
// It prefers a URL discovery actually returned, so the walk exercises real
// server state. It falls back to a synthetic id, which still distinguishes
// "unrouted" (404 for every id) from "routed but empty" (404 only for an
// unknown id).
//
// The literal path is regexp-escaped BEFORE the {idN} slots are turned into
// captures, so a regexp metacharacter appearing in a real path cannot be
// interpreted as syntax.
func concretize(path string, discovered []string) (string, string) {
	if !strings.Contains(path, "{") {
		return path, "literal"
	}
	quoted := regexp.QuoteMeta(path)
	pattern := "^" + idSlot.ReplaceAllString(quoted, `([^/]+)`) + "$"
	rx, err := regexp.Compile(pattern)
	if err == nil {
		for _, u := range discovered {
			if rx.MatchString(u) {
				return u, "discovered"
			}
		}
	}
	return idSlotPlain.ReplaceAllString(path, "0"), "synthetic"
}

// ---- classification -----------------------------------------------------

func classify(m Method, concretePath string, r response, root string, muxByPath map[string]bool) Verdict {
	if r.transportError != "" {
		return VerdictTransportError
	}

	if r.status == http.StatusNotFound {
		// An E-mode method declares 400/405. A 404 there is a distinct,
		// milder finding than a 404 on a Mandatory method, so it gets its
		// own class rather than being folded into "unrouted".
		if m.Mode == ModeError {
			return VerdictEMode404
		}
		// HEAD is special: the stdlib strips the body from a HEAD response,
		// so the ServeMux marker text never arrives and the body
		// discriminator cannot fire. Borrow the evidence from the GET on the
		// same path (GETs always run first) rather than silently defaulting
		// HEAD to the wrong class.
		if m.Verb == http.MethodHead {
			was, ok := muxByPath[concretePath]
			if !ok {
				return VerdictUnroutedUnknown
			}
			if was {
				return VerdictUnrouted
			}
			return VerdictRoutedNotFound
		}
		if isMuxNotFound(r) {
			return VerdictUnrouted
		}
		return VerdictRoutedNotFound
	}

	if r.status == http.StatusMethodNotAllowed {
		if m.Mode == ModeError {
			return VerdictConformant
		}
		return VerdictMethodRefused
	}

	if r.status == http.StatusForbidden {
		// ACL, not routing. Never folded into a routing class.
		return VerdictForbiddenACL
	}

	if m.Mode == ModeError {
		if r.status == http.StatusBadRequest || r.status == http.StatusMethodNotAllowed {
			return VerdictConformant
		}
		if r.status < 400 {
			return VerdictEModeAccepted
		}
		return VerdictWrongStatus
	}

	if r.status >= 500 {
		return VerdictServerError
	}
	if len(m.DeclaredStatuses) > 0 && !containsInt(m.DeclaredStatuses, r.status) {
		return VerdictWrongStatus
	}
	if len(m.DeclaredStatuses) == 0 && r.status >= 400 {
		return VerdictWrongStatus
	}

	// Representation check. Only meaningful when the WADL declares a response
	// element and the server actually returned a body.
	if len(m.DeclaredElements) > 0 && root != "" && !containsStr(m.DeclaredElements, root) {
		return VerdictWrongRespType
	}
	return VerdictConformant
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---- required-field checks ---------------------------------------------

// requiredChildren lists elements the standard requires on every instance of
// a named element found anywhere in a discovered response body.
//
// These are hand-authored assertions about specific, cited requirements, not
// a mechanical transcription of the WADL or the schema. IEEE 2030.5-2018
// section 12.2 and CSIP: an event a client is expected to acknowledge carries
// responseRequired, and ReplyTo tells it where to send that acknowledgement.
// Without both, the response function set cannot close the loop on a
// DERControl.
var requiredChildren = map[string][]string{
	"DERControl":        {"ReplyTo", "responseRequired"},
	"DefaultDERControl": {"ReplyTo", "responseRequired"},
	"TextMessage":       {"ReplyTo", "responseRequired"},
}

// checkRequiredFields walks a body and asserts the required children.
//
// responseRequired is an ATTRIBUTE on the Event base type in sep.xsd while
// ReplyTo is a child ELEMENT, so both spellings are probed for each name
// rather than assuming one.
func checkRequiredFields(path string, body []byte, rows *[]Row) {
	if len(body) == 0 {
		return
	}
	dec := xml.NewDecoder(bytes.NewReader(body))

	type frame struct {
		name     string
		required []string
		children map[string]bool
		attrs    map[string]bool
		href     string
	}
	var stack []*frame

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > 0 {
				stack[len(stack)-1].children[t.Name.Local] = true
			}
			req, watched := requiredChildren[t.Name.Local]
			f := &frame{name: t.Name.Local, children: map[string]bool{}, attrs: map[string]bool{}}
			if watched {
				f.required = req
			}
			for _, a := range t.Attr {
				f.attrs[a.Name.Local] = true
				if a.Name.Local == "href" {
					f.href = a.Value
				}
			}
			stack = append(stack, f)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if f.required == nil {
				continue
			}
			var missing []string
			for _, field := range f.required {
				if !f.children[field] && !f.attrs[field] {
					missing = append(missing, field)
				}
			}
			v := VerdictFieldsPresent
			if len(missing) > 0 {
				v = VerdictFieldsMissing
			}
			*rows = append(*rows, Row{
				Phase:          PhaseFieldCheck,
				Path:           path,
				Element:        f.name,
				Location:       f.href,
				RequiredFields: f.required,
				MissingFields:  missing,
				Verdict:        v,
			})
		}
	}
}

// ---- phases -------------------------------------------------------------

const sepNS = "urn:ieee:std:2030.5:ns"

// seedLFDI and the seed mRIDs are public identifiers, not credentials.
const seedLFDI = "F445603B47AF81A58CF7841F89D3DAF464EECA1E"

type seed struct {
	method  string
	path    string
	element string
	body    string
}

// seeds create a minimum of real server state before the read phases.
//
// Without this, most instance paths 404 because the store is empty, and an
// empty-store 404 would be indistinguishable from an unrouted one at the
// level of the status code alone.
//
// The bodies are populated rather than minimal: a minimal instance is fine
// for classifying ROUTING in the WADL walk, but a seed that gets rejected
// leaves a whole subtree unwalked, and an unwalked subtree silently
// understates the defect count.
func seedSet() []seed {
	return []seed{
		{
			method: http.MethodPost, path: "/edev", element: "EndDevice",
			body: `<EndDevice xmlns="` + sepNS + `"><sFDI>655709971722</sFDI>` +
				`<lFDI>` + seedLFDI + `</lFDI><changedTime>1785700000</changedTime>` +
				`<enabled>true</enabled></EndDevice>`,
		},
		{
			method: http.MethodPost, path: "/mup", element: "MirrorUsagePoint",
			body: `<MirrorUsagePoint xmlns="` + sepNS + `">` +
				`<mRID>0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FAA</mRID>` +
				`<description>wadl sweep mup</description><roleFlags>03</roleFlags>` +
				`<serviceCategoryKind>0</serviceCategoryKind><status>1</status>` +
				`<deviceLFDI>` + seedLFDI + `</deviceLFDI></MirrorUsagePoint>`,
		},
		{
			method: http.MethodPost, path: "/upt", element: "UsagePoint",
			body: `<UsagePoint xmlns="` + sepNS + `">` +
				`<mRID>0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FBB</mRID>` +
				`<description>wadl sweep upt</description><roleFlags>03</roleFlags>` +
				`<serviceCategoryKind>0</serviceCategoryKind><status>1</status>` +
				`</UsagePoint>`,
		},
	}
}

func (t *Target) phaseSeed(rows *[]Row, ri *routeIndex) {
	for _, s := range seedSet() {
		r := t.do(s.method, s.path, []byte(s.body))
		v := Verdict(fmt.Sprintf("%s_%d", VerdictSeedFailed, r.status))
		if r.status == http.StatusOK || r.status == http.StatusCreated {
			v = VerdictSeeded
		}
		row := Row{
			Phase:           PhaseSeed,
			Path:            s.path,
			Method:          s.method,
			Element:         s.element,
			Status:          r.status,
			ContentType:     r.contentType,
			Location:        r.location,
			RootElement:     rootElement(r.body),
			BodyBytes:       len(r.body),
			Verdict:         v,
			TransportError:  r.transportError,
			MountedByRouter: ri.mounted(s.method, s.path),
		}
		*rows = append(*rows, row)
		if r.location != "" {
			t.followLocation(rows, ri, r.location, s.method+" "+s.path, s.path)
		}
	}
}

const maxDiscoveryURLs = 600

func (t *Target) phaseDiscovery(rows *[]Row, ri *routeIndex, bodies map[string][]byte) []string {
	seen := map[string]bool{}
	queue := []link{{href: "/dcap", expectedRoot: "DeviceCapability", advertisedBy: "(root)"}}
	var discovered []string

	for len(queue) > 0 && len(seen) < maxDiscoveryURLs {
		l := queue[0]
		queue = queue[1:]
		if seen[l.href] {
			continue
		}
		seen[l.href] = true

		r := t.do(http.MethodGet, l.href, nil)
		root := rootElement(r.body)

		v := VerdictConformant
		switch {
		case r.transportError != "":
			v = VerdictTransportError
		case r.status == http.StatusNotFound && isMuxNotFound(r):
			v = VerdictDeadLinkUnrouted
		case r.status == http.StatusNotFound:
			v = VerdictDeadLinkNotFound
		case r.status == http.StatusMethodNotAllowed:
			v = VerdictMethodRefused
		case r.status >= 400:
			v = Verdict(fmt.Sprintf("http_%d", r.status))
		case l.expectedRoot != "" && root != "" && root != l.expectedRoot:
			v = VerdictWrongResType
		}

		*rows = append(*rows, Row{
			Phase:           PhaseDiscovery,
			Path:            l.href,
			Method:          http.MethodGet,
			AdvertisedBy:    l.advertisedBy,
			LinkElement:     l.linkElement,
			ExpectedRoot:    l.expectedRoot,
			Status:          r.status,
			ContentType:     r.contentType,
			RootElement:     root,
			Location:        r.location,
			BodyBytes:       len(r.body),
			Verdict:         v,
			TransportError:  r.transportError,
			MountedByRouter: ri.mounted(http.MethodGet, l.href),
		})
		discovered = append(discovered, l.href)
		bodies[l.href] = r.body

		if r.status == http.StatusOK {
			for _, nxt := range parseLinks(r.body, l.href) {
				if !seen[nxt.href] && strings.HasPrefix(nxt.href, "/") {
					queue = append(queue, nxt)
				}
			}
		}
	}
	return discovered
}

func (t *Target) phaseWADL(m *Model, discovered []string, rows *[]Row, ri *routeIndex) {
	// GET before HEAD before the mutating methods. GET-first is load bearing:
	// HEAD's 404 classification borrows the body evidence only a GET supplies.
	// Mutating methods run last so discovery-derived ids are still valid when
	// the read phases use them.
	order := map[string]int{http.MethodGet: 0, http.MethodHead: 1}
	ordered := make([]Method, len(m.Methods))
	copy(ordered, m.Methods)
	sort.SliceStable(ordered, func(i, j int) bool {
		oi, ok := order[ordered[i].Verb]
		if !ok {
			oi = 2
		}
		oj, ok := order[ordered[j].Verb]
		if !ok {
			oj = 2
		}
		return oi < oj
	})

	muxByPath := map[string]bool{}
	for _, meth := range ordered {
		path, idSource := concretize(meth.Path, discovered)

		var body []byte
		if (meth.Verb == http.MethodPut || meth.Verb == http.MethodPost) && len(meth.RequestElements) > 0 {
			body = []byte(`<` + meth.RequestElements[0] + ` xmlns="` + sepNS + `"/>`)
		}

		r := t.do(meth.Verb, path, body)
		root := rootElement(r.body)
		if meth.Verb == http.MethodGet && r.status == http.StatusNotFound {
			muxByPath[path] = isMuxNotFound(r)
		}
		v := classify(meth, path, r, root, muxByPath)

		*rows = append(*rows, Row{
			Phase:            PhaseWADL,
			ResourceID:       meth.ResourceID,
			WADLPath:         meth.Path,
			Path:             path,
			IDSource:         idSource,
			Method:           meth.Verb,
			Mode:             meth.Mode,
			DeclaredStatuses: meth.DeclaredStatuses,
			DeclaredElements: meth.DeclaredElements,
			Status:           r.status,
			ContentType:      r.contentType,
			RootElement:      root,
			Location:         r.location,
			BodyBytes:        len(r.body),
			BodyMinimal:      body != nil,
			Verdict:          v,
			TransportError:   r.transportError,
			MountedByRouter:  ri.mounted(meth.Verb, path),
		})

		switch {
		case r.location != "":
			t.followLocation(rows, ri, r.location, meth.Verb+" "+path, meth.Path)
		case meth.RequiresLocationHeader && (r.status == http.StatusOK || r.status == http.StatusCreated):
			// The WADL marks this Location header required="true".
			*rows = append(*rows, Row{
				Phase:      PhaseWADL,
				ResourceID: meth.ResourceID,
				WADLPath:   meth.Path,
				Path:       path,
				Method:     meth.Verb,
				Mode:       meth.Mode,
				Status:     r.status,
				Verdict:    VerdictMissingLocation,
			})
		}
	}
}

var schemeAuthority = regexp.MustCompile(`^https?://[^/]+`)

// followLocation GETs a Location header immediately after it was minted.
//
// Immediacy is the point. Following at end-of-run would let a later DELETE in
// the same sweep turn a working Location into a 404 and manufacture a defect
// the server never committed. A real client POSTs and reads the Location right
// away; the harness does the same, so the observation means what it appears to.
func (t *Target) followLocation(rows *[]Row, ri *routeIndex, loc, mintedBy, wadlPath string) {
	// Strip scheme and authority so the harness stays on the connection it
	// opened rather than chasing an external host.
	target := schemeAuthority.ReplaceAllString(loc, "")
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}

	r := t.do(http.MethodGet, target, nil)
	root := rootElement(r.body)

	v := VerdictLocationResolves
	switch {
	case r.transportError != "":
		v = VerdictTransportError
	case r.status == http.StatusNotFound && isMuxNotFound(r):
		v = VerdictLocationUnrouted
	case r.status == http.StatusNotFound:
		v = VerdictLocationNotFound
	case r.status >= 400:
		v = Verdict(fmt.Sprintf("location_http_%d", r.status))
	}

	*rows = append(*rows, Row{
		Phase:           PhaseLocation,
		Path:            target,
		Method:          http.MethodGet,
		Location:        loc,
		MintedBy:        mintedBy,
		WADLPath:        wadlPath,
		Status:          r.status,
		ContentType:     r.contentType,
		RootElement:     root,
		BodyBytes:       len(r.body),
		Verdict:         v,
		TransportError:  r.transportError,
		MountedByRouter: ri.mounted(http.MethodGet, target),
	})
}

// Sweep runs every phase against the target and returns all observations.
func Sweep(t *Target, m *Model) *Results {
	ri := newRouteIndex(t.MountedPatterns)
	res := &Results{MountedPatterns: t.MountedPatterns}

	t.phaseSeed(&res.Rows, ri)

	bodies := map[string][]byte{}
	discovered := t.phaseDiscovery(&res.Rows, ri, bodies)

	// Field checks operate on bodies discovery already retrieved, so they add
	// no requests and cannot perturb server state.
	paths := make([]string, 0, len(bodies))
	for p := range bodies {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		checkRequiredFields(p, bodies[p], &res.Rows)
	}

	t.phaseWADL(m, discovered, &res.Rows, ri)
	return res
}

// ---- reporting ----------------------------------------------------------

// Count returns the number of rows in a phase with a verdict.
func (r *Results) Count(p Phase, v Verdict) int {
	n := 0
	for _, row := range r.Rows {
		if row.Phase == p && row.Verdict == v {
			n++
		}
	}
	return n
}

// CountMode returns WADL-phase rows with a given mode and verdict.
func (r *Results) CountMode(mode Mode, v Verdict) int {
	n := 0
	for _, row := range r.Rows {
		if row.Phase == PhaseWADL && row.Mode == mode && row.Verdict == v {
			n++
		}
	}
	return n
}

// Filter returns every row matching a predicate.
func (r *Results) Filter(pred func(Row) bool) []Row {
	var out []Row
	for _, row := range r.Rows {
		if pred(row) {
			out = append(out, row)
		}
	}
	return out
}

// RoutingDisagreements returns rows where the wire evidence and the router's
// own route enumeration disagree about whether a path is mounted.
//
// This is the whole point of carrying two independent discriminators. Either
// side alone can be wrong: the wire marker can be masked by a middleware that
// rewrites a 404 body, and the enumeration can list a pattern that a wrapping
// handler shadows. A disagreement is therefore a finding about the HARNESS or
// about a middleware, and it must be surfaced rather than silently resolved in
// favour of whichever source the author trusted more.
func (r *Results) RoutingDisagreements() []Row {
	return r.Filter(func(row Row) bool {
		if row.MountedByRouter == nil {
			return false
		}
		switch row.Verdict {
		case VerdictUnrouted, VerdictDeadLinkUnrouted, VerdictLocationUnrouted:
			// Wire says unrouted; enumeration says a pattern matched.
			return *row.MountedByRouter
		case VerdictRoutedNotFound, VerdictDeadLinkNotFound, VerdictLocationNotFound:
			// Wire says a handler ran; enumeration says nothing matched.
			return !*row.MountedByRouter
		}
		return false
	})
}

// Summary renders phase and verdict counts as stable, sorted lines.
func (r *Results) Summary() []string {
	type key struct {
		p Phase
		v Verdict
	}
	counts := map[key]int{}
	for _, row := range r.Rows {
		counts[key{row.Phase, row.Verdict}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].p != keys[j].p {
			return keys[i].p < keys[j].p
		}
		return keys[i].v < keys[j].v
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%-10s %-34s %d", k.p, k.v, counts[k]))
	}
	return out
}
