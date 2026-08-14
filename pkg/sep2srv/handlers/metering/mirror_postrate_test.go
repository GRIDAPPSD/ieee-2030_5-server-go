package metering_test

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// postRateMUPMux wires the POST /mup and GET /mup/{id} pair against one
// store and one identity, so every test below can assert on the bytes a
// client actually receives from a follow-up GET rather than on the in-memory
// record. The 201 carries no body (section 10.11.3 rule (a)(3)), so the GET
// is the only place the served postRate is observable, which is exactly the
// surface a vendor client parses.
func postRateMUPMux(lfdi string, provider metering.PostRateProvider) *http.ServeMux {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, identityProvider(lfdi), provider))
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider(lfdi)))
	return mux
}

// createMUP POSTs a MirrorUsagePoint and returns the served bytes of the
// subsequent GET of that same resource.
func createMUP(t *testing.T, mux *http.ServeMux, mup sep2.MirrorUsagePoint) string {
	t.Helper()

	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal request MirrorUsagePoint: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /mup status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("POST /mup returned no Location header")
	}

	getReq := httptest.NewRequest(http.MethodGet, loc, nil)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body=%s", loc, getW.Code, getW.Body.String())
	}
	return getW.Body.String()
}

// TestCreateMirrorUsagePoint_ServerPostRateIsServed asserts that a mirror
// created by a client that stated NO postRate is served back carrying the
// server's configured rate. sep.xsd:6487 permits the server to "add"
// postRate; this is that path, and the assertion is on the served bytes
// because the served bytes are what the client's schema parser reads.
func TestCreateMirrorUsagePoint_ServerPostRateIsServed(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", func(string) (uint32, bool) {
		return 30, true
	})

	served := createMUP(t, mux, sep2.MirrorUsagePoint{
		MRID:        "INV001",
		Description: "Inverter 1",
	})

	if !strings.Contains(served, "<postRate>30</postRate>") {
		t.Errorf("served bytes lack <postRate>30</postRate>; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_ServerPostRateOverridesClient asserts the
// server's configured rate REPLACES a client-stated preference, and that the
// client's value appears nowhere in the served bytes.
//
// sep.xsd:6487 grants the server both verbs: "A server MAY add or modify
// postRate to indicate its preferred posting rate." A test that only proved
// the absent-value case would leave the "modify" half unasserted, which is
// the half a client can actually contradict.
func TestCreateMirrorUsagePoint_ServerPostRateOverridesClient(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", func(string) (uint32, bool) {
		return 30, true
	})

	clientRate := uint32(7200)
	served := createMUP(t, mux, sep2.MirrorUsagePoint{
		MRID:     "INV002",
		PostRate: &clientRate,
	})

	if !strings.Contains(served, "<postRate>30</postRate>") {
		t.Errorf("server rate did not override client rate; got:\n%s", served)
	}
	if strings.Contains(served, "7200") {
		t.Errorf("client-supplied postRate 7200 survived the server override; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_NilProviderPreservesClientPostRate is the
// no-op guard: a server configured with no rate policy must behave exactly
// as it did before PostRateProvider existed. It must NOT zero, drop, or
// invent a value. A client preference passes through untouched.
func TestCreateMirrorUsagePoint_NilProviderPreservesClientPostRate(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", nil)

	clientRate := uint32(7200)
	served := createMUP(t, mux, sep2.MirrorUsagePoint{
		MRID:     "INV003",
		PostRate: &clientRate,
	})

	if !strings.Contains(served, "<postRate>7200</postRate>") {
		t.Errorf("nil provider did not preserve the client's postRate; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_NilProviderOmitsAbsentPostRate asserts the
// other half of the no-op guard: with no server policy and no client value,
// postRate is absent from the served bytes entirely rather than serialized
// as a zero. postRate is minOccurs=0 (sep.xsd:6485), and 0 seconds would
// read as "post continuously", so emitting it would be a wire-visible
// behavior change for every existing consumer.
func TestCreateMirrorUsagePoint_NilProviderOmitsAbsentPostRate(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", nil)

	served := createMUP(t, mux, sep2.MirrorUsagePoint{MRID: "INV004"})

	if strings.Contains(served, "postRate") {
		t.Errorf("postRate present with no server policy and no client value; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_DecliningProviderPreservesClientPostRate
// asserts that a provider reporting ok=false is treated identically to a nil
// provider. This is the shape a per-device policy takes when it has an entry
// for some devices and none for this one: "no opinion" must never collapse
// into "rate zero".
func TestCreateMirrorUsagePoint_DecliningProviderPreservesClientPostRate(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", func(string) (uint32, bool) {
		return 0, false
	})

	clientRate := uint32(7200)
	served := createMUP(t, mux, sep2.MirrorUsagePoint{
		MRID:     "INV005",
		PostRate: &clientRate,
	})

	if !strings.Contains(served, "<postRate>7200</postRate>") {
		t.Errorf("declining provider did not preserve the client's postRate; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_PostRateProviderIsKeyedOnCallerLFDI asserts the
// provider receives the CREATING client's LFDI, the same identity stamped
// into DeviceLFDI. This is the seam that lets a per-device rate policy be
// added later by populating a map on the consumer side, with no change to
// this handler: if the handler ever stopped passing the caller's identity,
// a per-device policy would silently answer for the wrong device.
func TestCreateMirrorUsagePoint_PostRateProviderIsKeyedOnCallerLFDI(t *testing.T) {
	t.Parallel()

	var gotLFDI string
	mux := postRateMUPMux("DEVICE_A_LFDI", func(lfdi string) (uint32, bool) {
		gotLFDI = lfdi
		return 45, true
	})

	served := createMUP(t, mux, sep2.MirrorUsagePoint{MRID: "INV006"})

	if gotLFDI != "DEVICE_A_LFDI" {
		t.Errorf("provider received lfdi %q, want DEVICE_A_LFDI (the creating client)", gotLFDI)
	}
	if !strings.Contains(served, "<postRate>45</postRate>") {
		t.Errorf("per-caller rate not served; got:\n%s", served)
	}
	if !strings.Contains(served, "<deviceLFDI>DEVICE_A_LFDI</deviceLFDI>") {
		t.Errorf("deviceLFDI override disturbed; got:\n%s", served)
	}
}

// TestCreateMirrorUsagePoint_StampedPostRateKeepsElementOrder asserts the
// stamped postRate lands in sep.xsd sequence position, AFTER deviceLFDI,
// rather than wherever a client happened to place it. A schema-validating
// xs:sequence parser rejects the whole document on an out-of-order child,
// so stamping a value into the wrong position would trade a missing rate
// for an unparseable resource.
func TestCreateMirrorUsagePoint_StampedPostRateKeepsElementOrder(t *testing.T) {
	t.Parallel()

	mux := postRateMUPMux("TEST_LFDI_ABCDEF", func(string) (uint32, bool) {
		return 30, true
	})

	served := createMUP(t, mux, sep2.MirrorUsagePoint{
		MRID:        "INV007",
		Description: "Inverter 7",
		Status:      1,
	})

	// MirrorMeterReading is stripped on GET (rule (c)), so the surviving
	// order is mRID, description, status, deviceLFDI, postRate.
	for _, pair := range [][2]string{
		{"<mRID>", "<description>"},
		{"<description>", "<status>"},
		{"<status>", "<deviceLFDI>"},
		{"<deviceLFDI>", "<postRate>"},
	} {
		before := strings.Index(served, pair[0])
		after := strings.Index(served, pair[1])
		if before == -1 || after == -1 {
			t.Fatalf("served bytes missing %s or %s; got:\n%s", pair[0], pair[1], served)
		}
		if before > after {
			t.Errorf("%s appears after %s, violating sep.xsd sequence order; got:\n%s",
				pair[0], pair[1], served)
		}
	}
}
