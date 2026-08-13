package assembly_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A wired family with an unwired member must REFUSE, not panic.
//
// The mount gates read one field per FAMILY, not one field per route: fifteen
// handles have no gate of their own and are mounted on the strength of a
// sibling. Nothing in this package forced those siblings to arrive together, so
// a Stores carrying EndDevices and DERs but no DERPrograms mounted
// GET /edev/{id}/fsa/{fsaId}/derp over a nil handle, and the first request to
// it dereferenced that nil inside net/http's per-request recover: to the client
// an EOF, to the operator a stack trace and no diagnosis.
//
// The routes stay mounted, deliberately. The links that point at them are
// minted by handler packages that consult no store at all
// (handlers/fsa.HandleFSA mints DERProgramListLink, handlers/metering mints
// MeterReadingListLink, handlers/response mints ResponseListLink), and
// MintableHrefs in hrefs.go declares each of those minters as the source of the
// route. Unmounting the route while the minter keeps minting would trade a
// panic for the advertised-but-unrouted defect that ratchet exists to abolish.
// So the router keeps the route and answers a diagnosed 500: the deployment is
// mis-wired, that is a server fault, and it says so once at assembly and again
// on every request rather than dying silently.
//
// Every case below assigns the ZERO VALUE OF THE CONCRETE TYPE, which is what a
// consumer that leaves a field out of its store constructor actually produces.
// A nil interface literal was never the failing shape.
func TestPartialFamilyRefusesRatherThanPanicking(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field  string
		clear  func(*assembly.Stores)
		method string
		path   string
	}{
		{"DERCapabilities", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DERCapability]
			s.DERCapabilities = h
		}, http.MethodGet, "/edev/1/der/1/dercap"},

		{"DERSettings", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DERSettings]
			s.DERSettings = h
		}, http.MethodGet, "/edev/1/der/1/derg"},

		{"DERStatuses", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DERStatus]
			s.DERStatuses = h
		}, http.MethodGet, "/edev/1/der/1/ders"},

		{"DERAvailabilities", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DERAvailability]
			s.DERAvailabilities = h
		}, http.MethodGet, "/edev/1/der/1/dera"},

		// Pike's reproduction, verbatim: EndDevices and DERs wired,
		// DERPrograms unset, drive the route the FSA advertises.
		{"DERPrograms", func(s *assembly.Stores) {
			var h *memory.DERProgramStore
			s.DERPrograms = h
		}, http.MethodGet, "/edev/1/fsa/1/derp"},

		{"DERControls", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DERControl]
			s.DERControls = h
		}, http.MethodGet, "/edev/1/fsa/1/derp/1/derc"},

		{"DefaultDERControls", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.DefaultDERControl]
			s.DefaultDERControls = h
		}, http.MethodGet, "/edev/1/fsa/1/derp/1/dderc"},

		{"DERCurves", func(s *assembly.Stores) {
			var h *memory.Store[sep2.DERCurve]
			s.DERCurves = h
		}, http.MethodGet, "/dc"},

		{"MeterReadings", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.MeterReading]
			s.MeterReadings = h
		}, http.MethodGet, "/upt/1/mr"},

		{"Readings", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.Reading]
			s.Readings = h
		}, http.MethodGet, "/upt/1/mr/1/r"},

		{"ReadingTypes", func(s *assembly.Stores) {
			var h *memory.Store[sep2.ReadingType]
			s.ReadingTypes = h
		}, http.MethodGet, "/rt"},

		{"TextMessages", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.TextMessage]
			s.TextMessages = h
		}, http.MethodGet, "/msg/1/tm"},

		{"FlowReservationResponses", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.FlowReservationResponse]
			s.FlowReservationResponses = h
		}, http.MethodGet, "/edev/1/frp"},

		{"Responses", func(s *assembly.Stores) {
			var h *memory.ScopedStore[sep2.Response]
			s.Responses = h
		}, http.MethodGet, "/rsps/1/rsp"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()

			stores := testStores()
			tc.clear(stores)

			handler, patterns := assembly.BuildProtocolRouter(
				assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
			)

			// The route is still mounted: the family gate is the family's
			// gate, and the link that points here is minted whether or not
			// this handle arrived.
			pattern := tc.method + " " + tc.path
			if !slices.ContainsFunc(patterns, func(p string) bool {
				return strings.HasPrefix(p, tc.method+" ") && patternMatchesProbe(p, pattern)
			}) {
				t.Fatalf("no mounted pattern matches %q; the family gate must not unmount a route whose link is still minted", pattern)
			}

			srv := httptest.NewServer(handler)
			defer srv.Close()

			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				// A nil dereference inside the handler is recovered by
				// net/http, which closes the connection: the client sees a
				// transport error rather than a status code. That is the
				// failure this test exists to catch.
				t.Fatalf("%s %s over an unwired Stores.%s: %v (a panicking handler, not a refusal)",
					tc.method, tc.path, tc.field, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("%s %s over an unwired Stores.%s = %d, want 500: a mis-wired server must say so, not answer as though the resource is simply absent",
					tc.method, tc.path, tc.field, resp.StatusCode)
			}
		})
	}
}

// patternMatchesProbe reports whether the mounted pattern would match the probe
// pattern, treating every {wildcard} in the mount as matching the single
// segment in the same position.
func patternMatchesProbe(mounted, probe string) bool {
	mMethod, mPath, ok := strings.Cut(mounted, " ")
	if !ok {
		return false
	}
	pMethod, pPath, ok := strings.Cut(probe, " ")
	if !ok || mMethod != pMethod {
		return false
	}
	mSegs := strings.Split(strings.TrimPrefix(mPath, "/"), "/")
	pSegs := strings.Split(strings.TrimPrefix(pPath, "/"), "/")
	if len(mSegs) != len(pSegs) {
		return false
	}
	for i, seg := range mSegs {
		if strings.HasPrefix(seg, "{") {
			continue
		}
		if seg != pSegs[i] {
			return false
		}
	}
	return true
}

// MirrorMeterReadings is co-gated by MirrorUsagePoints, and its only route is a
// POST behind the section 10.11.3 rule (e) ownership gate, so the probe has to
// create the MirrorUsagePoint it posts under: an unowned parent is refused
// before any reading store is touched, which would prove nothing about the
// unwired handle.
func TestPartialMirrorFamilyRefusesRatherThanPanicking(t *testing.T) {
	t.Parallel()

	stores := testStores()
	var noReadings *memory.ScopedStore[sep2.MirrorMeterReading]
	stores.MirrorMeterReadings = noReadings

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	created, err := http.Post(srv.URL+"/mup", "application/sep+xml", strings.NewReader(mirrorUsagePointDoc))
	if err != nil {
		t.Fatalf("POST /mup: %v", err)
	}
	created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("POST /mup = %d, want 201", created.StatusCode)
	}
	location := created.Header.Get("Location")
	if location == "" {
		t.Fatal("POST /mup returned no Location header")
	}

	resp, err := http.Post(srv.URL+location+"/mr", "application/sep+xml", strings.NewReader(mirrorMeterReadingDoc))
	if err != nil {
		t.Fatalf("POST %s/mr over an unwired Stores.MirrorMeterReadings: %v (a panicking handler, not a refusal)", location, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST %s/mr over an unwired Stores.MirrorMeterReadings = %d, want 500", location, resp.StatusCode)
	}
}
