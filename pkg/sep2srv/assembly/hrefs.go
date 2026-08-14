package assembly

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
)

// Mintable-href routing assertion.
//
// # The defect class
//
// A handler stamps an Href on a resource, hands it back in a Location header
// or a Link, and nothing routes there. A client that follows OUR OWN advertised
// URI gets 404 or 405. This is worse than an unimplemented endpoint: the server
// asserted the resource exists at that URI, so a conforming client treats the
// failure as a server fault and, in the EPRI reference client's case, tears
// down the event or the mirror it was mid-way through establishing.
//
// The class does not stay put. A hand-maintained list of instances was wrong
// about three of five entries; an audit turned up a sixth; and one minor
// version bump ADDED a seventh by minting a Location at a shape nothing served.
// That is why this file is a check rather than a longer list: the list is the
// thing being replaced.
//
// # Why this lives here, and where it is going
//
// This assertion belongs with whatever owns the router. Under the four-layer
// target architecture (core is the shared client-and-server library, server-go
// is the server, the bridge grafts onto the server) the router is server-owned,
// so this file moves to server-go with [BuildProtocolRouter] rather than
// staying in core. It is here today only because the router is here today. Do
// not grow dependencies on it from the client half of core: see pkg/store's
// package documentation for the same note about the store contract, and the
// bridge/core boundary analysis for the sequencing.
//
// # Boot-time, and why not only test-time
//
// [AssertMintableHrefs] is written to be called once at server startup with the
// pattern list [BuildProtocolRouter] returned, and to fail startup loudly when
// a mintable href does not resolve. A test-time check would catch a defect
// earlier and cannot slow startup, and this package ships one as well
// (hrefs_test.go runs the same assertion against a fully wired router, so CI
// fails on a regression before anything is deployed).
//
// The boot-time call is still worth making, because the two catch different
// failures. Route registration in [BuildProtocolRouter] is conditional on which
// stores a consumer wired: with a nil FSA store the /edev/{id}/fsa route is
// never registered, while enddevice still stamps FunctionSetAssignmentsListLink
// on every EndDevice it serves. That is a DEPLOYMENT-shaped defect, invisible
// to any test that builds a fully populated router, and only a check that runs
// against the router the process actually built can see it. The startup cost is
// one synthetic request per declared shape against an in-memory mux, which is
// tens of microseconds in total.
//
// A consumer that deliberately serves a subset of the function sets should call
// [CheckMintableHrefs] and triage the report instead of calling
// [AssertMintableHrefs], because for that consumer "advertised but unrouted" is
// a decision rather than a bug.
//
// # The ratchet
//
// The instances that exist today do not break startup: they are listed in
// knownUnroutedHrefs, which is an explicit, greppable set rather than a
// threshold count. [AssertMintableHrefs] fails on anything NOT in that set, so
// a NEW instance is a hard startup failure naming the href and the code that
// mints it. Entries leave the set as instances are fixed, and
// TestKnownUnroutedHrefs_RatchetIsExact fails when a listed entry starts
// routing, so the set cannot silently stop shrinking. Deliberately, a fixed
// entry fails the TEST rather than startup: a deployment should not break
// because somebody repaired a route.

// HrefWildcard marks a segment of a [MintableHref] Shape whose value is chosen
// at request time. The minting site's own name for that value (the fmt verb, or
// the local variable) is not the router's name for it, so neither name is used:
// a shape says only that a segment varies.
const HrefWildcard = "{}"

// hrefProbeSegment is substituted for each [HrefWildcard] when a shape is
// probed against the router. Any single path segment would do; nothing in the
// pattern set is a literal segment with this value.
const hrefProbeSegment = "probe"

// MintableHref is one href SHAPE the server can put on the wire, together with
// the method a client uses to follow it.
//
// A shape is not a URL: /edev/{}/frq/{} stands for every href of that form the
// server can mint. Checking shapes rather than instances is what makes the
// check total over the code rather than over whatever happened to be created
// during a test run.
type MintableHref struct {
	// Shape is the href with each request-time segment replaced by
	// [HrefWildcard], for example "/edev/{}/frq/{}".
	Shape string

	// Method is the request method a client uses when it follows this href.
	// It is GET for the overwhelming majority: a client re-fetches the
	// resource whose URI we just handed it. It is not always GET, and the
	// difference is load-bearing, because a shape that routes for one method
	// and not another produces a 405 rather than a 404 and the two need
	// telling apart.
	Method string

	// Mint names the code that constructs this shape, package-qualified and
	// without a line number so it does not rot on every edit. It is reported
	// verbatim in the startup failure, because "which href" without "minted
	// where" leaves the reader grepping.
	Mint string

	// Why records what makes a client follow the href, so a reader can judge
	// the consequence of it not resolving rather than only the fact.
	Why string
}

// key is the ratchet key and the stable identity of a shape-plus-method.
func (m MintableHref) key() string { return m.Method + " " + m.Shape }

// probePath renders the shape as a concrete path suitable for a request.
func (m MintableHref) probePath() string {
	return strings.ReplaceAll(m.Shape, HrefWildcard, hrefProbeSegment)
}

// UnroutedReason distinguishes the two ways a mintable href fails to resolve.
// They are different defects: no route at all means the shape was never
// implemented, while a refused method means the resource exists but cannot be
// reached the way a client following a Location header would reach it.
type UnroutedReason string

const (
	// ReasonNoRoute means no pattern matches the path at all (404).
	ReasonNoRoute UnroutedReason = "no route"
	// ReasonMethodRefused means a pattern matches the path but not for this
	// method (405).
	ReasonMethodRefused UnroutedReason = "method refused"
)

// UnroutedHref is one mintable shape the router does not serve.
type UnroutedHref struct {
	Href   MintableHref
	Reason UnroutedReason
	// Probe is the concrete path that was requested, kept so a report is
	// reproducible with curl rather than only describable.
	Probe string
}

func (u UnroutedHref) String() string {
	return fmt.Sprintf("%s %s: %s (minted by %s; %s)",
		u.Href.Method, u.Href.Shape, u.Reason, u.Href.Mint, u.Href.Why)
}

// MintableHrefs returns every href shape this server can put on the wire.
//
// It is a declared registry rather than something derived from the handlers,
// because the handlers build hrefs with fmt.Sprintf and string concatenation
// and there is no seam to derive from. A declared registry has an obvious
// failure mode: someone adds a handler, mints a shape, and does not declare it,
// which is exactly how this defect class grew. That hole is closed by
// TestMintableHrefs_RegistryCoversEverySourceLiteral, which parses this
// package and the handler packages and fails when a source literal folds to an
// href shape that is not declared here. The registry is the input to the boot
// check; the source scan is what keeps the registry honest.
//
// Ordering is registration order, which groups by function set. Callers that
// need determinism across edits should sort.
func MintableHrefs() []MintableHref {
	return []MintableHref{
		// Top-level resources. DeviceCapability links to all of these, and
		// links them unconditionally: it does not consult which stores were
		// wired. A consumer that serves a subset of the function sets is
		// therefore advertising URIs it does not serve, which is why
		// AssertMintableHrefs is documented as the fully-wired call.
		{"/dcap", http.MethodGet, "handlers/dcap.HandleDeviceCapability", "the DeviceCapability self href, and the entry point every client polls"},
		{"/tm", http.MethodGet, "handlers/dcap.HandleDeviceCapability TimeLink", "clients sync clocks against it before acting on any scheduled event"},
		{"/sdev", http.MethodGet, "handlers/dcap.HandleDeviceCapability SelfDeviceLink", "the server's own EndDevice representation"},
		{"/sdev/sdi", http.MethodGet, "handlers/sdev.HandleSelfDevice DeviceInformationLink", "server DeviceInformation"},
		{"/edev", http.MethodGet, "handlers/dcap.HandleDeviceCapability EndDeviceListLink", "clients enumerate and register EndDevices here"},
		{"/mup", http.MethodGet, "handlers/dcap.HandleDeviceCapability MirrorUsagePointListLink", "metering clients POST mirrors here"},
		{"/upt", http.MethodGet, "handlers/dcap.HandleDeviceCapability UsagePointListLink", "server-side metering"},
		{"/dc", http.MethodGet, "handlers/dcap.HandleDeviceCapability DERProgramListLink", "global DERCurve list"},
		{"/msg", http.MethodGet, "handlers/dcap.HandleDeviceCapability MessagingProgramListLink", "messaging programs"},
		{"/rsps", http.MethodGet, "handlers/dcap.HandleDeviceCapability ResponseSetListLink", "response sets"},
		{"/rt", http.MethodGet, "assembly.topLevelMounts", "ReadingType list, mounted on the top-level mux"},

		// EndDevice and its immediate children.
		{"/edev/{}", http.MethodGet, "handlers/enddevice.HandleCreateEndDevice", "the Location header a client follows after POST /edev, and the self href of every EndDevice in the list"},
		// The source of this href moved. It used to be minted unconditionally
		// by the POST handler while nothing ever wrote the Registration
		// behind it, which this registry could not detect: the probe asks
		// whether the href resolves to a mounted route, and "/edev/{}/rg"
		// always did. It is now minted by the store binding that writes the
		// record, so the link and the record appear together. That is the
		// population half of the class, and it is outside what this
		// registry can prove.
		{"/edev/{}/rg", http.MethodGet, "memory.RegisteredEndDeviceStore RegistrationLink", "a client reads its own Registration to confirm the server accepted it"},
		{"/edev/{}/fsa", http.MethodGet, "handlers/enddevice.buildEndDevice FunctionSetAssignmentsListLink", "the path from an EndDevice to its DERPrograms"},
		{"/edev/{}/fsa/{}", http.MethodGet, "handlers/fsa.HandleFSA", "a single FunctionSetAssignments"},
		{"/edev/{}/fsa/{}/derp", http.MethodGet, "handlers/fsa.HandleFSA DERProgramListLink", "the DERProgram list under an FSA"},
		{"/edev/{}/fsa/{}/derp/{}", http.MethodGet, "handlers/der.DERProgramHref", "a single DERProgram member's self href"},
		// The source of this href moved too, for the same reason as the
		// Registration link above: it is minted by the store binding that
		// decides whether the function set is served, not by a handler that
		// stamps it unconditionally. Before that, nothing in the tree
		// assigned LogEventListLink at all, so the list was served at an
		// address no client could learn.
		{"/edev/{}/lel", http.MethodGet, "memory.LogEventLinkedEndDeviceStore LogEventListLink", "CSIP V1.2 BASIC-027 step 2 has the client find LogEventListLink on its EndDevice and read the list behind it"},
		{"/edev/{}/lel", http.MethodPost, "memory.LogEventLinkedEndDeviceStore LogEventListLink", "a device reports an alarm by POSTing a LogEvent to the list its EndDevice advertises (sep_wadl.xml:1385, mode M)"},
		{"/edev/{}/cfg", http.MethodGet, "handlers/configuration.HandleConfiguration", "Configuration self href"},
		{"/edev/{}/dstat", http.MethodGet, "assembly.registerNewFunctionSetRoutes DeviceStatus", "DeviceStatus self href"},
		{"/edev/{}/ps", http.MethodGet, "handlers/power_status.HandlePowerStatus", "PowerStatus self href"},

		// DER, addressed under the EndDevice.
		{"/edev/{}/der/{}", http.MethodGet, "handlers/der.StampDERInstance", "the self href every DERList member carries; a client walks the list and follows it to reach the DER's sub-resources"},
		{"/edev/{}/der/{}", http.MethodPut, "handlers/der.StampDERInstance", "IEEE 2030.5 declares PUT on the DER instance mode O (sep_wadl.xml:4116) and SunSpec CTP CORE-014 and CORE-016 exercise it on the certified path"},
		{"/edev/{}/der/{}/dercap", http.MethodGet, "handlers/der.DERSingletonHandlers", "DERCapability self href"},
		{"/edev/{}/der/{}/derg", http.MethodGet, "handlers/der.DERSingletonHandlers", "DERSettings self href"},
		{"/edev/{}/der/{}/ders", http.MethodGet, "handlers/der.DERSingletonHandlers", "DERStatus self href"},
		{"/edev/{}/der/{}/dera", http.MethodGet, "handlers/der.DERSingletonHandlers", "DERAvailability self href"},
		{"/edev/{}/fsa/{}/derp/{}/dderc", http.MethodGet, "handlers/der.DefaultDERControlHandler", "DefaultDERControl self href"},

		// Resources created by a client POST. Every one of these is handed
		// back in a Location header, which is the strongest form of the
		// advertisement: the server is telling the client where the thing it
		// just created now lives.
		{"/edev/{}/sub/{}", http.MethodGet, "handlers/subscription.HandleCreateSubscription", "the Location header returned by POST /edev/{}/sub; a client re-reads its subscription to confirm what the server stored"},
		{"/edev/{}/sub/{}", http.MethodDelete, "handlers/subscription.HandleCreateSubscription", "the same Location header, used to cancel the subscription"},
		{"/edev/{}/lel/{}", http.MethodGet, "handlers/logevent.HandlePostLogEvent", "the Location header returned by POST /edev/{}/lel"},
		{"/edev/{}/lel/{}", http.MethodDelete, "handlers/logevent.HandlePostLogEvent", "the same Location header, used to remove the event; DELETE on the LogEvent is Mandatory (sep_wadl.xml:1430)"},
		{"/edev/{}/frq/{}", http.MethodGet, "handlers/flow_reservation.HandlePostFlowReservationRequest", "the Location header returned by POST /edev/{}/frq"},
		{"/edev/{}/frp/{}", http.MethodGet, "handlers/flow_reservation.HandlePostFlowReservationRequest", "the FlowReservationResponse href stamped alongside the request, which the client polls for the server's decision"},
		{"/msg/{}/tm/{}", http.MethodGet, "handlers/messaging.HandlePostTextMessage", "the Location header returned by POST /msg/{}/tm"},
		{"/rsps/{}/rsp/{}", http.MethodGet, "handlers/response.MemberHref", "the Location header returned by POST /rsps/{}/rsp, and the Response's own self href"},

		// The Response function set. The POST entry is not a follow of a
		// Location header like the block above: it is the follow of an EVENT's
		// replyTo, which handlers/der.StampResponseRequest puts on every
		// DERControl this server serves. A replyTo that does
		// not accept a POST is the same defect one function set over: the
		// client would have been told to acknowledge an event at a URI that
		// refuses the acknowledgement.
		{"/rsps/{}", http.MethodGet, "handlers/response.SetHref", "the ResponseSet self href, and the member href of every set in GET /rsps"},
		{"/rsps/{}/rsp", http.MethodGet, "handlers/response.ListHref", "ResponseSet.ResponseListLink: a client reads back the responses the server recorded"},
		{"/rsps/{}/rsp", http.MethodPost, "handlers/response.ListHref", "the replyTo stamped on every served DERControl; a conforming client POSTs its Response here"},

		// Metering mirrors. POST /mup/{} is the EPRI reference client's
		// literal reading of section 10.11.3 rule (d): it posts the follow-up
		// MirrorMeterReading to the Location it was handed, not to a /mr
		// child. Both the GET and the POST are therefore mintable follows of
		// one href.
		{"/mup/{}", http.MethodGet, "handlers/metering.MirrorHref", "the Location header returned by POST /mup"},
		{"/mup/{}", http.MethodPost, "handlers/metering.MirrorHref", "IEEE 2030.5-2018 section 10.11.3 rule (d): the client posts readings to the resource identified in the server's response"},
		{"/mup/{}", http.MethodPut, "handlers/metering.MirrorHref", "PUT on the MirrorUsagePoint is mode M (sep_wadl.xml:2303): a client updates the mirror at the Location POST /mup handed it, and rule (a)(4) has the new data written over the existing record"},
		{"/mup/{}", http.MethodDelete, "handlers/metering.MirrorHref", "DELETE on the MirrorUsagePoint is mode M (sep_wadl.xml:2323): a client retires the mirror at the Location POST /mup handed it, and is served the stripped record back (sep_wadl.xml:2325)"},
		{"/mup/{}/mr/{}", http.MethodGet, "handlers/metering.stampMirrorMeterReading", "the Location header returned by POST /mup/{}/mr and by POST /mup/{}"},
		{"/upt/{}", http.MethodGet, "handlers/metering.UsagePointHref", "the Location header returned by POST /upt"},
		{"/upt/{}/mr", http.MethodGet, "handlers/metering.HandleCreateUsagePoint MeterReadingListLink", "the MeterReading list under a UsagePoint"},
	}
}

// knownUnroutedHrefs is the RATCHET.
//
// Every entry is a mintable href that does not resolve today and that
// [AssertMintableHrefs] therefore tolerates. The set is explicit rather than a
// count, so shrinking it is a visible diff and growing it is a deliberate act
// someone has to justify in review. Keys are "METHOD SHAPE", matching
// [MintableHref.key]; values say what the defect is.
//
// Removing an entry is the fix landing. TestKnownUnroutedHrefs_RatchetIsExact
// fails if an entry here starts routing, so the set cannot quietly stop
// shrinking, and it fails if a shape stops routing without being added here.
// Mounting their routes removed three entries: GET /edev/{}/frq/{}, GET
// /edev/{}/frp/{} and GET /msg/{}/tm/{}.
//
// A fourth removal, GET /edev/{}/log/{}, is worth being precise about,
// because an entry can leave this set for two very different reasons and
// only one of them is a fix. It did NOT leave because its mint site vanished
// and took the shape with it: the resource the Location named is now
// SERVED, at /edev/{}/lel/{}, which is the address the WADL declares
// (sep_wadl.xml:1404) and which the registry above now carries for both GET
// and DELETE. The old shape is gone from the registry only because the POST
// handler stamps the declared address instead of the undeclared one; the
// reachability the entry recorded as missing is present. The two entries
// that remain still need real fixes, and core has no public issue
// tracker to anchor a per-entry owner tag to. What keeps this set a
// ratchet rather than a bare suppression is not an owner tag but the
// non-empty, specific reason every entry carries:
// TestKnownUnroutedHrefs_EntriesAreDeclaredAndReasoned in hrefs_test.go
// rejects an empty one, and a real reason names the defect precisely
// enough that a reader can tell the entry apart from a shrug.
var knownUnroutedHrefs = map[string]string{
	"GET /edev/{}/sub/{}": "POST /edev/{id}/sub returns this Location but only DELETE is routed, so a client re-reading its own subscription gets 405. The path IS mounted, so this is a method gap rather than a dead link",
	"GET /mup/{}/mr/{}":   "POST /mup/{id}/mr and POST /mup/{id} both return this Location and nothing serves it",
}

// KnownUnroutedHrefs returns a copy of the ratchet set: the mintable hrefs that
// are known not to resolve and that [AssertMintableHrefs] tolerates, keyed
// "METHOD SHAPE" and valued with the defect.
//
// It is exported so a consumer can log the gaps it is shipping with rather than
// having to read this file, and so the tests that enforce the ratchet do not
// need to live inside the package. It returns a copy: the accepted set is not a
// runtime knob, and a consumer that could widen it at startup would have
// defeated the assertion.
func KnownUnroutedHrefs() map[string]string {
	out := make(map[string]string, len(knownUnroutedHrefs))
	for k, v := range knownUnroutedHrefs {
		out[k] = v
	}
	return out
}

// CheckMintableHrefs probes every shape in [MintableHrefs] against patterns and
// reports the ones that do not resolve.
//
// patterns is the list [BuildProtocolRouter] returned for the router this
// process is actually serving. Taking the router's own patterns as a RETURN
// VALUE, rather than reading them back out of a log line or re-deriving them,
// is what makes the check exact: there is no second description of the routes
// that could disagree with the first.
//
// The probe is a real request against a real [http.ServeMux] carrying those
// patterns, mounted behind the same top-level prefixes the production router
// mounts. Reusing the stdlib matcher rather than reimplementing pattern
// matching means the check cannot disagree with the server about what routes;
// mounting the prefixes too means a pattern registered on the protocol mux
// whose family is not mounted on the top mux is correctly reported as
// unreachable, which is the same defect one layer up.
//
// The report is ordered by method then shape so two runs are comparable.
func CheckMintableHrefs(patterns []string) ([]UnroutedHref, error) {
	probe, err := buildProbeRouter(patterns)
	if err != nil {
		return nil, err
	}
	return checkHrefsAgainst(probe, MintableHrefs()), nil
}

// AssertMintableHrefs is the boot-time gate. It returns nil when every mintable
// href either resolves or is a known instance listed in knownUnroutedHrefs, and
// otherwise an error naming each new offender, the code that mints it, and
// whether it is unrouted or method-refused.
//
// Call it at startup with the patterns [BuildProtocolRouter] returned and treat
// a non-nil error as fatal. Serving is the wrong response to a server that has
// been shown to advertise URIs it will not answer: the failure would otherwise
// surface as a client-side teardown in the field, attributed to the client.
//
// It reports NEW instances only. A known instance that has since been fixed is
// a test failure (TestKnownUnroutedHrefs_RatchetIsExact), not a startup
// failure, so repairing a route can never take a deployment down.
func AssertMintableHrefs(patterns []string) error {
	unrouted, err := CheckMintableHrefs(patterns)
	if err != nil {
		return err
	}
	return assertAgainstKnown(unrouted, knownUnroutedHrefs)
}

// assertAgainstKnown is the ratchet comparison, split out so tests can drive it
// with an injected accepted set instead of mutating the package-level one.
func assertAgainstKnown(unrouted []UnroutedHref, known map[string]string) error {
	var novel []UnroutedHref
	for _, u := range unrouted {
		if _, ok := known[u.Href.key()]; !ok {
			novel = append(novel, u)
		}
	}
	if len(novel) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "assembly: %d mintable href shape(s) do not resolve against this router's own patterns.\n", len(novel))
	b.WriteString("A client that follows an href this server advertises would get a 404 or a 405.\n")
	for _, u := range novel {
		fmt.Fprintf(&b, "  - %s\n    probe: %s %s\n", u, u.Href.Method, u.Probe)
	}
	b.WriteString("Route each shape in BuildProtocolRouter, stop minting it, or add it to " +
		"knownUnroutedHrefs in pkg/sep2srv/assembly/hrefs.go with the card that will fix it.")
	return fmt.Errorf("%s", b.String())
}

// checkHrefsAgainst probes each shape and collects the failures.
func checkHrefsAgainst(probe http.Handler, hrefs []MintableHref) []UnroutedHref {
	var out []UnroutedHref
	for _, h := range hrefs {
		path := h.probePath()
		req := httptest.NewRequest(h.Method, path, nil)
		rec := httptest.NewRecorder()
		probe.ServeHTTP(rec, req)

		switch rec.Code {
		case http.StatusNotFound:
			out = append(out, UnroutedHref{Href: h, Reason: ReasonNoRoute, Probe: path})
		case http.StatusMethodNotAllowed:
			out = append(out, UnroutedHref{Href: h, Reason: ReasonMethodRefused, Probe: path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Href.key() < out[j].Href.key() })
	return out
}

// buildProbeRouter reconstructs the production routing topology from a pattern
// list: a protocol mux carrying every pattern, mounted on a top mux under
// topLevelMounts, with a marker handler in place of the real ones.
//
// The marker writes 200 and nothing else. That is the third discriminator
// alongside the stdlib's 404 and 405: a 200 can only have come from a pattern
// matching, because no real handler is wired and nothing else in the chain
// writes a status.
//
// A malformed pattern makes ServeMux panic. That cannot happen for a list this
// mux already registered successfully, but a panic escaping a boot-time
// assertion would make the assertion more dangerous than its absence, so it is
// recovered and returned as an error.
func buildProbeRouter(patterns []string) (h http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			h, err = nil, fmt.Errorf("assembly: building href probe router from %d pattern(s): %v", len(patterns), r)
		}
	}()

	marker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	protocol := http.NewServeMux()
	for _, p := range patterns {
		protocol.Handle(p, marker)
	}

	top := http.NewServeMux()
	for _, prefix := range topLevelMounts {
		top.Handle(prefix, protocol)
	}
	return top, nil
}
