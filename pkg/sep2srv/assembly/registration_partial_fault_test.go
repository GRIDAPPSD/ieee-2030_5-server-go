package assembly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// A Registrations-store failure must not be reported to a client as "this
// device has no Registration".
//
// TestEveryMountedRouteReportsAStoreFailureAsAServerError in
// storefault_route_test.go arms ONE fault shared by every store, so on
// GET /edev the EndDevices store's own failure answers 500 first and the
// table never reaches the Registrations-only case. This file arms a fault
// on Registrations ALONE, with EndDevices left healthy: the shape Pike
// reproduced in the v0.14.0 pre-release pass, where GET /edev came back 200
// with RegistrationLink silently dropped while GET /edev/{id} came back 500
// for the very same underlying failure. Both symptoms traced to
// memory.RegisteredEndDeviceStore.deriveByHref, which GetBySFDI, GetByLFDI
// and List all route through: it logged the Registrations error, stripped
// the link, and returned a nil error. Get already propagated correctly;
// this file pins that List now agrees with it, and that a device with a
// genuine absence still serves 200.
//
// Under IEEE 2030.5-2018 section 4.4 p.19, an absent RegistrationLink is a
// positive assertion that the device has no Registration, so this is not
// cosmetic: a transient backend failure must not present to a conformant
// client as a permanent statement about the device.

// unprovisionedFaultLFDI names a device the fault-test registration policy
// declines to provision, so the companion test below has a device that is
// genuinely without a Registration rather than one made to look absent by
// happenstance.
const unprovisionedFaultLFDI = "11223344556677889900AABBCCDDEEFF0011223344"

// registrationFaultPolicy provisions every LFDI except
// unprovisionedFaultLFDI, so the same policy serves both the RED/GREEN test
// (where provisioning does not matter: the fault fires on any Registrations
// access) and the companion healthy-store test (where it must matter).
func registrationFaultPolicy() memory.RegistrationPolicy {
	return memory.RegistrationPolicy{
		PIN: func(lfdi string) (uint32, bool) {
			if lfdi == unprovisionedFaultLFDI || lfdi == "" {
				return 0, false
			}
			return testFixturePIN, true
		},
	}
}

// registrationFaultAuthPolicy is [testAuthPolicy] with the identity swapped
// out, so a test can choose which fixture device POST /edev provisions.
func registrationFaultAuthPolicy(lfdi string) assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(h http.Handler) http.Handler { return h },
		Identity: func(_ context.Context) (deviceLFDI, sfdi string, ok bool) {
			return lfdi, testSFDI, true
		},
		SFDIPrefix: func(sfdi string) (string, error) {
			return sfdi[:8], nil
		},
	}
}

// registrationFaultServer wires a router whose EndDevices store is healthy
// and whose Registrations store is wrapped in its OWN fault switch,
// independent of the shared one storefault_route_test.go uses. authLFDI is
// the identity POST /edev provisions the fixture device under.
func registrationFaultServer(t *testing.T, authLFDI string) (*httptest.Server, *storetest.Fault) {
	t.Helper()

	regsFault := &storetest.Fault{}
	stores := testStores()
	stores.EndDevices = memory.NewEndDeviceStore()
	stores.Registrations = storetest.NewFaultyResourceStore[sep2.Registration](memory.NewRegistrationStore(), regsFault)
	stores.RegistrationPolicy = registrationFaultPolicy()

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, registrationFaultAuthPolicy(authLFDI), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, regsFault
}

// postFixtureDevice provisions one EndDevice through POST /edev and fails
// the test if the store does not answer 201.
func postFixtureDevice(t *testing.T, base string) {
	t.Helper()
	resp, err := http.Post(base+"/edev", "application/sep+xml", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev = %d, want 201; body: %s", resp.StatusCode, body)
	}
}

// TestListReportsARegistrationsStoreFailureAsAServerError is the
// reproduction: GET /edev with only the Registrations store failing must
// answer 5xx, not 200 with the link silently stripped.
func TestListReportsARegistrationsStoreFailureAsAServerError(t *testing.T) {
	t.Parallel()

	srv, regsFault := registrationFaultServer(t, testLFDI)

	// Provision the fixture device while Registrations is healthy, so its
	// Href follows the real POST /edev addressing path rather than a
	// hand-built fixture.
	postFixtureDevice(t, srv.URL)

	regsFault.Arm(storetest.ErrBackendUnavailable)

	resp, err := http.Get(srv.URL + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode < 500 || resp.StatusCode > 599 {
		t.Fatalf("GET /edev = %d with only the Registrations store failing; want 5xx. "+
			"A non-5xx here reports a broken backend to the client as \"this device has no "+
			"Registration\", which IEEE 2030.5-2018 section 4.4 p.19 treats as a fact the client "+
			"acts on. body: %s", resp.StatusCode, body)
	}
}

// TestListServesAGenuinelyUnregisteredDeviceAs200WithTheLinkAbsent is the
// control on the fix above: a device the policy legitimately declines to
// provision, with Registrations HEALTHY, must still serve 200 with
// RegistrationLink absent. This is the invariant the fix must not break:
// propagating a real store error must not turn a real absence into an
// error too.
func TestListServesAGenuinelyUnregisteredDeviceAs200WithTheLinkAbsent(t *testing.T) {
	t.Parallel()

	srv, regsFault := registrationFaultServer(t, unprovisionedFaultLFDI)
	regsFault.Disarm() // explicit: this test is the healthy-store control

	postFixtureDevice(t, srv.URL)

	resp, err := http.Get(srv.URL + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("GET /edev = %d, want 200 for a healthy store; body: %s", resp.StatusCode, body)
	}

	var list sep2.EndDeviceList
	decodeXML(t, resp, &list)

	if len(list.EndDevice) != 1 {
		t.Fatalf("GET /edev returned %d device(s), want 1", len(list.EndDevice))
	}
	if list.EndDevice[0].RegistrationLink != nil {
		t.Errorf("a device the policy declined to provision advertises RegistrationLink %q; "+
			"2018 section 4.4 p.19 forbids a link to an unimplemented resource",
			list.EndDevice[0].RegistrationLink.Href)
	}
}
