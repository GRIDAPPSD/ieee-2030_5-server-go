package assembly_test

import (
	"context"
	"debug/buildinfo"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// testFixturePIN is an obvious stand-in for a provisioned registration pIN.
// It is not a real secret and exists only so tests can assert that the value
// the policy supplied is the value served. Its digits sum to 20, so it also
// happens to satisfy the 2018 section 6.3.5 check-digit rule; core neither
// generates nor validates pIN values, so that property is a fixture nicety
// rather than anything this package enforces.
const testFixturePIN uint32 = 123455

// testRegistrationPolicy provisions testFixturePIN for every device, which
// is what turns registration on for assembly tests. A Stores with no policy
// provisions nothing and serves no RegistrationLink at all, so tests that
// exercise the registration surface must wire one.
func testRegistrationPolicy() memory.RegistrationPolicy {
	return memory.RegistrationPolicy{
		PIN: func(lfdi string) (uint32, bool) {
			if lfdi == "" {
				return 0, false
			}
			return testFixturePIN, true
		},
	}
}

// testStores builds a fully-populated Stores instance for assembly tests.
// Populated stores cause all optional route-registration branches to fire,
// driving coverage on registerMirrorRoutes, registerMeteringRoutes, and
// registerNewFunctionSetRoutes. Fields that are nil skip the branch;
// we populate all of them to maximise coverage.
func testStores() *assembly.Stores {
	return &assembly.Stores{
		EndDevices:          memory.NewEndDeviceStore(),
		Registrations:       memory.NewRegistrationStore(),
		RegistrationPolicy:  testRegistrationPolicy(),
		MirrorUsagePoints:   memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings: memory.NewScopedStore[sep2.MirrorMeterReading](),

		DERs:               memory.NewScopedStore[sep2.DER](),
		DERCapabilities:    memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:        memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:        memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:  memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:        memory.NewDERProgramStore(),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),

		FSAs:          memory.NewScopedStore[sep2.FunctionSetAssignments](),
		Subscriptions: memory.NewSubscriptionStore(),

		// Server-side metering
		UsagePoints:   memory.NewStore[sep2.UsagePoint](),
		MeterReadings: memory.NewScopedStore[sep2.MeterReading](),
		Readings:      memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:  memory.NewStore[sep2.ReadingType](),

		// New function sets
		Configurations:           memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:           memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:                memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:            memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:        memory.NewStore[sep2.MessagingProgram](),
		TextMessages:             memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),
	}
}

// testAuthPolicy returns a pass-through AuthPolicy suitable for tests:
//   - Wrap is a no-op (no TLS required).
//   - Identity always returns the fixed test identity (ok=true).
//   - SFDIPrefix truncates to 8 chars (mirrors auth.ExtractSFDIPrefix).
const testLFDI = "AABBCCDDEEFF001122334455667788990011223344556677"
const testSFDI = "AABBCCDD11223344"

func testAuthPolicy() assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(h http.Handler) http.Handler { return h },
		Identity: func(_ context.Context) (lfdi, sfdi string, ok bool) {
			return testLFDI, testSFDI, true
		},
		SFDIPrefix: func(sfdi string) (string, error) {
			if len(sfdi) < 8 {
				return "", fmt.Errorf("SFDI too short: %q", sfdi)
			}
			return sfdi[:8], nil
		},
	}
}

// decodeXML unmarshals the response body into dst and fails the test on error.
func decodeXML(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := xml.Unmarshal(body, dst); err != nil {
		t.Fatalf("decode XML (status %d): %v\nbody: %s", resp.StatusCode, err, body)
	}
}

// TestAssembly_DCAPWired asserts that GET /dcap returns a correctly-wired
// DeviceCapability with required links (data-invariants Rule 1: assert field
// values, not just 200).
func TestAssembly_DCAPWired(t *testing.T) {
	t.Parallel()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{TZOffset: -28800},
		testStores(),
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dcap")
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET /dcap: want 200, got %d", resp.StatusCode)
	}

	var dcap sep2.DeviceCapability
	decodeXML(t, resp, &dcap)

	if dcap.Href != "/dcap" {
		t.Errorf("dcap.Href = %q, want %q", dcap.Href, "/dcap")
	}
	if dcap.TimeLink == nil {
		t.Fatal("dcap.TimeLink is nil")
	}
	if dcap.TimeLink.Href != "/tm" {
		t.Errorf("dcap.TimeLink.Href = %q, want %q", dcap.TimeLink.Href, "/tm")
	}
	if dcap.EndDeviceListLink == nil {
		t.Fatal("dcap.EndDeviceListLink is nil")
	}
	if dcap.EndDeviceListLink.Href != "/edev" {
		t.Errorf("dcap.EndDeviceListLink.Href = %q, want %q", dcap.EndDeviceListLink.Href, "/edev")
	}
	if dcap.MirrorUsagePointListLink == nil {
		t.Fatal("dcap.MirrorUsagePointListLink is nil")
	}
}

// deviceCapabilityLinkCase pairs a DeviceCapability link accessor with the
// XML root element the resource behind it must have when the link is
// present.
//
// IEEE 2030.5-2018 section 4.4 permits a Link element to be entirely absent
// when its function set is not implemented, so a nil link is never itself a
// failure here: this table only asserts "IF the link is present, it
// resolves to the resource type its name promises", never "the link must
// be present". That second, presence assertion is deliberately left
// unencoded: it is enablement-policy-dependent (a server MAY legitimately
// decline a function set, per section 4.3 modes), and that policy is a
// separate, config-driven design (in design as of 2026-08-03). Extending
// this test for that design means adding
// a "wantPresent bool" (or an enablement predicate) alongside wantEl on
// each case, so a declined function set asserts absence and an enabled one
// asserts both presence and correct root element, from the SAME table:
// today every case leaves that axis unchecked, matching the fact that
// nothing yet declines a function set at runtime.
type deviceCapabilityLinkCase struct {
	name string
	href func(dc *sep2.DeviceCapability) (href string, present bool)
	// wantEl is the local XML element name the resource at href must have.
	wantEl string
}

func linkHref(get func(dc *sep2.DeviceCapability) *sep2.Link) func(dc *sep2.DeviceCapability) (string, bool) {
	return func(dc *sep2.DeviceCapability) (string, bool) {
		l := get(dc)
		if l == nil {
			return "", false
		}
		return l.Href, true
	}
}

func listLinkHref(get func(dc *sep2.DeviceCapability) *sep2.ListLink) func(dc *sep2.DeviceCapability) (string, bool) {
	return func(dc *sep2.DeviceCapability) (string, bool) {
		l := get(dc)
		if l == nil {
			return "", false
		}
		return l.Href, true
	}
}

var deviceCapabilityLinkCases = []deviceCapabilityLinkCase{
	{"CustomerAccountListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.CustomerAccountListLink }), "CustomerAccountList"},
	{"DemandResponseProgramListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.DemandResponseProgramListLink }), "DemandResponseProgramList"},
	{"DERProgramListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.DERProgramListLink }), "DERProgramList"},
	{"FileListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.FileListLink }), "FileList"},
	{"MessagingProgramListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.MessagingProgramListLink }), "MessagingProgramList"},
	{"PrepaymentListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.PrepaymentListLink }), "PrepaymentList"},
	{"ResponseSetListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.ResponseSetListLink }), "ResponseSetList"},
	{"TariffProfileListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.TariffProfileListLink }), "TariffProfileList"},
	{"TimeLink", linkHref(func(dc *sep2.DeviceCapability) *sep2.Link { return dc.TimeLink }), "Time"},
	{"UsagePointListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.UsagePointListLink }), "UsagePointList"},
	{"EndDeviceListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.EndDeviceListLink }), "EndDeviceList"},
	{"MirrorUsagePointListLink", listLinkHref(func(dc *sep2.DeviceCapability) *sep2.ListLink { return dc.MirrorUsagePointListLink }), "MirrorUsagePointList"},
	{"SelfDeviceLink", linkHref(func(dc *sep2.DeviceCapability) *sep2.Link { return dc.SelfDeviceLink }), "SelfDevice"},
}

// rootElementName decodes just enough of body to read the root element's
// local XML name, without needing a typed struct per resource kind.
func rootElementName(t *testing.T, body []byte) string {
	t.Helper()
	var probe struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &probe); err != nil {
		t.Fatalf("decode root element: %v; body = %s", err, body)
	}
	return probe.XMLName.Local
}

// TestAssembly_DeviceCapabilityLinkContract asserts, for every link
// DeviceCapability can carry, that a present link resolves to a resource
// whose XML root element matches what the link name promises
// (data-invariants Rule 1: assert the actual field/element, not just 200).
// This is the check that catches a defect where a status-code sweep of every
// linked resource stayed green while DERProgramListLink pointed at "/dc"
// and served a DERCurveList instead of a DERProgramList.
func TestAssembly_DeviceCapabilityLinkContract(t *testing.T) {
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		testStores(),
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dcap")
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET /dcap: want 200, got %d", resp.StatusCode)
	}
	var dc sep2.DeviceCapability
	decodeXML(t, resp, &dc)

	for _, tc := range deviceCapabilityLinkCases {
		href, present := tc.href(&dc)
		if !present {
			// Section 4.4: omitting the link is conformant when the
			// function set is not implemented. Nothing to dereference.
			continue
		}

		linkResp, err := http.Get(srv.URL + href)
		if err != nil {
			t.Fatalf("%s: GET %s: %v", tc.name, href, err)
		}
		body, err := io.ReadAll(linkResp.Body)
		_ = linkResp.Body.Close()
		if err != nil {
			t.Fatalf("%s: GET %s: read body: %v", tc.name, href, err)
		}
		if linkResp.StatusCode != http.StatusOK {
			t.Fatalf("%s: GET %s: status = %d, want 200; body = %s", tc.name, href, linkResp.StatusCode, body)
		}

		gotEl := rootElementName(t, body)
		if gotEl != tc.wantEl {
			t.Errorf("%s: GET %s: root element = %q, want %q (link name promises %s but %s serves %s)",
				tc.name, href, gotEl, tc.wantEl, tc.wantEl, href, gotEl)
		}
	}

	// Regression guard: DERProgramListLink must be omitted, not pointed at
	// /dc (which serves DERCurveList, a different resource type). Core has
	// no top-level DERProgramList resource: DERProgram is served only
	// nested under FunctionSetAssignments (GET /edev/{id}/fsa/{fsaId}/derp).
	if dc.DERProgramListLink != nil {
		t.Errorf("dc.DERProgramListLink = %+v, want nil (section 4.4: omit links to function sets with no top-level resource)", dc.DERProgramListLink)
	}
}

// TestAssembly_EndDeviceCreateRoundTrip asserts that POST /edev creates an
// EndDevice and GET /edev returns a list containing it with the test SFDI
// (data-invariants Rule 1: assert stored field values).
func TestAssembly_EndDeviceCreateRoundTrip(t *testing.T) {
	t.Parallel()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		testStores(),
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// POST /edev: create a device. The SEP2 XML namespace is required by the
	// EndDevice struct's XMLName tag; a namespace-less body returns 400.
	postBody := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`
	resp, err := http.Post(srv.URL+"/edev", "application/sep+xml", strings.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("POST /edev: want 201/200, got %d: %s", resp.StatusCode, body)
	}

	var created sep2.EndDevice
	decodeXML(t, resp, &created)
	if created.SFDI != testSFDI {
		t.Errorf("created.SFDI = %q, want %q", created.SFDI, testSFDI)
	}
	if created.LFDI != testLFDI {
		t.Errorf("created.LFDI = %q, want %q", created.LFDI, testLFDI)
	}
	if created.Href == "" {
		t.Error("created.Href is empty")
	}

	// GET /edev: list must contain the created device
	resp2, err := http.Get(srv.URL + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		_ = resp2.Body.Close()
		t.Fatalf("GET /edev: want 200, got %d", resp2.StatusCode)
	}

	var list sep2.EndDeviceList
	decodeXML(t, resp2, &list)

	if list.All < 1 {
		t.Errorf("EndDeviceList.All = %d, want >= 1", list.All)
	}
	found := false
	for _, dev := range list.EndDevice {
		if dev.SFDI == testSFDI {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("EndDeviceList does not contain a device with SFDI %q", testSFDI)
	}
}

// TestAssembly_DERSingletonRoundTrip asserts that PUT then GET on
// /edev/{id}/der/{derId}/dercap returns a DERCapability whose
// RTGMaxW.Value == 10000 (data-invariants Rule 1: exact stored field).
func TestAssembly_DERSingletonRoundTrip(t *testing.T) {
	t.Parallel()
	stores := testStores()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// First create an EndDevice so the edev ID exists in the path.
	// Include the SEP2 namespace: the EndDevice XMLName tag requires it.
	resp, err := http.Post(srv.URL+"/edev", "application/sep+xml",
		strings.NewReader(`<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("POST /edev: want 201/200, got %d: %s", resp.StatusCode, body)
	}
	var edev sep2.EndDevice
	decodeXML(t, resp, &edev)

	// Extract the edev ID from the Href: "/edev/<id>"
	edevID := strings.TrimPrefix(edev.Href, "/edev/")
	if edevID == "" || edevID == edev.Href {
		t.Fatalf("cannot extract edev ID from Href %q", edev.Href)
	}

	const derID = "1"
	dercapURL := fmt.Sprintf("%s/edev/%s/der/%s/dercap", srv.URL, edevID, derID)

	// PUT DERCapability with RTGMaxW.Value = 10000. Include the SEP2 namespace.
	putBody := `<DERCapability xmlns="urn:ieee:std:2030.5:ns"><rtgMaxW><multiplier>0</multiplier><value>10000</value></rtgMaxW></DERCapability>`
	req, _ := http.NewRequest(http.MethodPut, dercapURL, strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/sep+xml")
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", dercapURL, err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT dercap: want 204/200, got %d", putResp.StatusCode)
	}

	// GET DERCapability: assert RTGMaxW.Value == 10000
	getResp, err := http.Get(dercapURL)
	if err != nil {
		t.Fatalf("GET %s: %v", dercapURL, err)
	}
	if getResp.StatusCode != http.StatusOK {
		_ = getResp.Body.Close()
		t.Fatalf("GET dercap: want 200, got %d", getResp.StatusCode)
	}

	var cap sep2.DERCapability
	decodeXML(t, getResp, &cap)

	if cap.RTGMaxW == nil {
		t.Fatal("DERCapability.RTGMaxW is nil after PUT")
	}
	if cap.RTGMaxW.Value != 10000 {
		t.Errorf("DERCapability.RTGMaxW.Value = %d, want 10000", cap.RTGMaxW.Value)
	}
}

// TestAssembly_TimeScalarsFlowThroughRouterConfig asserts that GET /tm returns
// a sep2.Time whose TzOffset matches the RouterConfig.TZOffset passed in
// (data-invariants Rule 1: assert the config field propagated, not just 200).
func TestAssembly_TimeScalarsFlowThroughRouterConfig(t *testing.T) {
	t.Parallel()
	const wantOffset int32 = -28800 // PST: UTC-8 in seconds

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{TZOffset: wantOffset},
		nil, // no stores: static-only routes still work
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tm")
	if err != nil {
		t.Fatalf("GET /tm: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET /tm: want 200, got %d", resp.StatusCode)
	}

	var tm sep2.Time
	decodeXML(t, resp, &tm)

	if tm.TzOffset != wantOffset {
		t.Errorf("Time.TzOffset = %d, want %d", tm.TzOffset, wantOffset)
	}
}

// TestAssembly_ImportCleanGate verifies that the assembly package (and all
// packages it transitively imports) contains no import of
// github.com/GRIDAPPSD/ieee-2030_5-go/internal/... paths.
//
// This is the layering invariant: core must not import server-internal
// packages. The Go toolchain enforces cross-module internal/ at compile time,
// so this test binary linking at all already proves the invariant holds. The
// test adds a runtime assertion via debug/buildinfo that scans the actual
// dependency list in the compiled binary, turning the guarantee from
// "implicit and invisible" to "explicit and tested" (Pike LOW finding).
func TestAssembly_ImportCleanGate(t *testing.T) {
	t.Parallel()

	const forbidden = "github.com/GRIDAPPSD/ieee-2030_5-go/internal"

	// Read the build-info embedded in the current test binary. os.Args[0] is
	// the test binary; debug/buildinfo.ReadFile inspects the Go build metadata
	// section without forking or network access.
	info, err := buildinfo.ReadFile(os.Args[0])
	if err != nil {
		// Reproducible case: -trimpath strips the binary or the binary is
		// a stripped static build without build info. The compile-time
		// enforcement still holds; skip rather than fail so CI is not
		// broken by stripped binaries.
		t.Skipf("debug/buildinfo.ReadFile(%q): %v (skipping runtime check; compile-time enforcement still applies)", os.Args[0], err)
	}

	for _, dep := range info.Deps {
		if strings.HasPrefix(dep.Path, forbidden) {
			t.Errorf("forbidden import in test binary: dep.Path = %q (prefix %q)", dep.Path, forbidden)
		}
	}
}

// notifyRemoverStub satisfies both coreedev.ResourceNotifier and the internal
// notifyRemover interface so TestAssembly_AsNotifyRemoved can exercise the
// asNotifyRemoved type-assertion path in registerEndDeviceRoutes.
// The stub counts Notify calls and records the last NotifyRemoved call.
type notifyRemoverStub struct {
	notifyCalled       int
	notifyRemovedCalls []string // resourceHref values
}

func (s *notifyRemoverStub) Notify(_ context.Context, resourceHref string, _ uint8) {
	s.notifyCalled++
}

func (s *notifyRemoverStub) NotifyRemoved(_ context.Context, sub sep2.Subscription) error {
	s.notifyRemovedCalls = append(s.notifyRemovedCalls, sub.Href)
	return nil
}

// TestAssembly_ScopedListRoutesMounted: exercises the scopedListHandler and
// scopedListHandlerDeep closures by sending HTTP GETs to routes that are
// registered through those helpers (FSA list, DER list, DERControl list).
// These routes are hit at the HTTP level so the closure body is exercised.
func TestAssembly_ScopedListRoutesMounted(t *testing.T) {
	t.Parallel()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// GET /edev/{id}/fsa (scoped by device id)
	resp, err := http.Get(srv.URL + "/edev/e1/fsa")
	if err != nil {
		t.Fatalf("GET /edev/e1/fsa: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /edev/e1/fsa status = %d, want 200", resp.StatusCode)
	}

	// GET /edev/{id}/der (scoped list)
	resp2, err := http.Get(srv.URL + "/edev/e1/der")
	if err != nil {
		t.Fatalf("GET /edev/e1/der: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET /edev/e1/der status = %d, want 200", resp2.StatusCode)
	}

	// GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc (scopedListHandlerDeep)
	resp3, err := http.Get(srv.URL + "/edev/e1/fsa/f1/derp/p1/derc")
	if err != nil {
		t.Fatalf("GET /edev/e1/fsa/f1/derp/p1/derc: %v", err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("GET /edev/e1/fsa/f1/derp/p1/derc status = %d, want 200", resp3.StatusCode)
	}

	// GET /mup (mirror usage point list, exercises registerMirrorRoutes)
	resp4, err := http.Get(srv.URL + "/mup")
	if err != nil {
		t.Fatalf("GET /mup: %v", err)
	}
	_ = resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("GET /mup status = %d, want 200", resp4.StatusCode)
	}

	// GET /upt (usage point list, exercises registerMeteringRoutes)
	resp5, err := http.Get(srv.URL + "/upt")
	if err != nil {
		t.Fatalf("GET /upt: %v", err)
	}
	_ = resp5.Body.Close()
	if resp5.StatusCode != http.StatusOK {
		t.Errorf("GET /upt status = %d, want 200", resp5.StatusCode)
	}
}

// TestAssembly_DERProgramMemberHrefResolves walks the FSA-to-DERProgramList
// discovery chain per CSIP v2.0 s5.2.3.1: GET the FSA-scoped DERProgramList,
// take a member's own href, then GET that href. A DERProgramList member
// must advertise an href that resolves against a mounted route, since that
// link walk is the only path a conforming client has to the control
// surface.
func TestAssembly_DERProgramMemberHrefResolves(t *testing.T) {
	t.Parallel()

	stores := testStores()
	edevID, fsaID, derpID := "e1", "f1", "p1"
	seedOwnedDevices(t, stores.EndDevices, edevID)
	wantHref := coreder.DERProgramHref(edevID, fsaID, derpID)
	program := sep2.DERProgram{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: wantHref}}}
	if err := stores.DERPrograms.Create(context.Background(), edevID, derpID, program); err != nil {
		t.Fatalf("seed DERProgram: %v", err)
	}

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	listResp, err := http.Get(srv.URL + "/edev/" + edevID + "/fsa/" + fsaID + "/derp")
	if err != nil {
		t.Fatalf("GET DERProgramList: %v", err)
	}
	defer func() { _ = listResp.Body.Close() }()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET DERProgramList status = %d, want 200", listResp.StatusCode)
	}
	body, err := io.ReadAll(listResp.Body)
	if err != nil {
		t.Fatalf("read DERProgramList body: %v", err)
	}
	var list sep2.DERProgramList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode DERProgramList: %v", err)
	}
	if len(list.DERProgram) != 1 {
		t.Fatalf("DERProgramList members = %d, want 1", len(list.DERProgram))
	}
	memberHref := list.DERProgram[0].Href
	if memberHref != wantHref {
		t.Errorf("member href = %q, want %q", memberHref, wantHref)
	}

	memberResp, err := http.Get(srv.URL + memberHref)
	if err != nil {
		t.Fatalf("GET member href %q: %v", memberHref, err)
	}
	defer func() { _ = memberResp.Body.Close() }()
	if memberResp.StatusCode != http.StatusOK {
		t.Errorf("GET member href %q status = %d, want 200: the FSA-to-DERProgram link walk must resolve", memberHref, memberResp.StatusCode)
	}
}

// TestAssembly_DERProgramMemberStaysReadOnly pins the method set of the
// DERProgram member route against the handler it is mounted on.
//
// That handler takes its method set as a value rather than hardcoding one, so
// "read-only" is a property of the call site rather than of the function.
// An empty itemMethods at the call site must
// therefore serve exactly what the hardcoded string served before: GET and HEAD,
// and an Allow header naming only those. Widening a read-only resource's method
// set is precisely the change that would otherwise ship dark, because a client
// that never tries a write would never notice.
//
// This is the WIRE half of that claim: what a client actually gets. Because only
// a GET pattern is registered for this path, the 405 and its Allow come from
// http.ServeMux rather than from the handler, so this test alone cannot tell a
// read-only handler from a writable one mounted on a read-only pattern. The
// handler's own gate, which is what would stop a future "PUT ..." registration
// from silently enabling a write, is pinned directly in
// scopedresource_internal_test.go. Neither half is sufficient alone.
func TestAssembly_DERProgramMemberStaysReadOnly(t *testing.T) {
	t.Parallel()

	stores := testStores()
	edevID, fsaID, derpID := "e1", "f1", "p1"
	seedOwnedDevices(t, stores.EndDevices, edevID)
	href := coreder.DERProgramHref(edevID, fsaID, derpID)
	program := sep2.DERProgram{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: href}}}
	if err := stores.DERPrograms.Create(context.Background(), edevID, derpID, program); err != nil {
		t.Fatalf("seed DERProgram: %v", err)
	}

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		t.Run(method+" is refused", func(t *testing.T) {
			req, err := http.NewRequest(method, srv.URL+href, strings.NewReader(
				`<DERProgram xmlns="urn:ieee:std:2030.5:ns"/>`))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, href, err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405: core exposes no write route for a single DERProgram", resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
		})
	}

	t.Run("GET still resolves", func(t *testing.T) {
		resp, err := http.Get(srv.URL + href)
		if err != nil {
			t.Fatalf("GET %s: %v", href, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var got sep2.DERProgram
		decodeXML(t, resp, &got)
		if got.Href != href {
			t.Errorf("Href = %q, want %q", got.Href, href)
		}
	})

	// A write must not have leaked into the store even though it was refused.
	if _, err := stores.DERPrograms.Get(context.Background(), edevID, derpID); err != nil {
		t.Errorf("the seeded DERProgram is gone after the refused writes: %v", err)
	}
}

// TestAssembly_AsNotifyRemoved: a notifier that also satisfies notifyRemover
// wires the subscription-delete handler with the NotifyRemoved callback.
// Asserts the DELETE /edev/{id}/sub/{subId} path is mounted and returns 204.
func TestAssembly_AsNotifyRemoved(t *testing.T) {
	t.Parallel()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	stub := &notifyRemoverStub{}
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		stub,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Verify the subscription-delete pattern is registered.
	found := false
	for _, p := range patterns {
		if p == "DELETE /edev/{id}/sub/{subId}" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DELETE /edev/{id}/sub/{subId} not in pattern list; patterns = %v", patterns)
	}

	// Seed a subscription so the DELETE path has something to act on.
	ctx := context.Background()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/e1/sub/s1"},
		},
		SubscribedResource: "/edev",
	}
	if err := stores.Subscriptions.Create(ctx, "s1", sub); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, srv.URL+"/edev/e1/sub/s1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1/sub/s1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE /edev/e1/sub/s1 status = %d, want 204", resp.StatusCode)
	}
}

// TestAssembly_PostMirrorUsagePointReading_ViaLocationHeader exercises the
// real assembled router (not a hand-mounted test mux) end to end: POST /mup,
// then POST the reading to exactly the Location value the server returned.
// IEEE 2030.5-2018 section 10.11.3 rule (d): the client posts readings "to
// the resource identified in the Metering server's response... (e.g.,
// /mup/3)". Before "POST /mup/{id}" was mounted alongside "GET /mup/{id}",
// this returned 405, since only GET was registered at that pattern.
func TestAssembly_PostMirrorUsagePointReading_ViaLocationHeader(t *testing.T) {
	t.Parallel()

	stores := testStores()
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)

	found := false
	for _, p := range patterns {
		if p == "POST /mup/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("POST /mup/{id} not in pattern list; patterns = %v", patterns)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	mup := sep2.MirrorUsagePoint{MRID: "INV001"}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal MirrorUsagePoint: %v", err)
	}
	createResp, err := http.Post(srv.URL+"/mup", "application/xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /mup: %v", err)
	}
	_ = createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /mup status = %d, want 201", createResp.StatusCode)
	}
	loc := createResp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /mup: no Location header")
	}

	val := int64(4200)
	uom := sep2.UomWatts
	mmr := sep2.MirrorMeterReading{
		MRID:        "MMR01",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	}
	mmrBody, err := xml.Marshal(&mmr)
	if err != nil {
		t.Fatalf("marshal MirrorMeterReading: %v", err)
	}

	// Follow the header value verbatim: the point under test is that our own
	// advertised Location and our own accepted POST target agree.
	postResp, err := http.Post(srv.URL+loc, "application/xml", strings.NewReader(string(mmrBody)))
	if err != nil {
		t.Fatalf("POST %s: %v", loc, err)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	_ = postResp.Body.Close()

	if postResp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("POST %s returned 405: server's own Location header rejected; body=%s", loc, postBody)
	}
	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s status = %d, want 201; body=%s", loc, postResp.StatusCode, postBody)
	}

	// The parent id is server-assigned and derived from the creating device's
	// identity together with its mRID (metering.MirrorStoreID), so the client
	// addresses it by the Location it was handed, never by its own mRID.
	parentID := strings.TrimPrefix(loc, "/mup/")
	mmrLoc := postResp.Header.Get("Location")
	prefix := "/mup/" + parentID + "/mr/"
	if !strings.HasPrefix(mmrLoc, prefix) {
		t.Fatalf("MirrorMeterReading Location = %q, want prefix %q", mmrLoc, prefix)
	}
	id := strings.TrimPrefix(mmrLoc, prefix)
	stored, err := stores.MirrorMeterReadings.Get(context.Background(), parentID, id)
	if err != nil {
		t.Fatalf("get stored MirrorMeterReading: %v", err)
	}
	if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != val {
		t.Errorf("stored Reading value not preserved: %+v", stored.Reading)
	}

	// deviceLFDI override invariant: unaffected by this route, cert-derived
	// identity is stamped only at MirrorUsagePoint creation.
	parent, err := stores.MirrorUsagePoints.Get(context.Background(), parentID)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if parent.DeviceLFDI != testLFDI {
		t.Errorf("parent DeviceLFDI = %q, want %q (cert override unaffected by reading POST)", parent.DeviceLFDI, testLFDI)
	}

	// Rule (c) regression check against the real router.
	getResp, err := http.Get(srv.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	getBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, getResp.StatusCode)
	}
	if strings.Contains(string(getBody), "MirrorMeterReading") {
		t.Errorf("GET %s served a MirrorMeterReading element, violates rule (c); body=%s", loc, getBody)
	}
}

// TestAssembly_MirrorOwnershipIsWiredOnEveryMupRoute proves the section
// 10.11.3 rule (e) gate is actually reachable through the real router, not
// merely present in the handler package. The handler-level suite in
// pkg/sep2srv/handlers/metering covers the rule itself; this covers the
// wiring, which is the half that silently regresses when a route is added and
// its lfdiProvider argument is forgotten.
//
// Two routers are built over ONE set of stores with two different identities:
// the first creates the mirror, the second is a valid but unrelated
// certificate. Every /mup route that touches a single mirror must deny it.
func TestAssembly_MirrorOwnershipIsWiredOnEveryMupRoute(t *testing.T) {
	t.Parallel()

	const otherLFDI = "FFEEDDCCBBAA998877665544332211009988776655443322"
	if otherLFDI == testLFDI {
		t.Fatal("test setup: the two identities must differ")
	}

	stores := testStores()

	ownerPolicy := testAuthPolicy()
	ownerHandler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, ownerPolicy, "serverSFDI", "serverLFDI", nil,
	)
	ownerSrv := httptest.NewServer(ownerHandler)
	defer ownerSrv.Close()

	otherPolicy := testAuthPolicy()
	otherPolicy.Identity = func(_ context.Context) (lfdi, sfdi string, ok bool) {
		return otherLFDI, testSFDI, true
	}
	otherHandler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, otherPolicy, "serverSFDI", "serverLFDI", nil,
	)
	otherSrv := httptest.NewServer(otherHandler)
	defer otherSrv.Close()

	body, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: "OWNED"})
	if err != nil {
		t.Fatalf("marshal MirrorUsagePoint: %v", err)
	}
	createResp, err := http.Post(ownerSrv.URL+"/mup", "application/xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /mup: %v", err)
	}
	_ = createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /mup status = %d, want 201", createResp.StatusCode)
	}
	// The mirror's URL is server-assigned; the owner learns it from Location.
	ownedPath := createResp.Header.Get("Location")
	if ownedPath == "" {
		t.Fatal("POST /mup: no Location header")
	}
	ownedID := strings.TrimPrefix(ownedPath, "/mup/")

	val := int64(99)
	uom := sep2.UomWatts
	mmrBody, err := xml.Marshal(&sep2.MirrorMeterReading{
		MRID:        "FORGED",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	})
	if err != nil {
		t.Fatalf("marshal MirrorMeterReading: %v", err)
	}

	for _, path := range []string{ownedPath, ownedPath + "/mr"} {
		resp, err := http.Post(otherSrv.URL+path, "application/xml", strings.NewReader(string(mmrBody)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s as a non-creator: status = %d, want 403; body = %s", path, resp.StatusCode, respBody)
		}
		if strings.Contains(string(respBody), "<") || strings.Contains(string(respBody), testLFDI) {
			t.Errorf("POST %s denial body leaks content: %s", path, respBody)
		}
	}

	getResp, err := http.Get(otherSrv.URL + ownedPath)
	if err != nil {
		t.Fatalf("GET %s: %v", ownedPath, err)
	}
	getBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Errorf("GET %s as a non-creator: status = %d, want 403; body = %s", ownedPath, getResp.StatusCode, getBody)
	}
	if strings.Contains(string(getBody), "<") || strings.Contains(string(getBody), testLFDI) {
		t.Errorf("GET denial body leaks content: %s", getBody)
	}

	// The two Mandatory instance methods are covered here for the same
	// reason the GET and the two POSTs are: this test is about
	// WIRING, and a route added without its lfdiProvider argument is exactly the
	// regression that would leave rule (e) present in the handler package and
	// absent from the server. The PUT carries a body that would take a different
	// branch if it were read, so a gate that ran after the body would answer
	// something other than 403 here.
	for _, m := range []struct {
		method string
		body   string
	}{
		{http.MethodPut, `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>OWNED</mRID></MirrorUsagePoint>`},
		{http.MethodDelete, ""},
	} {
		req, err := http.NewRequest(m.method, otherSrv.URL+ownedPath, strings.NewReader(m.body))
		if err != nil {
			t.Fatalf("build %s %s: %v", m.method, ownedPath, err)
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", m.method, ownedPath, err)
		}
		mBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-creator: status = %d, want 403; body = %s",
				m.method, ownedPath, resp.StatusCode, mBody)
		}
		if strings.Contains(string(mBody), "<") || strings.Contains(string(mBody), testLFDI) {
			t.Errorf("%s denial body leaks content: %s", m.method, mBody)
		}
	}

	// The denied PUT and DELETE left the owner's record exactly as it was, which
	// a status code cannot show: a 403 is returned whether or not the write ran
	// first.
	survivor, err := stores.MirrorUsagePoints.Get(context.Background(), ownedID)
	if err != nil {
		t.Fatalf("owner record missing after denied PUT and DELETE: %v", err)
	}
	if survivor.MRID != "OWNED" || survivor.DeviceLFDI != testLFDI {
		t.Errorf("owner record altered by a denied request: MRID = %q, DeviceLFDI = %q, want %q and %q",
			survivor.MRID, survivor.DeviceLFDI, "OWNED", testLFDI)
	}

	// Nothing was persisted by any of the denied writes.
	count, err := stores.MirrorMeterReadings.Count(context.Background(), ownedID)
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 0 {
		t.Errorf("stored reading count = %d, want 0: a denied POST persisted data", count)
	}

	// The creator is unaffected: same stores, same routes, 200 and 201.
	okResp, err := http.Post(ownerSrv.URL+ownedPath, "application/xml", strings.NewReader(string(mmrBody)))
	if err != nil {
		t.Fatalf("owner POST %s: %v", ownedPath, err)
	}
	_ = okResp.Body.Close()
	if okResp.StatusCode != http.StatusCreated {
		t.Errorf("owner POST %s status = %d, want 201", ownedPath, okResp.StatusCode)
	}
	ownerGet, err := http.Get(ownerSrv.URL + ownedPath)
	if err != nil {
		t.Fatalf("owner GET %s: %v", ownedPath, err)
	}
	ownerGetBody, _ := io.ReadAll(ownerGet.Body)
	_ = ownerGet.Body.Close()
	if ownerGet.StatusCode != http.StatusOK {
		t.Errorf("owner GET %s status = %d, want 200", ownedPath, ownerGet.StatusCode)
	}
	// Rule (c) still holds for the owner through the real router.
	if strings.Contains(string(ownerGetBody), "MirrorMeterReading") {
		t.Errorf("owner GET served a MirrorMeterReading element, violates rule (c); body = %s", ownerGetBody)
	}

	// The list stays server-wide in this change: rule (e) governs POST only,
	// and 6.2.3.1 puts sub-resource access control out of scope. Per-client
	// list scoping is permitted by 4.6.1 but is a separate design decision
	// (CSIP 5.7.1 per-device MirrorUsagePointListLink URIs), deliberately not
	// made here. Asserting it keeps the boundary of this change explicit.
	listResp, err := http.Get(otherSrv.URL + "/mup")
	if err != nil {
		t.Fatalf("GET /mup: %v", err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mup status = %d, want 200 (list is intentionally unscoped)", listResp.StatusCode)
	}
	if !strings.Contains(string(listBody), "<mRID>OWNED</mRID>") {
		t.Errorf("GET /mup omitted a mirror the caller did not create; the list was scoped, which this change does not do. body = %s", listBody)
	}
}

// TestAssembly_SameMRIDFromTwoDevicesStaysIsolated reproduces the field
// condition through the real router: nine devices holding nine distinct
// certificates POSTed only three distinct MirrorUsagePoint mRIDs between them,
// because nothing coordinates a client-chosen mRID across devices. Keyed
// globally on that mRID, the second device to use one was answered 200 with a
// Location pointing at the FIRST device's mirror, and would then have posted
// its readings into another device's resource.
//
// Two devices, one shared mRID, one shared set of stores: each must end up
// with its own mirror, its own URL, and no access to the other's.
func TestAssembly_SameMRIDFromTwoDevicesStaysIsolated(t *testing.T) {
	t.Parallel()

	const otherLFDI = "FFEEDDCCBBAA998877665544332211009988776655443322"
	const sharedMRID = "0123456789ABCDEF0123456789ABCDEF"

	stores := testStores()

	deviceAHandler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	deviceASrv := httptest.NewServer(deviceAHandler)
	defer deviceASrv.Close()

	bPolicy := testAuthPolicy()
	bPolicy.Identity = func(_ context.Context) (lfdi, sfdi string, ok bool) {
		return otherLFDI, testSFDI, true
	}
	deviceBHandler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, bPolicy, "serverSFDI", "serverLFDI", nil,
	)
	deviceBSrv := httptest.NewServer(deviceBHandler)
	defer deviceBSrv.Close()

	body, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: sharedMRID})
	if err != nil {
		t.Fatalf("marshal MirrorUsagePoint: %v", err)
	}

	create := func(srv *httptest.Server, who string) string {
		t.Helper()
		resp, err := http.Post(srv.URL+"/mup", "application/xml", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("%s POST /mup: %v", who, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s POST /mup: status = %d, want 201 (its own mirror); body = %s", who, resp.StatusCode, respBody)
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Fatalf("%s POST /mup: empty Location", who)
		}
		if len(loc) >= 128 {
			t.Errorf("%s POST /mup: Location = %q has length %d, want < 128", who, loc, len(loc))
		}
		return loc
	}

	locA := create(deviceASrv, "device A")
	locB := create(deviceBSrv, "device B")

	if locA == locB {
		t.Fatalf("device B was handed device A's mirror: both Locations are %q", locA)
	}

	// Each stored record carries its own creator's LFDI, cert-derived.
	for _, tc := range []struct{ loc, owner, who string }{
		{locA, testLFDI, "device A"},
		{locB, otherLFDI, "device B"},
	} {
		stored, err := stores.MirrorUsagePoints.Get(context.Background(), strings.TrimPrefix(tc.loc, "/mup/"))
		if err != nil {
			t.Fatalf("%s: get stored mirror at %q: %v", tc.who, tc.loc, err)
		}
		if stored.DeviceLFDI != tc.owner {
			t.Errorf("%s: stored DeviceLFDI = %q, want %q", tc.who, stored.DeviceLFDI, tc.owner)
		}
		if stored.MRID != sharedMRID {
			t.Errorf("%s: stored mRID = %q, want %q preserved", tc.who, stored.MRID, sharedMRID)
		}
		if stored.Href != tc.loc {
			t.Errorf("%s: stored Href = %q, want %q (must agree with Location)", tc.who, stored.Href, tc.loc)
		}
	}

	// The ownership gate still holds across the pair: device A may not write
	// or read device B's mirror even though they share an mRID.
	uom := sep2.UomWatts
	val := int64(7)
	mmrBody, err := xml.Marshal(&sep2.MirrorMeterReading{
		MRID:        "FORGED",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	})
	if err != nil {
		t.Fatalf("marshal MirrorMeterReading: %v", err)
	}
	for _, path := range []string{locB, locB + "/mr"} {
		resp, err := http.Post(deviceASrv.URL+path, "application/xml", strings.NewReader(string(mmrBody)))
		if err != nil {
			t.Fatalf("device A POST %s: %v", path, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("device A POST %s (device B's mirror): status = %d, want 403; body = %s", path, resp.StatusCode, respBody)
		}
	}
	getResp, err := http.Get(deviceASrv.URL + locB)
	if err != nil {
		t.Fatalf("device A GET %s: %v", locB, err)
	}
	getBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Errorf("device A GET %s (device B's mirror): status = %d, want 403; body = %s", locB, getResp.StatusCode, getBody)
	}

	count, err := stores.MirrorMeterReadings.Count(context.Background(), strings.TrimPrefix(locB, "/mup/"))
	if err != nil {
		t.Fatalf("count readings under device B's mirror: %v", err)
	}
	if count != 0 {
		t.Errorf("readings stored under device B's mirror by device A = %d, want 0", count)
	}
}

// TestAssembly_PatternListNonEmpty asserts that BuildProtocolRouter returns
// a non-empty, sorted pattern list (boot-logging invariant).
func TestAssembly_PatternListNonEmpty(t *testing.T) {
	t.Parallel()
	_, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		testStores(),
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)

	if len(patterns) == 0 {
		t.Fatal("BuildProtocolRouter returned empty pattern list")
	}

	// Assert sorted (the recordingMux.Patterns contract)
	for i := 1; i < len(patterns); i++ {
		if patterns[i] < patterns[i-1] {
			t.Errorf("pattern list not sorted at index %d: %q < %q", i, patterns[i], patterns[i-1])
		}
	}

	// Assert /edev and /tm are present
	found := map[string]bool{}
	for _, p := range patterns {
		found[p] = true
	}
	for _, want := range []string{"GET /dcap", "GET /tm", "GET /edev", "POST /edev"} {
		if !found[want] {
			t.Errorf("pattern list missing %q; got: %v", want, patterns)
		}
	}
}

// TestResourceNotifierAliasIdentity proves true type identity between
// assembly.ResourceNotifier and coreedev.ResourceNotifier. The compile-time
// bidirectional var _ pair is kept as a cheap guard, but is insufficient on
// its own because it also compiles for method-compatible distinct interfaces.
// The reflect.TypeOf assertion is the runtime proof: pointer-to-interface
// types are equal only when the two names denote exactly the same type (a
// true alias), not merely compatible method sets.
func TestResourceNotifierAliasIdentity(t *testing.T) {
	t.Parallel()
	var _ assembly.ResourceNotifier = (coreedev.ResourceNotifier)(nil)
	var _ coreedev.ResourceNotifier = (assembly.ResourceNotifier)(nil)
	if reflect.TypeOf((*assembly.ResourceNotifier)(nil)) != reflect.TypeOf((*coreedev.ResourceNotifier)(nil)) {
		t.Fatal("assembly.ResourceNotifier is not the same type as coreedev.ResourceNotifier")
	}
}
