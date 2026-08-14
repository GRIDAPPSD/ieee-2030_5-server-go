package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The EndDevice and its Registration are advertised and served as a pair,
// or neither.
//
// The defect these tests close: RegistrationLink was stamped on every
// EndDevice while nothing ever wrote a Registration record, so GET
// /edev/{id}/rg answered a handler 404 on a route that was mounted. Devi
// found it in the live conformance sweep on 2026-08-02, on both a POSTed
// device and a boot-seeded one, which is why both paths are exercised here.
//
// WHAT THESE TESTS ARE CAREFUL ABOUT. Not one of them seeds a Registration.
// A test that seeded one and then fetched it would pass whether or not
// creation is coupled, so it would prove nothing about the property the card
// is actually about. The Registration each test fetches exists only because
// the EndDevice was created.
//
// WHY assembly.AssertMintableHrefs DOES NOT COVER THIS. That assertion walks
// mintable hrefs and checks each resolves to a registered route pattern.
// "/edev/1/rg" always did. The defect was on the other axis: the route was
// mounted and the resource was absent. The assertion closes the routing half
// of the advertised-but-broken class and leaves the population half open,
// and these tests are the population half.

// pinnedRegistrationTime is an arbitrary fixed dateTimeRegistered so tests
// can assert an exact field value rather than "not zero".
const pinnedRegistrationTime int64 = 1500000000

// unprovisionedLFDI is a device the test policy declines to give a pIN,
// so it exercises the fail-closed branch: no Registration, and therefore no
// RegistrationLink either.
const unprovisionedLFDI = "00000000000000000000000000000000DEADBEEF"

// bindingTestStores returns a Stores whose EndDevice store is ALREADY bound
// to its Registration store, which is the shape an embedder that seeds
// devices at boot must build: seeding happens before the router exists, so
// the coupling has to be in the store the seeding writes through.
//
// It also returns the raw Registration store, so a test can construct the
// no-Registration state directly rather than through any handler.
func bindingTestStores(t *testing.T) (*assembly.Stores, store.ResourceStore[sep2.Registration], store.EndDeviceStore) {
	t.Helper()

	stores := testStores()
	regs := stores.Registrations
	policy := memory.RegistrationPolicy{
		PIN: func(lfdi string) (uint32, bool) {
			if lfdi == unprovisionedLFDI || lfdi == "" {
				return 0, false
			}
			return testFixturePIN, true
		},
	}
	bound := memory.NewRegisteredEndDeviceStore(stores.EndDevices, regs, policy)
	stores.EndDevices = bound
	stores.RegistrationPolicy = policy
	return stores, regs, bound
}

// bindingTestServer wires a router over a pre-bound Stores and runs seed
// against the bound EndDevice store BEFORE the router is built, which is the
// boot-fixture provisioning path.
func bindingTestServer(t *testing.T, seed func(ctx context.Context, edevs store.EndDeviceStore)) (*httptest.Server, store.ResourceStore[sep2.Registration]) {
	t.Helper()

	stores, regs, bound := bindingTestStores(t)
	if seed != nil {
		seed(context.Background(), bound)
	}
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, indexTestPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, regs
}

// assertServedRegistration decodes a Registration from the wire and asserts
// every field a client depends on. Per [[data-invariants]] Rule 1 this
// checks the values, not merely that the fetch returned 200: a Registration
// with a zero pIN is schema-valid and useless, which is exactly the shape a
// non-crash assertion would wave through.
func assertServedRegistration(t *testing.T, body, wantHref string) {
	t.Helper()

	var reg sep2.Registration
	if err := xml.Unmarshal([]byte(body), &reg); err != nil {
		t.Fatalf("decode Registration: %v; body=%s", err, body)
	}
	if reg.Href != wantHref {
		t.Errorf("Registration.Href = %q, want %q", reg.Href, wantHref)
	}
	if reg.PIN != testFixturePIN {
		t.Errorf("Registration.pIN is not the provisioned value: got %d, want the value the policy supplied", reg.PIN)
	}
	if reg.PollRate != memory.DefaultRegistrationPollRate {
		t.Errorf("Registration.pollRate = %d, want %d", reg.PollRate, memory.DefaultRegistrationPollRate)
	}
	if reg.DateTimeRegistered == 0 {
		t.Error("Registration.dateTimeRegistered is 0; sep.xsd makes it minOccurs=1 and a client prioritizes providers by it")
	}
	// pollRate is an ATTRIBUTE in sep.xsd (line 190), not an element, and
	// omitempty on a zero value would drop it silently. Assert the bytes.
	if !strings.Contains(body, `pollRate="900"`) {
		t.Errorf("served bytes carry no pollRate attribute; body=%s", body)
	}
}

// TestRegistrationBinding_POSTedEndDeviceServesItsRegistration is the
// create-then-fetch pairing (verdict class C3 SCENARIO) for the
// self-registration path: POST /edev, then follow the RegistrationLink the
// server just advertised. Nothing seeds a Registration.
func TestRegistrationBinding_POSTedEndDeviceServesItsRegistration(t *testing.T) {
	t.Parallel()

	srv, _ := bindingTestServer(t, nil)
	dev := register(t, srv, deviceLFDIA)

	if dev.RegistrationLink == nil {
		t.Fatal("POST /edev returned a device with no RegistrationLink")
	}
	if dev.RegistrationLink.Href != "/edev/1/rg" {
		t.Fatalf("RegistrationLink.Href = %q, want %q", dev.RegistrationLink.Href, "/edev/1/rg")
	}

	status, body := do(t, srv, http.MethodGet, dev.RegistrationLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", dev.RegistrationLink.Href, status, body)
	}
	assertServedRegistration(t, body, "/edev/1/rg")
}

// TestRegistrationBinding_BootFixtureEndDeviceServesItsRegistration is the
// same pairing for the other supported creation path: an embedder writing
// EndDevices straight into the store at boot, with no HTTP request involved
// and, again, no Registration seeded.
func TestRegistrationBinding_BootFixtureEndDeviceServesItsRegistration(t *testing.T) {
	t.Parallel()

	srv, _ := bindingTestServer(t, func(ctx context.Context, edevs store.EndDeviceStore) {
		enabled := true
		dev := sep2.EndDevice{Enabled: &enabled, LFDI: deviceLFDIA, SFDI: deviceSFDIA}
		dev.Href = "/edev/7"
		if err := edevs.Create(ctx, "7", dev); err != nil {
			t.Fatalf("seed EndDevice: %v", err)
		}
	})

	// The device the client discovers must advertise the link, and the link
	// must resolve. Read the EndDevice off the wire rather than trusting the
	// value seeded, because the served bytes are what a client acts on.
	status, body := do(t, srv, http.MethodGet, "/edev/7", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/7: status %d, want 200; body=%s", status, body)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal([]byte(body), &dev); err != nil {
		t.Fatalf("decode EndDevice: %v; body=%s", err, body)
	}
	if dev.RegistrationLink == nil {
		t.Fatal("boot-seeded EndDevice is served with no RegistrationLink")
	}
	if dev.RegistrationLink.Href != "/edev/7/rg" {
		t.Fatalf("RegistrationLink.Href = %q, want %q", dev.RegistrationLink.Href, "/edev/7/rg")
	}

	status, body = do(t, srv, http.MethodGet, dev.RegistrationLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", dev.RegistrationLink.Href, status, body)
	}
	assertServedRegistration(t, body, "/edev/7/rg")
}

// TestRegistrationBinding_AbsentRegistrationSuppressesTheLink builds the
// state the invariant forbids and asserts the server refuses to publish it.
//
// The Registration is removed straight from the store, behind every handler,
// which is the only way to reach a state no supported creation path can
// produce. IEEE 2030.5-2018 section 4.4 p.19: "If a function set is not
// implemented, Link elements to resources in that function set SHALL NOT be
// included." The assertion is on the served BYTES, both on the device
// resource and in the list a client walks to discover it, because a link
// stripped from one and left in the other is still a link a client follows
// into a 404.
func TestRegistrationBinding_AbsentRegistrationSuppressesTheLink(t *testing.T) {
	t.Parallel()

	srv, regs := bindingTestServer(t, nil)
	dev := register(t, srv, deviceLFDIA)
	if dev.RegistrationLink == nil {
		t.Fatal("precondition: the device did not advertise a RegistrationLink to begin with")
	}

	if err := regs.Delete(context.Background(), "1"); err != nil {
		t.Fatalf("remove the Registration directly from the store: %v", err)
	}

	status, body := do(t, srv, http.MethodGet, "/edev/1", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/1: status %d, want 200; body=%s", status, body)
	}
	if strings.Contains(body, "<RegistrationLink") {
		t.Errorf("EndDevice with no Registration still advertises one; body=%s", body)
	}

	status, body = do(t, srv, http.MethodGet, "/edev", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev: status %d, want 200; body=%s", status, body)
	}
	if strings.Contains(body, "<RegistrationLink") {
		t.Errorf("EndDeviceList entry with no Registration still advertises one; body=%s", body)
	}

	// And the route itself still answers honestly rather than synthesizing
	// a resource to make the link work.
	status, body = do(t, srv, http.MethodGet, "/edev/1/rg", deviceLFDIA)
	if status != http.StatusNotFound {
		t.Errorf("GET /edev/1/rg after removing the record: status %d, want 404; body=%s", status, body)
	}
}

// TestRegistrationBinding_UnprovisionedDeviceAdvertisesNothing covers the
// other direction of the same invariant: a device the policy gives no pIN
// gets no Registration, and therefore must not advertise one. This is the
// fail-closed default, and it is what a server with no PIN resolver wired
// does for every device.
func TestRegistrationBinding_UnprovisionedDeviceAdvertisesNothing(t *testing.T) {
	t.Parallel()

	srv, _ := bindingTestServer(t, func(ctx context.Context, edevs store.EndDeviceStore) {
		enabled := true
		dev := sep2.EndDevice{Enabled: &enabled, LFDI: unprovisionedLFDI, SFDI: "0000000000000000"}
		dev.Href = "/edev/4"
		if err := edevs.Create(ctx, "4", dev); err != nil {
			t.Fatalf("seed unprovisioned EndDevice: %v", err)
		}
	})

	status, body := do(t, srv, http.MethodGet, "/edev/4", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/4: status %d, want 200; body=%s", status, body)
	}
	if strings.Contains(body, "<RegistrationLink") {
		t.Errorf("unprovisioned EndDevice advertises a RegistrationLink it cannot serve; body=%s", body)
	}
}

// TestRegistrationBinding_ClientCannotForgeTheLinkByPUT asserts a client
// cannot talk the server into advertising a Registration by writing the link
// onto its own record. Whether the server holds a Registration is the
// server's fact, not the client's claim, and a forged link would put every
// other client that walks the list into a 404.
func TestRegistrationBinding_ClientCannotForgeTheLinkByPUT(t *testing.T) {
	t.Parallel()

	srv, regs := bindingTestServer(t, nil)
	register(t, srv, deviceLFDIA)
	if err := regs.Delete(context.Background(), "1"); err != nil {
		t.Fatalf("remove the Registration directly from the store: %v", err)
	}

	forged := `<EndDevice xmlns="urn:ieee:std:2030.5:ns">` +
		`<RegistrationLink href="/edev/1/rg"/>` +
		`<sFDI>` + deviceSFDIA + `</sFDI>` +
		`</EndDevice>`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/edev/1", strings.NewReader(forged))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("X-Test-LFDI", deviceLFDIA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /edev/1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /edev/1: status %d, want 204", resp.StatusCode)
	}

	status, body := do(t, srv, http.MethodGet, "/edev/1", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/1: status %d, want 200; body=%s", status, body)
	}
	if strings.Contains(body, "<RegistrationLink") {
		t.Errorf("a client-supplied RegistrationLink survived onto the wire; body=%s", body)
	}
}

// TestRegistrationBinding_PinnedTimestampReachesTheWire asserts the
// dateTimeRegistered a client reads is the one the store stamped, not a
// value the encoder invented or dropped. Fixing the clock is what lets this
// be an equality assertion rather than a range check.
func TestRegistrationBinding_PinnedTimestampReachesTheWire(t *testing.T) {
	t.Parallel()

	stores := testStores()
	bound := memory.NewRegisteredEndDeviceStore(stores.EndDevices, stores.Registrations, memory.RegistrationPolicy{
		PIN:      func(string) (uint32, bool) { return testFixturePIN, true },
		PollRate: 300,
	})
	memory.SetRegistrationClockForTest(bound, func() int64 { return pinnedRegistrationTime })
	stores.EndDevices = bound

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, indexTestPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	dev := register(t, srv, deviceLFDIA)
	if dev.RegistrationLink == nil {
		t.Fatal("POST /edev returned a device with no RegistrationLink")
	}

	status, body := do(t, srv, http.MethodGet, dev.RegistrationLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", dev.RegistrationLink.Href, status, body)
	}
	var reg sep2.Registration
	if err := xml.Unmarshal([]byte(body), &reg); err != nil {
		t.Fatalf("decode Registration: %v; body=%s", err, body)
	}
	if reg.DateTimeRegistered != pinnedRegistrationTime {
		t.Errorf("Registration.dateTimeRegistered = %d, want %d", reg.DateTimeRegistered, pinnedRegistrationTime)
	}
	if reg.PollRate != 300 {
		t.Errorf("Registration.pollRate = %d, want the policy value 300", reg.PollRate)
	}
	if !strings.Contains(body, `pollRate="300"`) {
		t.Errorf("the policy pollRate did not reach the wire; body=%s", body)
	}
}
