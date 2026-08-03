package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// Identity the mirror tests present at the TLS seam. Shared so the record a
// fixture stores and the caller a request carries cannot silently disagree,
// which under the section 10.11.3 rule (e) ownership gate reads as a 403 that
// looks like an unrelated failure.
const (
	testSFDI = "TEST_SFDI_12"
	testLFDI = "TEST_LFDI_40CHARS_AABBCCDD00112233445566"
)

// authLFDIProvider wraps auth.GetIdentity as a coremetering.LFDIProvider.
// Stays server-side because the auth package is server-stay; this wiring
// is exactly what the test exercises (server auth context -> core handler).
func authLFDIProvider(ctx context.Context) (string, bool) {
	id, ok := auth.GetIdentity(ctx)
	return id.LFDI, ok
}

// noPostRatePreference is this server's postRate policy, expressed as a
// coremetering.PostRateProvider: this deployment states no preferred posting
// rate, for any client.
//
// This is a settled policy, NOT an unfinished stub awaiting real config.
// sep.xsd:6485-6488 declares MirrorUsagePoint.postRate with minOccurs="0"
// and describes it as "POST rate, or how often mirrored data should be
// POSTed, in seconds. A client MAY indicate a preferred postRate when POSTing
// MirrorUsagePoint. A server MAY add or modify postRate to indicate its
// preferred posting rate." The element is optional and both the client side
// and the server side are MAY, so a server that expresses no posting-rate
// preference is fully conformant. Nothing is missing here.
//
// Returning ok=false is therefore not "unset the rate". Core reads a false
// second return as "leave whatever postRate the client supplied untouched,
// including none" (see coremetering.PostRateProvider). That distinction is
// the whole point of the signature: the spec lets a server "add OR modify" a
// client's stated preference, so the provider is a resolver of server opinion
// rather than a default-if-absent value, and declining to have an opinion
// leaves the client's own value alone rather than erasing it.
//
// This matches production: NewCoreRouterConfig (internal/server/assembly_seam.go)
// leaves assembly.RouterConfig.PostRateProvider nil, which core treats
// identically to a provider that always answers false. If this deployment
// ever grows a configured rate, this function and that config field are the
// two places it lands.
func noPostRatePreference(string) (uint32, bool) { return 0, false }

// TestHandleCreateMirrorUsagePoint pins the server-auth-to-core-handler
// wiring: the identity core stamps into the record is the one the auth
// context carried, not one the client claimed.
//
// The 201 response body is NOT where that is observed. IEEE 2030.5-2018
// section 10.11.3 rule (a)(3) has POST /mup answer 201 with a Location and
// no body, so the assertion follows the Location with a GET, which rule (c)
// permits to serve the stored record. That is also the path a real client
// takes, so it exercises one more link than reading a body that the spec
// says is empty.
func TestHandleCreateMirrorUsagePoint(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider, noPostRatePreference))
	mux.HandleFunc("GET /mup/{id}", coremetering.HandleMirrorUsagePoint(s, authLFDIProvider))

	mup := sep2.MirrorUsagePoint{
		MRID:                "INV001",
		Description:         "Inverter 1",
		ServiceCategoryKind: 0,
		Status:              1,
		// A client-claimed LFDI that core must overwrite. Sending the value
		// the server would have assigned anyway would let a handler that
		// trusts the body pass this test.
		DeviceLFDI: "CLIENT_CLAIMED_LFDI_DEADBEEF00112233445566",
	}
	body, _ := xml.Marshal(&mup)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req = addIdentity(req, testSFDI, testLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/mup/") {
		t.Fatalf("Location = %q, want /mup/...", loc)
	}

	getReq := httptest.NewRequest(http.MethodGet, loc, nil)
	getReq = addIdentity(getReq, testSFDI, testLFDI)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200, body: %s", loc, getW.Code, getW.Body.String())
	}

	var result sep2.MirrorUsagePoint
	if err := xml.Unmarshal(getW.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal served MirrorUsagePoint: %v", err)
	}
	if result.DeviceLFDI != testLFDI {
		t.Errorf("DeviceLFDI = %q, want cert value %q", result.DeviceLFDI, testLFDI)
	}
	if result.MRID != "INV001" {
		t.Errorf("mRID = %q, want INV001", result.MRID)
	}
	if result.Description != "Inverter 1" {
		t.Errorf("description = %q, want %q", result.Description, "Inverter 1")
	}

	// The postRate policy above declined to have an opinion, and this client
	// stated none either, so the served resource must carry no postRate at
	// all. Asserting on the served bytes rather than the decoded struct is
	// deliberate: postRate is *uint32 with omitempty, so a bug that wrote a
	// zero-second rate would decode to a non-nil pointer AND serialize as
	// <postRate>0</postRate>, and 0 seconds is not a valid posting interval.
	if strings.Contains(getW.Body.String(), "postRate") {
		t.Errorf("served bytes carry a postRate despite no server preference and none from the client:\n%s", getW.Body.String())
	}
}

func TestHandleMirrorUsagePointGet(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	// DeviceLFDI is the creator stamp core compares the caller against for
	// IEEE 2030.5-2018 section 10.11.3 rule (e); a record with none is
	// unreachable by design, so the fixture has to carry it.
	_ = s.Create(context.Background(), "test1", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/test1"},
		MRID:       "TEST1",
		DeviceLFDI: testLFDI,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", coremetering.HandleMirrorUsagePoint(s, authLFDIProvider))

	req := httptest.NewRequest(http.MethodGet, "/mup/test1", nil)
	req = addIdentity(req, testSFDI, testLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

// TestHandleMirrorUsagePointGetForeignCallerDenied pins the ownership gate
// itself, not just the happy path: a caller that did not create the mirror
// gets 403 and no record content. Without this, the identity argument added
// above could be wired to anything and the happy-path test would still pass.
func TestHandleMirrorUsagePointGetForeignCallerDenied(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	_ = s.Create(context.Background(), "test1", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/test1"},
		MRID:       "TEST1",
		DeviceLFDI: testLFDI,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", coremetering.HandleMirrorUsagePoint(s, authLFDIProvider))

	req := httptest.NewRequest(http.MethodGet, "/mup/test1", nil)
	req = addIdentity(req, "OTHER_SFDI_99", "OTHER_LFDI_40CHARS_99887766554433221100FF")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "TEST1") {
		t.Errorf("denial body leaked the stored mRID: %s", w.Body.String())
	}
}

func TestHandlePostMirrorMeterReading(t *testing.T) {
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	_ = mupStore.Create(context.Background(), "inv1", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/inv1"},
		MRID:       "INV1",
		DeviceLFDI: testLFDI,
	})

	val := int64(5000)
	uom := sep2.UomWatts
	mmr := sep2.MirrorMeterReading{
		MRID:        "MMR01",
		Description: "Active Power",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	}
	body, _ := xml.Marshal(&mmr)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", coremetering.HandlePostMirrorMeterReading(mupStore, mmrStore, authLFDIProvider))

	req := httptest.NewRequest(http.MethodPost, "/mup/inv1/mr", bytes.NewReader(body))
	req = addIdentity(req, testSFDI, testLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	// Verify stored
	count, _ := mmrStore.Count(context.Background(), "inv1")
	if count != 1 {
		t.Errorf("mirror meter readings count = %d, want 1", count)
	}
}

func TestHandlePostMirrorMeterReadingNotFoundParent(t *testing.T) {
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", coremetering.HandlePostMirrorMeterReading(mupStore, mmrStore, authLFDIProvider))

	req := httptest.NewRequest(http.MethodPost, "/mup/nonexistent/mr", bytes.NewBufferString("<MirrorMeterReading/>"))
	req = addIdentity(req, testSFDI, testLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// helpers

func addIdentity(req *http.Request, sfdi, lfdi string) *http.Request {
	// Inject device identity into context, simulating the IdentityMiddleware.
	// Stays server-side because auth.IdentityContextKey and auth.DeviceIdentity
	// are server-stay types (internal/auth).
	identity := auth.DeviceIdentity{SFDI: sfdi, LFDI: lfdi}
	ctx := context.WithValue(req.Context(), auth.IdentityContextKey(), identity)
	return req.WithContext(ctx)
}
