package assembly_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A store handle holding a NIL POINTER must read as "function set not
// wired".
//
// This is the one behaviour change interface-typing the store handles could
// have made and did make, before the mount gates were rewritten to ask
// store.IsAbsent. A consumer builds its stores by naming the fields it serves
// and leaving out the ones it does not, then copies that struct into
// assembly.Stores field by field. Under concrete pointer fields the omitted
// field arrived as a nil pointer and the gate unmounted the function set. Under
// interface fields the very same assignment arrives as an interface whose type
// half is set, which is not equal to nil, so the routes mount over a handle
// that panics on first use: a 500 from a route the deployment never asked for.
//
// It was found by the WADL conformance sweep rather than by this package: the
// reference server's own test harness leaves Registrations unset, so the whole
// /edev family answered EOF. That is why this test exists at the level it does,
// with a nil pointer of the concrete type rather than a nil interface literal;
// a nil interface was never the failing case.
func TestTypedNilStoreHandleReadsAsUnwired(t *testing.T) {
	t.Parallel()

	stores := testStores()

	// Exactly what a consumer that does not serve these produces: the zero
	// value of the concrete type it would otherwise have constructed.
	var noRegistrations *memory.RegistrationStore
	var noLogEvents *memory.ScopedStore[sep2.LogEvent]
	var noFlowReservations *memory.ScopedStore[sep2.FlowReservationRequest]
	stores.Registrations = noRegistrations
	stores.LogEvents = noLogEvents
	stores.FlowReservationRequests = noFlowReservations

	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)

	// The routes are not mounted at all, which is the section 4.4 p.19
	// requirement and the pre-change behaviour.
	for _, unwanted := range []string{
		"GET /edev/{id}/rg",
		"GET /edev/{id}/lel",
		"POST /edev/{id}/lel",
		"GET /edev/{id}/frq",
	} {
		if slices.Contains(patterns, unwanted) {
			t.Errorf("%q is mounted over a nil store handle", unwanted)
		}
	}

	// And a request to one answers 404 from the mux rather than panicking
	// inside a handler, which is what an EOF on the wire looks like to a
	// client. The /edev family itself must still work: only the function sets
	// whose handles were cleared are gone.
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/edev/0/lel")
	if err != nil {
		t.Fatalf("GET /edev/0/lel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edev/0/lel = %d, want 404 from an unmounted route", resp.StatusCode)
	}

	list, err := http.Get(srv.URL + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev = %d, want 200: clearing one function set must not break the EndDevice list",
			list.StatusCode)
	}

	// The EndDevices served through the nil-Registrations path carry no
	// RegistrationLink, which is the same section 4.4 p.19 rule stated on the
	// serving side: a link is advertised only for a function set that is wired.
	var devices sep2.EndDeviceList
	decodeXML(t, list, &devices)
	for i, d := range devices.EndDevice {
		if d.RegistrationLink != nil {
			t.Errorf("EndDevice[%d] advertises a RegistrationLink while the Registration store is unwired", i)
		}
	}
}
