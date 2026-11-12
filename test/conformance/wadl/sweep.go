package wadl

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
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
	VerdictConformant      Verdict = "conformant"
	VerdictUnrouted        Verdict = "unrouted"
	VerdictRoutedNotFound  Verdict = "routed_not_found"
	VerdictUnroutedUnknown Verdict = "unrouted_indeterminate"
	VerdictMethodRefused   Verdict = "method_refused"
	// VerdictDeprecatedRefused is a Deprecated method answered 405, which the
	// method-mode policy accepts.
	VerdictDeprecatedRefused Verdict = "deprecated_refused"
	VerdictWrongStatus       Verdict = "wrong_status"
	VerdictWrongRespType     Verdict = "wrong_response_type"
	VerdictWrongResType      Verdict = "wrong_resource_type"
	VerdictForbiddenACL      Verdict = "forbidden_acl"
	VerdictServerError       Verdict = "server_error"
	VerdictTransportError    Verdict = "transport_error"
	VerdictEModeAccepted     Verdict = "e_mode_accepted"
	VerdictEMode404          Verdict = "e_mode_404"
	VerdictMissingLocation   Verdict = "missing_required_location_header"
	VerdictDeadLinkUnrouted  Verdict = "dead_link_unrouted"
	VerdictDeadLinkNotFound  Verdict = "dead_link_not_found"
	VerdictLocationResolves  Verdict = "location_resolves"
	VerdictLocationUnrouted  Verdict = "location_unrouted"
	VerdictLocationNotFound  Verdict = "location_not_found"
	VerdictFieldsPresent     Verdict = "fields_present"
	VerdictFieldsMissing     Verdict = "missing_required_fields"
	// VerdictNotReached marks a WADL resource with a Mandatory or Optional
	// method that no link followed from /dcap led to, though the schema names a
	// link for it. It counts as non-conformant.
	VerdictNotReached Verdict = "not_reached"
	// VerdictDeviceForbidden and VerdictDeviceNotForbidden score the ordinary
	// device's attempt at a manager-only write: 403 from an authority check, or
	// anything else.
	VerdictDeviceForbidden    Verdict = "device_forbidden"
	VerdictDeviceNotForbidden Verdict = "device_not_forbidden"
	VerdictSeeded             Verdict = "seeded"
	VerdictSeedFailed         Verdict = "seed_failed"
	// VerdictSeedFailedDirect marks a seed written straight into the stores
	// that did not complete; Row.TransportError carries the reason.
	VerdictSeedFailedDirect Verdict = "seed_failed_direct"
)

// Phase names the stage of the sweep a row came from.
type Phase string

const (
	PhaseSeed       Phase = "seed"
	PhaseDiscovery  Phase = "discovery"
	PhaseFieldCheck Phase = "field_check"
	PhaseWADL       Phase = "wadl"
	PhaseLocation   Phase = "location"
	// PhaseRestricted rows are the ordinary device's attempts at the writes the
	// server restricts to a manager certificate.
	PhaseRestricted Phase = "restricted"
)

// Row is one observation. Every field is either what was sent or what came
// back; nothing here is a guess.
type Row struct {
	Phase      Phase  `json:"phase"`
	ResourceID string `json:"resource_id,omitempty"`
	WADLPath   string `json:"wadl_path,omitempty"`
	Path       string `json:"path"`
	Method     string `json:"method,omitempty"`
	Mode       Mode   `json:"mode,omitempty"`
	IDSource   string `json:"id_source,omitempty"`
	// AsManager records that the request carried the manager certificate.
	AsManager bool    `json:"as_manager,omitempty"`
	Verdict   Verdict `json:"verdict"`

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
	// CrawlReached is how many WADL resources with a Mandatory or Optional method
	// the walk from /dcap found. Zero means the crawl failed, however the
	// sample-path probes scored.
	CrawlReached int
}

// LinksFollowed is how many discovery fetches were advertised by a link in a
// body, not seeded as the root. Zero means the crawl followed nothing from /dcap.
func (r *Results) LinksFollowed() int {
	n := 0
	for _, row := range r.Rows {
		if row.Phase == PhaseDiscovery && row.AdvertisedBy != crawlRoot {
			n++
		}
	}
	return n
}

// Target is the server under test.
type Target struct {
	// BaseURL is the scheme, host, and port, with no trailing slash.
	BaseURL string
	// Client performs the requests. For a live mTLS server this carries the
	// client certificate; for an in-process router it is the httptest client.
	Client *http.Client
	// ManagerClient presents a manager certificate. When set, the writes the
	// server restricts to a manager (restrictedWrite) are made with it, and the
	// ordinary Client's attempt at each is recorded as a restricted row. Nil
	// sends every request with Client and records no restricted rows.
	ManagerClient *http.Client
	// ManagedDeviceID is the store id of the EndDevice ManagerClient manages.
	// A manager acts only on its own device, so each restricted write under
	// /edev/{id} is sent, as the manager and as the ordinary device alike, to
	// this device in place of the one the crawl found. Empty leaves the path
	// as crawled.
	ManagedDeviceID string
	// MountedPatterns is the router's own enumeration of the patterns it
	// registered, as returned by assembly.BuildProtocolRouter. Supplying it
	// enables the second, independent unrouted discriminator. Empty is
	// allowed: the sweep then relies on the wire discriminator alone and
	// says so rather than pretending to a corroboration it does not have.
	MountedPatterns []string
	// Links is the schema's Link fields. Nil makes the sweep strict: a resource
	// the crawl misses is not_reached, never probed at its sample path.
	Links *LinkSchema
	// Stores are the stores the target serves. They are read by the seeds that
	// write server-authored state directly; nil makes those seeds fail.
	Stores *server.Stores
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
	return t.doWith(t.Client, method, path, body)
}

func (t *Target) doWith(client *http.Client, method, path string, body []byte) response {
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

	resp, err := client.Do(req)
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

// isRouteRefusal reports the refusal the server gives a path no route family
// names. It is a 403, but not an ACL denial: the body says no route exists.
func isRouteRefusal(r response) bool {
	return r.status == http.StatusForbidden &&
		strings.TrimSpace(string(r.body)) == routefamily.RefusalBody
}

// isUnrouted is the wire discriminator for "no route": the mux's own 404, or
// the server's refusal of a path no family names.
func isUnrouted(r response) bool { return isMuxNotFound(r) || isRouteRefusal(r) }

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
	// typeName is the element's xsi:type, which names the resource a list member is.
	typeName string
	// manager is set on a link followed with the manager certificate: the
	// server shows the usage point list only to a manager, so a device's own
	// crawl cannot reach what hangs below it.
	manager bool
}

// linkRootExceptions names the links whose resource has a different root
// element than the link name minus "Link", because sep.xsd types the link for
// a role and the resource for its shape. A later Active list adds its row here.
var linkRootExceptions = map[string]string{
	// sep.xsd:4628 types ActiveDERControlListLink; the list it names is a
	// DERControlList (the WADL resource ActiveDERControlList has no element
	// of its own).
	"ActiveDERControlListLink": "DERControlList",
}

// parseLinks extracts every advertised href from a response body.
//
// The expected root is the link element name with a trailing "Link" stripped,
// which is the schema's own naming contract: a DERProgramListLink advertises a
// DERProgramList, except the links in linkRootExceptions. That contract is
// what lets a body check catch a link that resolves but serves the wrong
// resource type.
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

		var href, typeName string
		for _, a := range se.Attr {
			switch {
			case a.Name.Local == "href" && href == "":
				href = a.Value
			case a.Name.Local == "type" && a.Name.Space == xsiNS:
				typeName = stripNS(a.Value)
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
			if root, ok := linkRootExceptions[name]; ok {
				expected = root
			}
		}
		out = append(out, link{
			href:         href,
			linkElement:  name,
			expectedRoot: expected,
			advertisedBy: sourcePath,
			typeName:     typeName,
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
	// A path under a device keeps the discovered device id, so the child
	// answers for the device the sweep seeded rather than for a device 0 that
	// does not exist.
	if dev := discoveredDevice(discovered); dev != "" && strings.HasPrefix(path, "/edev/{id1}") {
		path = strings.Replace(path, "{id1}", dev, 1)
	}
	return idSlotPlain.ReplaceAllString(path, "0"), "synthetic"
}

var devicePath = regexp.MustCompile(`^/edev/([^/]+)$`)

// discoveredDevice is the id of the first EndDevice discovery returned, or "".
func discoveredDevice(discovered []string) string {
	for _, u := range discovered {
		if m := devicePath.FindStringSubmatch(u); m != nil {
			return m[1]
		}
	}
	return ""
}

// ---- classification -----------------------------------------------------

func classify(m Method, concretePath string, r response, root string, muxByPath map[string]bool) Verdict {
	if r.transportError != "" {
		return VerdictTransportError
	}

	// A HEAD refusal has no body to carry the marker, so it borrows the GET
	// evidence like a HEAD 404 does.
	headRefusal := r.status == http.StatusForbidden && m.Verb == http.MethodHead && muxByPath[concretePath]
	if r.status == http.StatusNotFound || isRouteRefusal(r) || headRefusal {
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
		if isUnrouted(r) {
			return VerdictUnrouted
		}
		return VerdictRoutedNotFound
	}

	if r.status == http.StatusMethodNotAllowed {
		if m.Mode == ModeError {
			return VerdictConformant
		}
		if m.Mode == ModeDeprecated {
			return VerdictDeprecatedRefused
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

// The seed mRIDs are public identifiers, not credentials.

// devToken in a seed path stands for the EndDevice the first seed created,
// so every child seed addresses the discovered device and not a guessed id.
const devToken = "{dev}"

type seed struct {
	method  string
	path    string
	element string
	body    string
	// direct writes state no protocol route can create (a Registration, a
	// MessagingProgram, which the server authors itself). It runs in seedSet's
	// order, against the stores the target serves, and gets the device id the
	// first seed created. method and path are for the row only.
	direct func(ctx context.Context, s *server.Stores, dev string) error
	// manager sends the seed with the manager certificate, for a write the
	// server restricts to one.
	manager bool
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
//
// This is the one place a fixture is added: a resource the walk probes and the
// server does not create on its own gets a seed here, never a second list.
func seedSet() []seed {
	return []seed{
		{
			method: http.MethodPost, path: "/edev", element: "EndDevice",
			// No sFDI or lFDI: the server takes the posting client's own
			// certificate identity, and refuses a body that claims another (#1127).
			body: `<EndDevice xmlns="` + sepNS + `"><changedTime>1785700000</changedTime>` +
				`<enabled>true</enabled></EndDevice>`,
		},
		{
			method: http.MethodPost, path: "/mup", element: "MirrorUsagePoint",
			// No deviceLFDI: absent resolves to the posting client's own
			// certificate identity (#720); a claimed
			// identity other than the sweep's own would be refused with 403.
			body: `<MirrorUsagePoint xmlns="` + sepNS + `">` +
				`<mRID>0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FAA</mRID>` +
				`<description>wadl sweep mup</description><roleFlags>03</roleFlags>` +
				`<serviceCategoryKind>0</serviceCategoryKind><status>1</status></MirrorUsagePoint>`,
		},
		{
			method: http.MethodPost, path: "/upt", element: "UsagePoint", manager: true,
			body: `<UsagePoint xmlns="` + sepNS + `">` +
				`<mRID>0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FBB</mRID>` +
				`<description>wadl sweep upt</description><roleFlags>03</roleFlags>` +
				`<serviceCategoryKind>0</serviceCategoryKind><status>1</status>` +
				`</UsagePoint>`,
		},
		{
			// A MeterReading with one ReadingSet, one Reading and a ReadingType
			// under the UsagePoint above, so the crawl can follow the whole
			// hierarchy. No protocol route creates them.
			method: http.MethodGet, path: "/upt/{upt}/mr/1", element: "MeterReading",
			direct: func(ctx context.Context, s *server.Stores, _ string) error {
				upt := coremetering.UsagePointStoreID("0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FBB")
				mrHref := coremetering.UsagePointHref(upt) + "/mr/1"
				setHref := mrHref + "/rs/1"
				mr := sep2.MeterReading{
					IdentifiedObject:   sep2.IdentifiedObject{MRID: "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FE1"},
					ReadingTypeLink:    &sep2.Link{Href: mrHref + "/rt"},
					ReadingSetListLink: &sep2.ListLink{Href: mrHref + "/rs", All: 1},
				}
				mr.Href = mrHref
				if err := s.MeterReadings.Create(ctx, upt, "1", mr); err != nil {
					return err
				}
				uom := sep2.UomWattHours
				rt := sep2.ReadingType{Uom: &uom}
				rt.Href = mrHref + "/rt"
				if err := s.ReadingTypes.Create(ctx, upt, "1", rt); err != nil {
					return err
				}
				set := sep2.ReadingSet{
					IdentifiedObject: sep2.IdentifiedObject{MRID: "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FE2"},
					TimePeriod:       sep2.DateTimeInterval{Start: 1785700000, Duration: 900},
					ReadingListLink:  &sep2.ListLink{Href: setHref + "/r", All: 1},
				}
				set.Href = setHref
				setScope, _ := coremetering.ReadingSetScopeKey(upt, "1")
				if err := s.ReadingSets.Create(ctx, setScope, "1", set); err != nil {
					return err
				}
				value, localID := int64(42), sep2.HexBinary16(1)
				reading := sep2.Reading{Value: &value, LocalID: &localID, TimePeriod: &sep2.DateTimeInterval{Start: 1785700000, Duration: 900}}
				reading.Href = setHref + "/r/1"
				readingScope, _ := coremetering.ReadingScopeKey(upt, "1", "1")
				return s.Readings.Create(ctx, readingScope, "1", reading)
			},
		},
		{
			method: http.MethodPost, path: "/edev/" + devToken + "/lel", element: "LogEvent",
			body: `<LogEvent xmlns="` + sepNS + `"><createdDateTime>1785700000</createdDateTime>` +
				`<logEventCode>1</logEventCode><logEventID>42</logEventID>` +
				`<logEventPEN>37244</logEventPEN><profileID>2</profileID></LogEvent>`,
		},
		{
			method: http.MethodPost, path: "/edev/" + devToken + "/sub", element: "Subscription",
			body: `<Subscription xmlns="` + sepNS + `"><subscribedResource>/edev/` + devToken + `/fsa</subscribedResource>` +
				`<encoding>0</encoding><level>+S1</level><limit>1</limit>` +
				`<notificationURI>http://192.0.2.1/n</notificationURI></Subscription>`,
		},
		{
			method: http.MethodPost, path: "/edev/" + devToken + "/frq", element: "FlowReservationRequest",
			body: `<FlowReservationRequest xmlns="` + sepNS + `"><mRID>0102030405060708090A0B0C0D0E0F10</mRID>` +
				`<creationTime>1785700000</creationTime>` +
				`<energyRequested><multiplier>3</multiplier><value>15</value></energyRequested>` +
				`<intervalRequested><duration>10800</duration><start>1785700000</start></intervalRequested>` +
				`<powerRequested><multiplier>3</multiplier><value>7</value></powerRequested>` +
				`<RequestStatus><dateTime>1785700000</dateTime><requestStatus>0</requestStatus></RequestStatus>` +
				`</FlowReservationRequest>`,
		},
		{
			// The server answers Response POSTs from any device, and one stored
			// Response serves every Response type the WADL lists at this path.
			method: http.MethodPost, path: "/rsps/1/rsp", element: "Response",
			body: `<Response xmlns="` + sepNS + `"><createdDateTime>1785700000</createdDateTime>` +
				`<status>1</status><subject>0123456789ABCDEF0123456789ABCDEF</subject></Response>`,
		},
		{
			// Registration PINs are server-authored and the policy is fail-closed,
			// so no route creates one. The PIN is a public test constant.
			method: http.MethodGet, path: "/edev/" + devToken + "/rg", element: "Registration",
			direct: func(ctx context.Context, s *server.Stores, dev string) error {
				reg := sep2.Registration{DateTimeRegistered: 1785700000, PIN: 123455, PollRate: 900}
				reg.Href = "/edev/" + dev + "/rg"
				return s.Registrations.Create(ctx, dev, reg)
			},
		},
		{
			// An FSA, a DERProgram under it and one control in force, so the
			// crawl can follow FSA -> program -> control lists, including the
			// program's active list. No protocol route creates any of them.
			method: http.MethodGet, path: "/edev/" + devToken + "/fsa/1", element: "DERProgram",
			direct: func(ctx context.Context, s *server.Stores, dev string) error {
				progHref := coreder.DERProgramHref(dev, "1", "1")
				fsa := sep2.FunctionSetAssignments{IdentifiedObject: sep2.IdentifiedObject{MRID: "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FD1"}}
				fsa.Href = "/edev/" + dev + "/fsa/1"
				fsa.DERProgramListLink = &sep2.ListLink{Href: fsa.Href + "/derp", All: 1}
				if err := s.FSAs.Create(ctx, dev, "1", fsa); err != nil {
					return err
				}
				prog := fsaprogram.CopyProgram(dev, "1", memory.ProgramTemplate{
					ID: "1", MRID: "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FD2", Description: "wadl sweep derp", Primacy: 1,
				})
				prog.DefaultDERControlLink = &sep2.Link{Href: coreder.DefaultHref(progHref)}
				if err := s.DERPrograms.Create(ctx, dev, "1", prog); err != nil {
					return err
				}
				now := sep2time.Now().Unix()
				ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1}}}
				ctrl.MRID = "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FD3"
				ctrl.Href = progHref + "/derc/1"
				ctrl.CreationTime = now - 200
				ctrl.Interval = &sep2.DateTimeInterval{Start: now - 100, Duration: 3600}
				return s.DERControls.Create(ctx, derhref.ScopeKey(dev, "1", "1"), "1", ctrl)
			},
		},
		{
			method: http.MethodGet, path: "/msg/1", element: "MessagingProgram",
			direct: func(ctx context.Context, s *server.Stores, _ string) error {
				mp := sep2.MessagingProgram{
					IdentifiedObject: sep2.IdentifiedObject{MRID: "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0FCC", Description: "wadl sweep msg"}, Primacy: 1,
					TextMessageListLink: &sep2.ListLink{Href: "/msg/1/tm"},
				}
				mp.Href = "/msg/1"
				return s.MessagingPrograms.Create(ctx, "1", mp)
			},
		},
	}
}

// phaseSeed returns the hrefs the seeds minted, for resources no advertised
// link reaches (a Subscription is listed under no EndDevice link).
//
// It runs seedSet in order. A seed that names the device before the
// device seed produced an id is recorded as failed, never sent with the token
// unresolved.
func (t *Target) phaseSeed(rows *[]Row, ri *routeIndex) []string {
	dev := ""
	var minted []string
	for _, s := range seedSet() {
		path := s.path
		var r response
		var directErr error
		unresolved := strings.Contains(path, devToken) && dev == ""
		path = strings.ReplaceAll(path, devToken, dev)
		body := strings.ReplaceAll(s.body, devToken, dev)

		switch {
		case unresolved:
			directErr = errors.New("the EndDevice seed produced no device id")
		case s.direct != nil && t.Stores == nil:
			directErr = errors.New("the target carries no stores to seed")
		case s.direct != nil:
			// A direct seed has no HTTP response; status 200 is recorded only
			// when it completes, so a failure is not read as an accepted POST.
			directErr = s.direct(context.Background(), t.Stores, dev)
			if directErr == nil {
				r.status = http.StatusOK
			}
		default:
			client := t.Client
			if s.manager && t.ManagerClient != nil {
				client = t.ManagerClient
			}
			r = t.doWith(client, s.method, path, []byte(body))
		}

		v := Verdict(fmt.Sprintf("%s_%d", VerdictSeedFailed, r.status))
		switch {
		case directErr == nil && (r.status == http.StatusOK || r.status == http.StatusCreated):
			v = VerdictSeeded
		case directErr != nil && s.direct != nil:
			v = VerdictSeedFailedDirect
		}
		row := Row{
			Phase:           PhaseSeed,
			Path:            path,
			Method:          s.method,
			Element:         s.element,
			Status:          r.status,
			ContentType:     r.contentType,
			Location:        r.location,
			RootElement:     rootElement(r.body),
			BodyBytes:       len(r.body),
			Verdict:         v,
			TransportError:  r.transportError,
			MountedByRouter: ri.mounted(s.method, path),
		}
		if directErr != nil {
			row.TransportError = directErr.Error()
		}
		*rows = append(*rows, row)
		if m := devicePath.FindStringSubmatch(r.location); m != nil && dev == "" {
			dev = m[1]
		}
		if r.location != "" {
			t.followLocation(rows, ri, r.location, s.method+" "+path, path)
			minted = append(minted, schemeAuthority.ReplaceAllString(r.location, ""))
		}
	}
	return minted
}

const maxDiscoveryURLs = 600

// crawlRoot is the advertisedBy of the first link, which no resource advertises.
const crawlRoot = "(root)"

// managerCrawlRoot is the advertisedBy of the second crawl start, which only the
// manager certificate can follow.
const managerCrawlRoot = "(manager root)"

// managerCrawlPrefix bounds the manager crawl: its links are followed as the
// manager only while they stay under it.
const managerCrawlPrefix = "/upt"

func (t *Target) phaseDiscovery(rows *[]Row, ri *routeIndex, bodies map[string][]byte) ([]string, *crawl) {
	seen := map[string]bool{}
	queue := []link{{href: "/dcap", expectedRoot: "DeviceCapability", advertisedBy: crawlRoot}}
	if t.ManagerClient != nil {
		queue = append(queue, link{href: managerCrawlPrefix, expectedRoot: "UsagePointList", advertisedBy: managerCrawlRoot, manager: true})
	}
	var discovered []string
	reached := newCrawl()

	for len(queue) > 0 && len(seen) < maxDiscoveryURLs {
		l := queue[0]
		queue = queue[1:]
		seenKey := l.href
		client := t.Client
		if l.manager {
			seenKey += " (manager)"
			client = t.ManagerClient
		}
		if seen[seenKey] {
			continue
		}
		seen[seenKey] = true

		r := t.doWith(client, http.MethodGet, l.href, nil)
		root := rootElement(r.body)

		v := VerdictConformant
		switch {
		case r.transportError != "":
			v = VerdictTransportError
		case isUnrouted(r):
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
			reached.add(l.resourceID(), l.href)
			reached.add(rootTypeName(r.body), l.href)
			for _, nxt := range parseLinks(r.body, l.href) {
				nxt.manager = l.manager && strings.HasPrefix(nxt.href, managerCrawlPrefix)
				key := nxt.href
				if nxt.manager {
					key += " (manager)"
				}
				if !seen[key] && strings.HasPrefix(nxt.href, "/") {
					queue = append(queue, nxt)
				}
			}
		}
	}
	return discovered, reached
}

func (t *Target) phaseWADL(m *Model, discovered []string, reached *crawl, rows *[]Row, ri *routeIndex) (reachedResources int) {
	// GET before HEAD before the mutating methods. GET-first is load bearing:
	// HEAD's 404 classification borrows the body evidence only a GET supplies.
	// Mutating methods run last so discovery-derived ids are still valid when
	// the read phases use them.
	// Writes run PUT, POST, then DELETE, and DELETE runs deepest path first. A
	// DELETE that ran before a PUT on the same resource turned the PUT's 404
	// into a harness artifact, and a parent DELETE before a child DELETE did
	// the same to the child.
	order := map[string]int{
		http.MethodGet: 0, http.MethodHead: 1, http.MethodPut: 2, http.MethodPost: 3, http.MethodDelete: 4,
	}
	ordered := make([]Method, len(m.Methods))
	copy(ordered, m.Methods)
	sort.SliceStable(ordered, func(i, j int) bool {
		oi, ok := order[ordered[i].Verb]
		if !ok {
			oi = 5
		}
		oj, ok := order[ordered[j].Verb]
		if !ok {
			oj = 5
		}
		if oi != oj {
			return oi < oj
		}
		if oi == order[http.MethodDelete] {
			return strings.Count(ordered[i].Path, "/") > strings.Count(ordered[j].Path, "/")
		}
		return false
	})

	// A Mandatory or Optional method is scored where the crawl found its resource.
	// A resource the crawl missed is reported not_reached and not probed, unless no
	// schema link names it, which leaves its sample path the only way to find it.
	states, notReached := resourceStates(m, reached, t.Links)
	*rows = append(*rows, notReached...)
	for _, st := range states {
		if st == stateReached {
			reachedResources++
		}
	}

	muxByPath := map[string]bool{}
	for _, meth := range ordered {
		path, idSource := concretize(meth.Path, discovered)
		if requirable(meth.Mode) {
			switch states[keyOf(meth)] {
			case stateNotReached:
				continue
			case stateReached:
				path, _ = reached.href(meth.ResourceID)
				idSource = "crawled"
			case stateSamplePath:
				idSource = "sample_path"
			}
		}

		var body []byte
		if (meth.Verb == http.MethodPut || meth.Verb == http.MethodPost) && len(meth.RequestElements) > 0 {
			body = []byte(`<` + meth.RequestElements[0] + ` xmlns="` + sepNS + `"/>`)
		}

		client := t.Client
		asManager := false
		if t.ManagerClient != nil && restrictedWrite(meth) {
			path = t.pairedPath(path)
			// The device goes first: a manager DELETE that succeeded would
			// leave the device a 404 that says nothing about authority.
			t.probeDeviceOnRestricted(rows, meth, path, body)
			client, asManager = t.ManagerClient, true
		}
		r := t.doWith(client, meth.Verb, path, body)
		root := rootElement(r.body)
		if meth.Verb == http.MethodGet && (r.status == http.StatusNotFound || isRouteRefusal(r)) {
			muxByPath[path] = isUnrouted(r)
		}
		v := classify(meth, path, r, root, muxByPath)

		*rows = append(*rows, Row{
			Phase:            PhaseWADL,
			ResourceID:       meth.ResourceID,
			WADLPath:         meth.Path,
			Path:             path,
			IDSource:         idSource,
			AsManager:        asManager,
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
	return reachedResources
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
	case isUnrouted(r):
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

	minted := t.phaseSeed(&res.Rows, ri)

	bodies := map[string][]byte{}
	discovered, reached := t.phaseDiscovery(&res.Rows, ri, bodies)
	for _, h := range minted {
		if !containsStr(discovered, h) {
			discovered = append(discovered, h)
		}
	}

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

	res.CrawlReached = t.phaseWADL(m, discovered, reached, &res.Rows, ri)
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

// NotReachedMethods counts the model's methods of one mode that belong to a
// resource the crawl did not reach. Those methods are not probed, so no
// per-method row carries them.
func (r *Results) NotReachedMethods(m *Model, mode Mode) int {
	return len(r.notReachedMethodList(m, mode))
}

// notReachedMethodList is the one home of the not-reached selection: the model's
// methods of one mode whose resource the crawl missed, in model order.
func (r *Results) notReachedMethodList(m *Model, mode Mode) []Method {
	missed := map[resKey]bool{}
	for _, row := range r.NotReachedRows() {
		missed[resKey{row.ResourceID, row.WADLPath}] = true
	}
	var out []Method
	for _, meth := range m.Methods {
		if meth.Mode == mode && missed[keyOf(meth)] {
			out = append(out, meth)
		}
	}
	return out
}

// NotReachedRows is the one home of the not_reached row selection: the WADL-phase
// rows of resources the crawl missed.
func (r *Results) NotReachedRows() []Row {
	return r.Filter(func(row Row) bool {
		return row.Phase == PhaseWADL && row.Verdict == VerdictNotReached
	})
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

// edevSegment matches the leading /edev/{id} of a path.
var edevSegment = regexp.MustCompile(`^/edev/[^/]+`)

// pairedPath points a path under /edev/{id} at the manager's own device, so the
// manager's write and the ordinary device's probe of it name the same record
// and the manager is not refused for acting on a device it does not manage.
func (t *Target) pairedPath(path string) string {
	if t.ManagedDeviceID == "" {
		return path
	}
	return edevSegment.ReplaceAllLiteralString(path, "/edev/"+t.ManagedDeviceID)
}
