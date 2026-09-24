package assembly_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The router, over stores that can FAIL.
//
// Every store in this repository is in-memory and essentially cannot fail, so
// the distinction the error contract draws between "the resource is absent" and
// "the backend did not answer" has never been exercised on a single route. The
// moment a durable backend is attached that distinction goes live across the
// whole surface at once, and a route that renders a failed
// lookup as a 404, as an empty list, or as a synthesized default resource is a
// silent wrong answer on the wire: the client is told a fact about the fleet
// that the server does not actually know.
//
// It matters beyond status codes because the store is a seam the bridge reads
// directly. A store error rendered as an empty list is indistinguishable to the
// bridge from a genuinely empty fleet, the same confusion a fail-loud guard
// already had to be added for on the CIM side.
//
// The route list here is DERIVED from BuildProtocolRouter's own pattern
// enumeration, never hand-written, so a route added tomorrow is covered the day
// it is mounted rather than the day somebody remembers to extend a list. What a
// hand-written list buys is a test that keeps passing while the surface it
// claims to cover grows underneath it.

// faultProbePathValue is substituted for every wildcard in a mounted pattern.
//
// The value is irrelevant to what is being asserted: with the fault armed, no
// lookup gets far enough for the key to matter. It must merely be non-empty, so
// that a route is not accidentally probed with an empty path segment, which
// several handlers treat as a 400 before any store is touched.
const faultProbePathValue = "1"

// storeFreeRoutes are the mounted patterns a store failure cannot reach,
// with the reason each one cannot.
//
// This is the ONLY sanctioned way for a mounted route to sit outside the fault
// table, and it is checked in both directions by [partitionMountedRoutes]: an
// entry naming a pattern that is not mounted fails, and a mounted pattern that
// is neither probed nor named here fails. A route cannot be quietly dropped
// from coverage by deleting its probe.
var storeFreeRoutes = map[string]string{
	"GET /dcap":     "serves a static DeviceCapability document; it reads no store",
	"GET /tm":       "serves the clock from RouterConfig; it reads no store",
	"GET /sdev":     "serves the server's own SelfDevice from the configured SFDI and LFDI; it reads no store",
	"GET /sdev/sdi": "serves the server's own DeviceInformation from the configured LFDI; it reads no store",

	// The subscription store is NOT part of the pkg/store contract: it is
	// queried by resource href and by device, and its records carry a store key
	// alongside the resource, so no interface in pkg/store expresses it and
	// Stores.Subscriptions is a concrete *memory.SubscriptionStore. There is
	// nothing here to inject a contract-level fault into. When that store is
	// brought under the contract, these three lose their exemption and the
	// partition check below is what forces them back into the table.
	"GET /edev/{id}/sub":            "backed by *memory.SubscriptionStore, which pkg/store does not model",
	"POST /edev/{id}/sub":           "backed by *memory.SubscriptionStore, which pkg/store does not model",
	"DELETE /edev/{id}/sub/{subId}": "backed by *memory.SubscriptionStore, which pkg/store does not model",
}

// faultProbeBodies supplies the request document for every mounted pattern
// whose method carries one.
//
// A write route reached with an unparseable body answers 400 from its own body
// check and never touches a store, so a probe without a valid document would
// report a passing route that the fault never reached. Every entry is therefore
// a document the handler accepts on the happy path, and [probeRequestFor]
// refuses to build a probe for a write pattern that has no entry, which is what
// stops a newly mounted POST from being covered by an empty body that proves
// nothing.
var faultProbeBodies = map[string]string{
	"POST /edev": sep2Doc("EndDevice", ""),
	"PUT /edev/{id}": sep2Doc("EndDevice",
		`<lFDI>`+testLFDI+`</lFDI>`),

	"POST /edev/{id}/lel": sep2Doc("LogEvent",
		`<createdDateTime>1700000000</createdDateTime>`+
			`<logEventCode>1</logEventCode>`+
			`<logEventID>42</logEventID>`+
			`<logEventPEN>37244</logEventPEN>`+
			`<profileID>2</profileID>`),

	"POST /edev/{id}/frq": sep2Doc("FlowReservationRequest",
		`<durationRequested>60</durationRequested>`+
			`<energyRequested><multiplier>0</multiplier><value>100</value></energyRequested>`+
			`<intervalRequested><duration>60</duration><start>1700000000</start></intervalRequested>`+
			`<powerRequested><multiplier>0</multiplier><value>100</value></powerRequested>`+
			`<requestStatus><dateTime>1700000000</dateTime><requestStatus>0</requestStatus></requestStatus>`),

	"PUT /edev/{id}/cfg":   sep2Doc("Configuration", `<userDeviceName>probe</userDeviceName>`),
	"PUT /edev/{id}/dstat": sep2Doc("DeviceStatus", `<changedTime>1700000000</changedTime>`),
	"PUT /edev/{id}/ps": sep2Doc("PowerStatus",
		`<changedTime>1700000000</changedTime><currentPowerSource>1</currentPowerSource>`),

	"PUT /edev/{id}/der/{derId}":        sep2Doc("DER", ""),
	"PUT /edev/{id}/der/{derId}/dercap": sep2Doc("DERCapability", `<type>83</type>`),
	"PUT /edev/{id}/der/{derId}/derg":   sep2Doc("DERSettings", `<updatedTime>1700000000</updatedTime>`),
	"PUT /edev/{id}/der/{derId}/ders":   sep2Doc("DERStatus", `<readingTime>1700000000</readingTime>`),
	"PUT /edev/{id}/der/{derId}/dera":   sep2Doc("DERAvailability", `<readingTime>1700000000</readingTime>`),

	"PUT /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc": sep2Doc("DefaultDERControl",
		`<DERControlBase><opModEnergize>true</opModEnergize></DERControlBase>`),

	"POST /msg/{msgId}/tm": sep2Doc("TextMessage",
		`<creationTime>1700000000</creationTime>`+
			`<EventStatus><currentStatus>0</currentStatus><dateTime>1700000000</dateTime>`+
			`<potentiallySuperseded>false</potentiallySuperseded></EventStatus>`+
			`<interval><duration>60</duration><start>1700000000</start></interval>`+
			`<msgID>1</msgID><priority>0</priority><textMessage>probe</textMessage>`),

	"POST /upt": sep2Doc("UsagePoint",
		`<mRID>0A1B2C3D4E5F60718293A4B5C6D7E8F9</mRID>`+
			`<serviceCategoryKind>0</serviceCategoryKind><status>1</status>`),

	"POST /mup":         mirrorUsagePointDoc,
	"PUT /mup/{id}":     mirrorUsagePointDoc,
	"POST /mup/{id}":    mirrorMeterReadingDoc,
	"POST /mup/{id}/mr": mirrorMeterReadingDoc,

	"POST /rsps/{rspsId}/rsp": sep2Doc("Response",
		`<createdDateTime>1700000000</createdDateTime>`+
			`<endDeviceLFDI>`+testLFDI+`</endDeviceLFDI>`+
			`<status>1</status>`+
			`<subject>0A1B2C3D4E5F60718293A4B5C6D7E8F9</subject>`),
}

var mirrorUsagePointDoc = sep2Doc("MirrorUsagePoint",
	`<mRID>1A2B3C4D5E6F70819293A4B5C6D7E8F9</mRID>`+
		`<description>fault probe</description>`+
		`<deviceLFDI>`+testLFDI+`</deviceLFDI>`+
		`<serviceCategoryKind>0</serviceCategoryKind><status>1</status>`)

var mirrorMeterReadingDoc = sep2Doc("MirrorMeterReading",
	`<mRID>2A2B3C4D5E6F70819293A4B5C6D7E8F9</mRID>`+
		`<Reading><value>1</value></Reading>`)

// sep2Doc wraps children in a root element carrying the IEEE 2030.5 namespace.
// The namespace is not decoration: the metering handlers reject a document
// whose root does not carry it, and would do so before reaching any store.
func sep2Doc(root, children string) string {
	return `<` + root + ` xmlns="urn:ieee:std:2030.5:ns">` + children + `</` + root + `>`
}

// faultyStores builds a Stores whose every contract-typed field is the
// PRODUCTION in-memory store wrapped in a fault decorator, all sharing one
// switch.
//
// Wrapping the real implementation rather than substituting a different one is
// deliberate. The happy path stays exactly what a deployment runs, so the only
// difference between this server and a healthy one is the injected failure, and
// a route that answers 500 here answers 500 for that reason and no other. A
// purpose-built failing implementation could differ from the memory store in
// some second way, and the test would not be able to tell which difference
// produced the status code.
//
// The three fields that pkg/store does not model stay concrete, exactly as they
// are on Stores: EndDeviceIndexes is an index allocator, Subscriptions is
// queried by href and by device, and AdminFSAs is a management plane. Their
// routes are named in storeFreeRoutes with that reason.
//
// The EndDevice store is the exception: it sits on its own switch and holds a
// device the test identity owns at faultProbePathValue. The ownership gate reads
// that store before any /edev/{id} handler runs, so with it failing no such
// handler could be reached; [faultProbeDevice] says how to fail it too.
func faultyStores(t *testing.T) (*assembly.Stores, *storetest.Fault, faultProbeDevice) {
	t.Helper()
	fault := &storetest.Fault{}
	device := faultProbeDevice{fault: &storetest.Fault{}, raw: memory.NewEndDeviceStore()}
	device.ensure(t)

	return &assembly.Stores{
		EndDevices:          storetest.NewFaultyEndDeviceStore(device.raw, device.fault),
		Registrations:       storetest.NewFaultyResourceStore[sep2.Registration](memory.NewRegistrationStore(), fault),
		RegistrationPolicy:  testRegistrationPolicy(),
		MirrorUsagePoints:   storetest.NewFaultyResourceStore[sep2.MirrorUsagePoint](memory.NewStore[sep2.MirrorUsagePoint](), fault),
		MirrorMeterReadings: storetest.NewFaultyScopedStore[sep2.MirrorMeterReading](memory.NewScopedStore[sep2.MirrorMeterReading](), fault),

		DERs:               storetest.NewFaultyScopedStore[sep2.DER](memory.NewScopedStore[sep2.DER](), fault),
		DERCapabilities:    storetest.NewFaultyScopedStore[sep2.DERCapability](memory.NewScopedStore[sep2.DERCapability](), fault),
		DERSettings:        storetest.NewFaultyScopedStore[sep2.DERSettings](memory.NewScopedStore[sep2.DERSettings](), fault),
		DERStatuses:        storetest.NewFaultyScopedStore[sep2.DERStatus](memory.NewScopedStore[sep2.DERStatus](), fault),
		DERAvailabilities:  storetest.NewFaultyScopedStore[sep2.DERAvailability](memory.NewScopedStore[sep2.DERAvailability](), fault),
		DERPrograms:        storetest.NewFaultyScopedStore[sep2.DERProgram](memory.NewDERProgramStore(), fault),
		DERControls:        storetest.NewFaultyScopedStore[sep2.DERControl](memory.NewScopedStore[sep2.DERControl](), fault),
		DefaultDERControls: storetest.NewFaultyScopedStore[sep2.DefaultDERControl](memory.NewScopedStore[sep2.DefaultDERControl](), fault),
		DERCurves:          storetest.NewFaultyResourceStore[sep2.DERCurve](memory.NewStore[sep2.DERCurve](), fault),

		FSAs:          storetest.NewFaultyScopedStore[sep2.FunctionSetAssignments](memory.NewScopedStore[sep2.FunctionSetAssignments](), fault),
		Subscriptions: memory.NewSubscriptionStore(),

		UsagePoints:   storetest.NewFaultyResourceStore[sep2.UsagePoint](memory.NewStore[sep2.UsagePoint](), fault),
		MeterReadings: storetest.NewFaultyScopedStore[sep2.MeterReading](memory.NewScopedStore[sep2.MeterReading](), fault),
		Readings:      storetest.NewFaultyScopedStore[sep2.Reading](memory.NewScopedStore[sep2.Reading](), fault),
		ReadingTypes:  storetest.NewFaultyResourceStore[sep2.ReadingType](memory.NewStore[sep2.ReadingType](), fault),

		Configurations:           storetest.NewFaultyScopedStore[sep2.Configuration](memory.NewScopedStore[sep2.Configuration](), fault),
		DeviceStatuses:           storetest.NewFaultyScopedStore[sep2.DeviceStatus](memory.NewScopedStore[sep2.DeviceStatus](), fault),
		LogEvents:                storetest.NewFaultyScopedStore[sep2.LogEvent](memory.NewScopedStore[sep2.LogEvent](), fault),
		PowerStatuses:            storetest.NewFaultyScopedStore[sep2.PowerStatus](memory.NewScopedStore[sep2.PowerStatus](), fault),
		MessagingPrograms:        storetest.NewFaultyResourceStore[sep2.MessagingProgram](memory.NewStore[sep2.MessagingProgram](), fault),
		TextMessages:             storetest.NewFaultyScopedStore[sep2.TextMessage](memory.NewScopedStore[sep2.TextMessage](), fault),
		FlowReservationRequests:  storetest.NewFaultyScopedStore[sep2.FlowReservationRequest](memory.NewScopedStore[sep2.FlowReservationRequest](), fault),
		FlowReservationResponses: storetest.NewFaultyScopedStore[sep2.FlowReservationResponse](memory.NewScopedStore[sep2.FlowReservationResponse](), fault),
		ResponseSets:             storetest.NewFaultyResourceStore[sep2.ResponseSet](memory.NewStore[sep2.ResponseSet](), fault),
		Responses:                storetest.NewFaultyScopedStore[sep2.Response](memory.NewScopedStore[sep2.Response](), fault),
	}, fault, device
}

// faultProbeDevice is the EndDevice every fault probe addresses, with its own
// fault switch.
type faultProbeDevice struct {
	fault *storetest.Fault
	raw   *memory.EndDeviceStore
}

// ensure re-seeds the device when a probe removed it. A DELETE /edev/{id}
// probe can delete the record before a later store write fails.
func (d faultProbeDevice) ensure(t *testing.T) {
	t.Helper()
	seedOwnedDevices(t, d.raw, faultProbePathValue)
}

// routePartition is the split of the router's own pattern enumeration into the
// routes a fault probe drives and the routes deliberately left out.
type routePartition struct {
	Probed   []string
	Excluded []string
}

// partitionMountedRoutes is the vacuity guard, and it is a plain function
// rather than inline test code so that it can itself be tested against a
// degenerate enumeration.
//
// A table-driven test over an empty table passes. So does one whose table was
// built by filtering an enumeration that silently came back empty, and so does
// one whose exclusion list quietly grew to swallow the surface. Each of those
// reports success from a run that measured nothing, which is worse than a
// failure because it is indistinguishable from a real pass.
//
// Every way that can happen is refused here:
//
//   - an empty enumeration, which is what a router that mounted nothing, or a
//     changed accessor that stopped reporting, would produce;
//   - an exclusion naming a pattern that is not mounted, which is a stale entry
//     whose route may have been renamed out from under it;
//   - a partition that does not account for every mounted pattern exactly once;
//   - an empty probe set, which is the "covers nothing" case stated directly.
func partitionMountedRoutes(patterns []string, exclusions map[string]string) (routePartition, error) {
	if len(patterns) == 0 {
		return routePartition{}, fmt.Errorf("the router reported no mounted patterns; a route table built from this would cover nothing and pass")
	}

	mounted := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		if mounted[p] {
			return routePartition{}, fmt.Errorf("pattern %q is enumerated twice; the partition count cannot be trusted", p)
		}
		mounted[p] = true
	}

	stale := make([]string, 0, len(exclusions))
	for p := range exclusions {
		if !mounted[p] {
			stale = append(stale, p)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return routePartition{}, fmt.Errorf("storeFreeRoutes names %d pattern(s) the router does not mount: %v; "+
			"an exclusion that outlives its route silently shrinks the count it is checked against", len(stale), stale)
	}

	var part routePartition
	for _, p := range patterns {
		if _, excluded := exclusions[p]; excluded {
			part.Excluded = append(part.Excluded, p)
			continue
		}
		part.Probed = append(part.Probed, p)
	}

	if len(part.Probed) == 0 {
		return routePartition{}, fmt.Errorf("every one of the %d mounted patterns is excluded; the fault table would cover nothing", len(patterns))
	}
	if got := len(part.Probed) + len(part.Excluded); got != len(patterns) {
		return routePartition{}, fmt.Errorf("the partition accounts for %d patterns but %d are mounted", got, len(patterns))
	}
	return part, nil
}

// probeRequestFor turns a mounted pattern into a concrete request.
//
// It refuses rather than guesses for a write method with no registered
// document: a POST probed with an empty body is answered by the handler's own
// body check, so it would report a covered route that the store failure never
// reached. That refusal is why adding a write route without a probe body fails
// this test instead of passing it vacuously.
//
// It substitutes faultProbePathValue for every wildcard. A caller that needs
// a different, stated id (for example a specific EndDevice to compare a
// manager's answer against its owner's) calls probeRequestForID directly
// instead of relying on faultProbePathValue's value matching by coincidence.
func probeRequestFor(base, pattern string) (*http.Request, error) {
	return probeRequestForID(base, pattern, faultProbePathValue)
}

// probeRequestForID is probeRequestFor with the wildcard substitution named
// explicitly, so a caller states which id it is asking about rather than
// inheriting whatever faultProbePathValue happens to be.
func probeRequestForID(base, pattern, idValue string) (*http.Request, error) {
	method, shape, ok := strings.Cut(pattern, " ")
	if !ok {
		return nil, fmt.Errorf("pattern %q has no method", pattern)
	}

	segments := strings.Split(shape, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			segments[i] = idValue
		}
	}
	path := strings.Join(segments, "/")

	body, hasBody := faultProbeBodies[pattern]
	needsBody := method == http.MethodPost || method == http.MethodPut
	switch {
	case needsBody && !hasBody:
		return nil, fmt.Errorf("no probe document registered for %q; add one to faultProbeBodies, "+
			"because a %s with no body is answered by the handler's body check before any store is read", pattern, method)
	case !needsBody && hasBody:
		return nil, fmt.Errorf("a probe document is registered for %q, whose method carries none", pattern)
	}

	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if needsBody {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	return req, nil
}

// TestEveryMountedRouteReportsAStoreFailureAsAServerError is the route-level
// half of the error contract: a store that fails must not be reported to a
// client as a resource that is absent or a collection that is empty.
//
// The status code is the whole assertion here, and deliberately so. 404 is the
// answer that says "this resource does not exist", 200 with an empty list says
// "this collection is empty", and 200 with a synthesized default says "here is
// the resource": each is a claim about the fleet that a server whose backend
// just stopped answering is not entitled to make. Only 5xx says "I do not
// know", and a client that reads 404 for a resource that is really there acts
// on the wrong fact, whereas a client that reads 500 retries.
func TestEveryMountedRouteReportsAStoreFailureAsAServerError(t *testing.T) {
	t.Parallel()

	stores, fault, device := faultyStores(t)

	// The router is built while the stores are HEALTHY: assembly seeds the
	// default ResponseSet at build time, and a router assembled against an
	// already-failing store would be a different router from the one a
	// deployment runs.
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	part, err := partitionMountedRoutes(patterns, storeFreeRoutes)
	if err != nil {
		t.Fatalf("route coverage: %v", err)
	}
	t.Logf("mounted %d route(s): probing %d, %d excluded as store-free",
		len(patterns), len(part.Probed), len(part.Excluded))

	fault.Arm(storetest.ErrBackendUnavailable)

	probed := 0
	for _, pattern := range part.Probed {
		device.ensure(t)
		status, err := probeStatus(srv.URL, pattern)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		probed++

		if status < 500 || status > 599 {
			t.Errorf("%s answered %d with the store failing; want 5xx. "+
				"A non-5xx here reports a broken backend to the client as a fact about the resource",
				pattern, status)
		}
	}

	// The counter closes the last vacuity gap the partition cannot: the
	// partition proves the TABLE is complete, and this proves the table was
	// actually walked. A loop that skipped every entry on a build error would
	// otherwise leave a green test behind.
	if probed != len(part.Probed) {
		t.Errorf("drove %d of %d probed routes; the table did not cover what it claimed", probed, len(part.Probed))
	}

	// Now the EndDevice store fails as well. Every /edev route owes a 5xx: the
	// ungated two from their handlers, the rest from the ownership gate, which
	// cannot establish ownership and must not answer as though it had refused.
	device.fault.Arm(storetest.ErrBackendUnavailable)
	edevProbed := 0
	for _, pattern := range part.Probed {
		_, shape, _ := strings.Cut(pattern, " ")
		if shape != "/edev" && !strings.HasPrefix(shape, "/edev/") {
			continue
		}
		device.ensure(t)
		status, err := probeStatus(srv.URL, pattern)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		edevProbed++

		if status < 500 || status > 599 {
			t.Errorf("%s answered %d with the EndDevice store failing; want 5xx", pattern, status)
		}
	}
	// The store-free /edev routes still pass the gate, which reads the
	// EndDevice store before their handlers run.
	for _, pattern := range part.Excluded {
		_, shape, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(shape, "/edev/") {
			continue
		}
		device.ensure(t)
		method, path := concreteGatePath(pattern)
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		_ = resp.Body.Close()
		edevProbed++
		if resp.StatusCode < 500 || resp.StatusCode > 599 {
			t.Errorf("store-free %s answered %d with the EndDevice store failing; want 5xx from the ownership gate", pattern, resp.StatusCode)
		}
	}
	if edevProbed == 0 {
		t.Error("no /edev route was probed with the EndDevice store failing; that phase asserted nothing")
	}
	t.Logf("probed %d /edev route(s) with the EndDevice store failing", edevProbed)
}

// probeStatus drives one probe request and returns its status.
func probeStatus(base, pattern string) (int, error) {
	req, err := probeRequestFor(base, pattern)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// TestPartitionMountedRoutesRefusesAVacuousTable exercises the vacuity guard
// itself against the degenerate inputs it exists to catch.
//
// The first case is the one worth naming: an empty enumeration. It is exactly
// what a broken accessor, or a router that mounted nothing, would hand a table
// built by filtering, and the table would then pass while covering not one
// route. Without this case the guard is a claim about the guard.
func TestPartitionMountedRoutesRefusesAVacuousTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		patterns   []string
		exclusions map[string]string
		wantErr    string
	}{
		{
			name:     "an empty enumeration",
			patterns: nil,
			wantErr:  "no mounted patterns",
		},
		{
			name:     "an empty enumeration, non-nil",
			patterns: []string{},
			wantErr:  "no mounted patterns",
		},
		{
			name:       "every route excluded",
			patterns:   []string{"GET /a", "GET /b"},
			exclusions: map[string]string{"GET /a": "reason", "GET /b": "reason"},
			wantErr:    "would cover nothing",
		},
		{
			name:       "an exclusion for a route that is not mounted",
			patterns:   []string{"GET /a"},
			exclusions: map[string]string{"GET /b": "reason"},
			wantErr:    "does not mount",
		},
		{
			name:     "a duplicated pattern",
			patterns: []string{"GET /a", "GET /a"},
			wantErr:  "enumerated twice",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			part, err := partitionMountedRoutes(tc.patterns, tc.exclusions)
			if err == nil {
				t.Fatalf("partition accepted %v with %d probed route(s); want a refusal mentioning %q",
					tc.patterns, len(part.Probed), tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}

	// And it accepts a well-formed partition, so the guard is not simply
	// refusing everything handed to it.
	part, err := partitionMountedRoutes([]string{"GET /a", "GET /b"}, map[string]string{"GET /b": "reason"})
	if err != nil {
		t.Fatalf("partition refused a well-formed enumeration: %v", err)
	}
	if len(part.Probed) != 1 || part.Probed[0] != "GET /a" {
		t.Errorf("probed = %v, want [GET /a]", part.Probed)
	}
	if len(part.Excluded) != 1 || part.Excluded[0] != "GET /b" {
		t.Errorf("excluded = %v, want [GET /b]", part.Excluded)
	}
}

// TestFaultProbeRoutesAreServedWhileHealthy is the control.
//
// Without it the table above would pass against a server that answered 500 to
// everything for some reason of its own: a probe document the handler rejects,
// a path shape no route matches, a wildcard substitution that lands outside the
// mount. Each of those produces a status this suite would happily accept, and
// the test would be measuring its own fixtures rather than the error contract.
//
// So every probed route is driven a second time with the fault DISARMED, and
// none of them may answer 5xx. What a healthy route does answer varies by
// route and is not asserted here: a 404 for a resource nothing seeded is
// correct, and so is a 200, a 201 or a 204.
func TestFaultProbeRoutesAreServedWhileHealthy(t *testing.T) {
	t.Parallel()

	stores, fault, device := faultyStores(t)
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	part, err := partitionMountedRoutes(patterns, storeFreeRoutes)
	if err != nil {
		t.Fatalf("route coverage: %v", err)
	}

	fault.Disarm()

	for _, pattern := range part.Probed {
		device.ensure(t)
		req, err := probeRequestFor(srv.URL, pattern)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		status := resp.StatusCode
		_ = resp.Body.Close()

		if status >= 500 {
			t.Errorf("%s answered %d against a HEALTHY store; the fault probe for this route proves nothing, "+
				"because the route answers 5xx whatever the store is doing", pattern, status)
		}
	}
}
