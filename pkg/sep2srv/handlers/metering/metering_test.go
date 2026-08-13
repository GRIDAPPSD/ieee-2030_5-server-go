package metering_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// --- BuildUsagePointList ---

func TestBuildUsagePointList(t *testing.T) {
	t.Parallel()
	items := []sep2.UsagePoint{{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/upt/a"}}}}
	result := metering.BuildUsagePointList("/upt", store.ListResult[sep2.UsagePoint]{
		Items:   items,
		All:     3,
		Results: 1,
	}, 300)

	if result.Href != "/upt" {
		t.Errorf("Href = %q, want /upt", result.Href)
	}
	if result.All != 3 {
		t.Errorf("All = %d, want 3", result.All)
	}
	if len(result.UsagePoint) != 1 {
		t.Errorf("len(UsagePoint) = %d, want 1", len(result.UsagePoint))
	}
}

// --- HandleUsagePoint ---

func TestHandleUsagePoint_Get(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	_ = s.Create(context.Background(), "upt1", sep2.UsagePoint{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/upt/upt1"}}})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt/{uptId}", metering.HandleUsagePoint(s))

	req := httptest.NewRequest(http.MethodGet, "/upt/upt1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var upt sep2.UsagePoint
	_ = xml.Unmarshal(w.Body.Bytes(), &upt)
	if upt.Href != "/upt/upt1" {
		t.Errorf("Href = %q, want /upt/upt1", upt.Href)
	}
}

func TestHandleUsagePoint_NotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt/{uptId}", metering.HandleUsagePoint(s))

	req := httptest.NewRequest(http.MethodGet, "/upt/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- HandleCreateUsagePoint ---

func TestHandleCreateUsagePoint_Created(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(s))

	upt := sep2.UsagePoint{MRID: "UPT001"}
	body, _ := xml.Marshal(&upt)

	req := httptest.NewRequest(http.MethodPost, "/upt", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// The id is derived from the mRID, not the mRID itself: an mRID is a
	// plain unvalidated string and this handler puts the id into a URI it
	// hands back. See TestHandleCreateUsagePoint_LocationIsBounded.
	wantHref := metering.UsagePointHref(metering.UsagePointStoreID("UPT001"))
	loc := w.Header().Get("Location")
	if loc != wantHref {
		t.Errorf("Location = %q, want %q", loc, wantHref)
	}

	var result sep2.UsagePoint
	if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Href != wantHref {
		t.Errorf("Href = %q, want %q", result.Href, wantHref)
	}
	if result.MeterReadingListLink == nil {
		t.Fatal("MeterReadingListLink is nil, want set")
	}
	if result.MeterReadingListLink.Href != wantHref+"/mr" {
		t.Errorf("MeterReadingListLink.Href = %q, want %q", result.MeterReadingListLink.Href, wantHref+"/mr")
	}

	// The record is retrievable at the id the Location names, so the derived
	// key and the served URI agree.
	stored, err := s.Get(context.Background(), strings.TrimPrefix(loc, "/upt/"))
	if err != nil {
		t.Fatalf("get stored UsagePoint at the id Location names: %v", err)
	}
	if stored.MRID != "UPT001" {
		t.Errorf("stored MRID = %q, want UPT001 preserved", stored.MRID)
	}
	if stored.Href != wantHref {
		t.Errorf("stored Href = %q, want %q", stored.Href, wantHref)
	}
}

func TestHandleCreateUsagePoint_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt", metering.HandleCreateUsagePoint(s))

	req := httptest.NewRequest(http.MethodGet, "/upt", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// --- HandleReadingType ---

func TestHandleReadingType_Get(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.ReadingType]()
	uom := sep2.UomWatts
	_ = s.Create(context.Background(), "rt1", sep2.ReadingType{
		Resource: sep2.Resource{Href: "/rt/rt1"},
		Uom:      &uom,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /rt/{id}", metering.HandleReadingType(s))

	req := httptest.NewRequest(http.MethodGet, "/rt/rt1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var rt sep2.ReadingType
	_ = xml.Unmarshal(w.Body.Bytes(), &rt)
	if rt.Href != "/rt/rt1" {
		t.Errorf("Href = %q, want /rt/rt1", rt.Href)
	}
}

// --- HandleCreateMirrorUsagePoint ---

func identityProvider(lfdi string) metering.LFDIProvider {
	return func(_ context.Context) (string, bool) {
		return lfdi, lfdi != ""
	}
}

func TestHandleCreateMirrorUsagePoint_Created(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, identityProvider("TEST_LFDI_ABCDEF"), nil))

	mup := sep2.MirrorUsagePoint{
		MRID:        "INV001",
		Description: "Inverter 1",
	}
	body, _ := xml.Marshal(&mup)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// The id is server-assigned and derived from (creating device, client
	// mRID), not the raw client mRID: see MirrorStoreID. Asserting the derived
	// value rather than a literal keeps this test honest about what the client
	// is handed while still pinning an exact string.
	loc := w.Header().Get("Location")
	wantLoc := "/mup/" + metering.MirrorStoreID("TEST_LFDI_ABCDEF", "INV001")
	if loc != wantLoc {
		t.Errorf("Location = %q, want %q", loc, wantLoc)
	}
	if loc == "/mup/INV001" {
		t.Error("Location echoes the client mRID as the resource id; identity must be per owning device")
	}

	// IEEE 2030.5-2018 section 10.11.3 rule (a)(3) mandates only the
	// Location header on 201, not a body: the EPRI client never reads a
	// POST response body (retrieve.c process_response follows Location
	// with a fresh GET instead). The 201 response body is empty; assert
	// the created record's DeviceLFDI via the store, which is what the
	// client's follow-up GET would actually observe.
	if w.Body.Len() != 0 {
		t.Errorf("201 body = %q, want empty (Location header is the sole spec-mandated carrier)", w.Body.String())
	}

	stored, err := s.Get(context.Background(), strings.TrimPrefix(loc, "/mup/"))
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != "TEST_LFDI_ABCDEF" {
		t.Errorf("stored DeviceLFDI = %q, want TEST_LFDI_ABCDEF (from provider)", stored.DeviceLFDI)
	}
	if stored.MRID != "INV001" {
		t.Errorf("stored MRID = %q, want INV001 preserved verbatim in the resource", stored.MRID)
	}
	if stored.Href != wantLoc {
		t.Errorf("stored Href = %q, want %q (must agree with the Location served)", stored.Href, wantLoc)
	}
}

// TestHandleCreateMirrorUsagePoint_InlineReadingsAreServerStamped asserts that
// the inline MirrorMeterReading slice on a POSTed MirrorUsagePoint gets the same
// server-side overrides the out-of-band POST /mup/{id}/mr path applies.
//
// Each inline element embeds Resource, so href is client-supplied, and
// lastUpdateTime is a plain client-writable field. /mup is NOT /edev-scoped, so
// no ownership middleware runs on it, and GET /mup echoes the whole list back to
// every reader. Storing either field verbatim therefore lets one device plant a
// chosen href and a forged reading timestamp onto records other devices read.
// The sibling endpoint HandlePostMirrorMeterReading overwrites both
// (mirror.go: mmr.Href and mmr.LastUpdateTime); the inline path must not be the
// asymmetric hole that bypasses them.
func TestHandleCreateMirrorUsagePoint_InlineReadingsAreServerStamped(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, identityProvider("TEST_LFDI_ABCDEF"), nil))

	const forgedHref = "/mup/VICTIM/mr/00000000000000000001"
	const forgedTime = int64(1)
	valA, valB := int64(1000), int64(2000)
	mup := sep2.MirrorUsagePoint{
		MRID:       "INV001",
		DeviceLFDI: "CLIENT_SUPPLIED_LFDI",
		MirrorMeterReading: []sep2.MirrorMeterReading{
			{
				Resource:       sep2.Resource{Href: forgedHref},
				MRID:           "MMR_A",
				LastUpdateTime: forgedTime,
				Reading:        &sep2.Reading{Value: &valA},
			},
			{
				Resource:       sep2.Resource{Href: forgedHref},
				MRID:           "MMR_B",
				LastUpdateTime: forgedTime,
				Reading:        &sep2.Reading{Value: &valB},
			},
		},
	}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	before := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	after := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	parentID := metering.MirrorStoreID("TEST_LFDI_ABCDEF", "INV001")
	stored, err := s.Get(context.Background(), parentID)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != "TEST_LFDI_ABCDEF" {
		t.Errorf("stored DeviceLFDI = %q, want TEST_LFDI_ABCDEF (cert override)", stored.DeviceLFDI)
	}
	if len(stored.MirrorMeterReading) != 2 {
		t.Fatalf("stored MirrorMeterReading count = %d, want 2", len(stored.MirrorMeterReading))
	}

	seenHrefs := make(map[string]bool, len(stored.MirrorMeterReading))
	for i, got := range stored.MirrorMeterReading {
		if got.Href == forgedHref {
			t.Errorf("reading %d: stored Href = %q, client-supplied href persisted verbatim", i, got.Href)
		}
		// Match the shape HandlePostMirrorMeterReading mints:
		// "/mup/{parentID}/mr/{20-digit zero-padded id}", where parentID is
		// the server-assigned MirrorUsagePoint id, not the client mRID.
		prefix := "/mup/" + parentID + "/mr/"
		if !strings.HasPrefix(got.Href, prefix) {
			t.Errorf("reading %d: stored Href = %q, want prefix %q", i, got.Href, prefix)
		} else {
			id := strings.TrimPrefix(got.Href, prefix)
			if len(id) != 20 {
				t.Errorf("reading %d: href id segment %q has length %d, want 20 (%%020d nanos, matching the POST /mup/{id}/mr path)", i, id, len(id))
			}
			for _, c := range id {
				if c < '0' || c > '9' {
					t.Errorf("reading %d: href id segment %q is not all digits", i, id)
					break
				}
			}
		}
		if seenHrefs[got.Href] {
			t.Errorf("reading %d: stored Href %q collides with an earlier reading; ids must be distinct for sorted ordering", i, got.Href)
		}
		seenHrefs[got.Href] = true

		if got.LastUpdateTime == forgedTime {
			t.Errorf("reading %d: stored LastUpdateTime = %d, forged client value persisted verbatim", i, got.LastUpdateTime)
		}
		if got.LastUpdateTime < before || got.LastUpdateTime > after {
			t.Errorf("reading %d: stored LastUpdateTime = %d, want server clock in [%d, %d]", i, got.LastUpdateTime, before, after)
		}
	}

	// Client-owned payload must survive: the fix overrides the two
	// server-owned fields, it does not discard the reading itself.
	if stored.MirrorMeterReading[0].MRID != "MMR_A" || stored.MirrorMeterReading[1].MRID != "MMR_B" {
		t.Errorf("inline reading mRIDs = %q, %q; want MMR_A, MMR_B preserved in order",
			stored.MirrorMeterReading[0].MRID, stored.MirrorMeterReading[1].MRID)
	}
	if stored.MirrorMeterReading[0].Reading == nil || *stored.MirrorMeterReading[0].Reading.Value != valA {
		t.Errorf("inline reading 0 value not preserved: %+v", stored.MirrorMeterReading[0].Reading)
	}
	if stored.MirrorMeterReading[1].Reading == nil || *stored.MirrorMeterReading[1].Reading.Value != valB {
		t.Errorf("inline reading 1 value not preserved: %+v", stored.MirrorMeterReading[1].Reading)
	}
}

func TestHandleCreateMirrorUsagePoint_NoIdentity(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	noIdentity := metering.LFDIProvider(func(_ context.Context) (string, bool) {
		return "", false
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, noIdentity, nil))

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewBufferString("<MirrorUsagePoint/>"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// --- HandlePostMirrorMeterReading ---

func TestHandlePostMirrorMeterReading_Created(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	_ = mupStore.Create(context.Background(), "inv1", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/inv1"},
		MRID:       "INV1",
		DeviceLFDI: "DEVICE_A_LFDI",
	})

	val := int64(5000)
	uom := sep2.UomWatts
	const forgedHref = "/mup/VICTIM/mr/00000000000000000001"
	const forgedTime = int64(1)
	mmr := sep2.MirrorMeterReading{
		Resource:       sep2.Resource{Href: forgedHref},
		MRID:           "MMR01",
		Description:    "Active Power",
		LastUpdateTime: forgedTime,
		ReadingType:    &sep2.ReadingType{Uom: &uom},
		Reading:        &sep2.Reading{Value: &val},
	}
	body, _ := xml.Marshal(&mmr)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider("DEVICE_A_LFDI")))

	before := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/mup/inv1/mr", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	after := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	count, _ := mmrStore.Count(context.Background(), "inv1")
	if count != 1 {
		t.Fatalf("mirror meter readings count = %d, want 1", count)
	}

	// The Location header names the id the server minted, which is also the
	// store key. Assert on the STORED record: the response body here is empty.
	loc := w.Header().Get("Location")
	prefix := "/mup/inv1/mr/"
	if !strings.HasPrefix(loc, prefix) {
		t.Fatalf("Location = %q, want prefix %q", loc, prefix)
	}
	id := strings.TrimPrefix(loc, prefix)
	if len(id) != 20 {
		t.Errorf("minted id %q has length %d, want 20", id, len(id))
	}

	stored, err := mmrStore.Get(context.Background(), "inv1", id)
	if err != nil {
		t.Fatalf("get stored MirrorMeterReading %q: %v", id, err)
	}
	if stored.Href != loc {
		t.Errorf("stored Href = %q, want %q (server-synthesized, matching Location)", stored.Href, loc)
	}
	if stored.Href == forgedHref {
		t.Errorf("stored Href = %q, client-supplied href persisted verbatim", stored.Href)
	}
	if stored.LastUpdateTime == forgedTime {
		t.Errorf("stored LastUpdateTime = %d, forged client value persisted verbatim", stored.LastUpdateTime)
	}
	if stored.LastUpdateTime < before || stored.LastUpdateTime > after {
		t.Errorf("stored LastUpdateTime = %d, want server clock in [%d, %d]", stored.LastUpdateTime, before, after)
	}
	if stored.MRID != "MMR01" {
		t.Errorf("stored MRID = %q, want MMR01 preserved", stored.MRID)
	}
	if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != val {
		t.Errorf("stored Reading value not preserved: %+v", stored.Reading)
	}
}

func TestHandlePostMirrorMeterReading_NotFoundParent(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider("DEVICE_A_LFDI")))

	req := httptest.NewRequest(http.MethodPost, "/mup/nonexistent/mr", bytes.NewBufferString("<MirrorMeterReading/>"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- POST /mup/{id} (WADL-mandated Location-follow route) ---

// TestHandlePostMirrorMeterReading_ViaLocationHeader reproduces the exact
// sequence IEEE 2030.5-2018 section 10.11.3 rule (d) describes and the EPRI
// reference client performs: POST a MirrorUsagePoint, then POST the reading
// to the literal Location value the server returned (e.g. /mup/3), not to a
// separately-documented convention path. Before HandlePostMirrorMeterReading
// was mounted at "POST /mup/{id}" this returned 405, because only "GET
// /mup/{id}" existed at that pattern.
func TestHandlePostMirrorMeterReading_ViaLocationHeader(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, identityProvider("DEVICE_A_LFDI"), nil))
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(mupStore, identityProvider("DEVICE_A_LFDI")))
	mux.HandleFunc("POST /mup/{id}", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider("DEVICE_A_LFDI")))

	// Step 1: create the MirrorUsagePoint, exactly the client's first exchange.
	mup := sep2.MirrorUsagePoint{MRID: "INV001"}
	body, _ := xml.Marshal(&mup)
	createReq := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	createW := httptest.NewRecorder()
	mux.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body: %s", createW.Code, createW.Body.String())
	}
	loc := createW.Header().Get("Location")
	if loc == "" {
		t.Fatal("POST /mup: no Location header")
	}

	// Step 2: post a MirrorMeterReading to EXACTLY the Location value. The
	// client never constructs this path itself: it follows http_location().
	val := int64(4200)
	uom := sep2.UomWatts
	mmr := sep2.MirrorMeterReading{
		MRID:        "MMR01",
		Description: "Active Power",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	}
	mmrBody, _ := xml.Marshal(&mmr)
	postReq := httptest.NewRequest(http.MethodPost, loc, bytes.NewReader(mmrBody))
	postW := httptest.NewRecorder()
	mux.ServeHTTP(postW, postReq)

	if postW.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST %s returned 405: the server's own Location header is not accepted", loc)
	}
	if postW.Code != http.StatusCreated {
		t.Fatalf("POST %s status = %d, want 201, body: %s", loc, postW.Code, postW.Body.String())
	}

	// Assert the reading is actually stored under the parent id, not merely
	// that the POST returned 2xx.
	// The parent id is the server-assigned one the client read off Location,
	// never the client's own mRID.
	parentID := strings.TrimPrefix(loc, "/mup/")
	mmrLoc := postW.Header().Get("Location")
	prefix := "/mup/" + parentID + "/mr/"
	if !strings.HasPrefix(mmrLoc, prefix) {
		t.Fatalf("MirrorMeterReading Location = %q, want prefix %q", mmrLoc, prefix)
	}
	id := strings.TrimPrefix(mmrLoc, prefix)
	stored, err := mmrStore.Get(context.Background(), parentID, id)
	if err != nil {
		t.Fatalf("get stored MirrorMeterReading: %v", err)
	}
	if stored.MRID != "MMR01" {
		t.Errorf("stored MRID = %q, want MMR01", stored.MRID)
	}
	if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != val {
		t.Errorf("stored Reading value not preserved: %+v", stored.Reading)
	}

	// deviceLFDI override invariant: this route only ever mutates
	// MirrorMeterReading storage, never the parent MirrorUsagePoint record,
	// so the cert-derived DeviceLFDI stamped at creation time must be
	// unchanged by a reading POST through the new route.
	parent, err := mupStore.Get(context.Background(), parentID)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if parent.DeviceLFDI != "DEVICE_A_LFDI" {
		t.Errorf("parent DeviceLFDI = %q, want DEVICE_A_LFDI unchanged by the reading POST", parent.DeviceLFDI)
	}

	// Rule (c) regression check: GET /mup/{id} must still omit
	// MirrorMeterReading children after a reading has been posted through
	// the new route.
	getReq := httptest.NewRequest(http.MethodGet, loc, nil)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, getW.Code)
	}
	if strings.Contains(getW.Body.String(), "MirrorMeterReading") {
		t.Errorf("GET /mup/{id} served a MirrorMeterReading element after a reading was posted via POST /mup/{id}, violates rule (c); body=%s", getW.Body.String())
	}
}

func TestHandlePostMirrorMeterReading_ViaLocationHeader_NotFoundParent(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider("DEVICE_A_LFDI")))

	req := httptest.NewRequest(http.MethodPost, "/mup/nonexistent", bytes.NewBufferString("<MirrorMeterReading/>"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- /mup ownership, IEEE 2030.5-2018 section 10.11.3 rule (e) ---
//
// The suite below REPLACES TestHandlePostMirrorMeterReading_NoOwnershipCheck_KnownGap,
// which documented that neither POST route consulted an LFDIProvider and
// asserted a cross-device write returned 201. That gap is closed, so the test
// that recorded it is gone: keeping it would pin the vulnerability open. Its
// two jobs are both carried forward with the sign flipped:
//
//   - cross-device write behavior is now asserted as DENIED, by
//     TestHandlePostMirrorMeterReading_CrossDeviceWriteDenied;
//   - route parity between POST /mup/{id} and POST /mup/{id}/mr is still
//     proven rather than assumed, by exercising every case over both paths.

// assertNoBodyLeak fails if a denial response carries anything beyond a fixed
// plain-text reason. A denial must teach a prober nothing about the resource
// it was denied: no XML representation, no LFDI, no stored field values.
func assertNoBodyLeak(t *testing.T, w *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	body := w.Body.String()
	if strings.Contains(body, "<") {
		t.Errorf("denial body carries XML markup, want plain-text reason only: %q", body)
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "xml") {
		t.Errorf("denial Content-Type = %q, want no XML content type", ct)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(body, s) {
			t.Errorf("denial body leaks %q; body = %q", s, body)
		}
	}
}

// seedOwnedMirror stores a MirrorUsagePoint stamped with owner as its
// DeviceLFDI, matching what HandleCreateMirrorUsagePoint writes from the
// caller's certificate identity.
func seedOwnedMirror(t *testing.T, s *memory.Store[sep2.MirrorUsagePoint], id, mrid, owner string) {
	t.Helper()
	err := s.Create(context.Background(), id, sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/" + id},
		MRID:       mrid,
		DeviceLFDI: owner,
	})
	if err != nil {
		t.Fatalf("seed MirrorUsagePoint %q: %v", id, err)
	}
}

// bothPostRoutes returns the two request paths that reach
// HandlePostMirrorMeterReading for a given MirrorUsagePoint id. Every
// ownership case runs over both so the WADL-mandated POST /mup/{id} and the
// pre-existing POST /mup/{id}/mr are proven identical, not assumed so.
func bothPostRoutes(id string) []string {
	return []string{"/mup/" + id, "/mup/" + id + "/mr"}
}

func mirrorPostMux(
	mupStore *memory.Store[sep2.MirrorUsagePoint],
	mmrStore *memory.ScopedStore[sep2.MirrorMeterReading],
	caller string,
) *http.ServeMux {
	mux := http.NewServeMux()
	h := metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider(caller))
	mux.HandleFunc("POST /mup/{id}", h)
	mux.HandleFunc("POST /mup/{id}/mr", h)
	return mux
}

func readingBody(t *testing.T, mrid string, value int64) []byte {
	t.Helper()
	uom := sep2.UomWatts
	body, err := xml.Marshal(&sep2.MirrorMeterReading{
		MRID:        mrid,
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &value},
	})
	if err != nil {
		t.Fatalf("marshal MirrorMeterReading: %v", err)
	}
	return body
}

// TestHandlePostMirrorMeterReading_CrossDeviceWriteDenied asserts rule (e):
// "The Metering Mirror server SHOULD only accept POSTs to a given
// MirrorUsagePoint from the client that created the mirror." Device B holds a
// valid certificate but did not create device A's mirror, so both POST routes
// deny it, nothing is stored, and the denial body leaks nothing.
func TestHandlePostMirrorMeterReading_CrossDeviceWriteDenied(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	const attackerLFDI = "DEVICE_B_LFDI"

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, attackerLFDI)

			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "FORGED", 1)))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("POST %s as a non-creator: status = %d, want 403; body = %s", path, w.Code, w.Body.String())
			}
			// No body leak: no XML, neither LFDI, no stored identifier.
			assertNoBodyLeak(t, w, ownerLFDI, attackerLFDI, "DEVICE_A", "device-a")

			// The write must be refused, not merely reported as refused.
			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != 0 {
				t.Errorf("stored reading count = %d, want 0: a denied POST persisted data", count)
			}

			// The victim's own record is untouched, DeviceLFDI included.
			parent, err := mupStore.Get(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("get MirrorUsagePoint: %v", err)
			}
			if parent.DeviceLFDI != ownerLFDI {
				t.Errorf("parent DeviceLFDI = %q, want %q unchanged by the denied POST", parent.DeviceLFDI, ownerLFDI)
			}
			if len(parent.MirrorMeterReading) != 0 {
				t.Errorf("parent MirrorMeterReading count = %d, want 0", len(parent.MirrorMeterReading))
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_OwnerWriteAccepted is the positive half of
// the rule: the creating client still posts to its own mirror, on both routes,
// and the reading is actually stored with its value intact rather than merely
// acknowledged with a 2xx.
func TestHandlePostMirrorMeterReading_OwnerWriteAccepted(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, ownerLFDI)

			const val = int64(4200)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "MMR01", val)))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("POST %s as the creator: status = %d, want 201; body = %s", path, w.Code, w.Body.String())
			}

			loc := w.Header().Get("Location")
			const prefix = "/mup/device-a/mr/"
			if !strings.HasPrefix(loc, prefix) {
				t.Fatalf("Location = %q, want prefix %q (both routes mint the same href shape)", loc, prefix)
			}
			id := strings.TrimPrefix(loc, prefix)
			stored, err := mmrStore.Get(context.Background(), "device-a", id)
			if err != nil {
				t.Fatalf("get stored MirrorMeterReading %q: %v", id, err)
			}
			if stored.MRID != "MMR01" {
				t.Errorf("stored MRID = %q, want MMR01", stored.MRID)
			}
			if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != val {
				t.Errorf("stored Reading value not preserved: %+v", stored.Reading)
			}
			if stored.Href != loc {
				t.Errorf("stored Href = %q, want %q", stored.Href, loc)
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_AggregatorOwnsMultipleMirrors is the case a
// naive per-device rule breaks. Under CSIP an aggregator acts for many DERs
// and legitimately owns many mirrors. Because HandleCreateMirrorUsagePoint
// stamps each mirror with the CREATING caller's LFDI, all of an aggregator's
// mirrors carry the aggregator's LFDI and it retains write access to every one
// of them. Scope is by stamped creator; nothing assumes one mirror per
// certificate.
//
// The mirrors are created through the real POST /mup handler rather than
// seeded, so the test exercises the actual stamping path the rule depends on.
func TestHandlePostMirrorMeterReading_AggregatorOwnsMultipleMirrors(t *testing.T) {
	t.Parallel()
	const aggregatorLFDI = "AGGREGATOR_LFDI"
	const outsiderLFDI = "OUTSIDER_LFDI"

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, identityProvider(aggregatorLFDI), nil))
	aggPost := metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider(aggregatorLFDI))
	mux.HandleFunc("POST /mup/{id}", aggPost)
	mux.HandleFunc("POST /mup/{id}/mr", aggPost)

	// One aggregator certificate creates mirrors for two distinct DERs. The
	// server assigns each an id; the aggregator addresses them by the Location
	// it was handed, exactly as a client does.
	derMRIDs := []string{"DER_ONE", "DER_TWO"}
	mirrorIDs := make(map[string]string, len(derMRIDs))
	for _, mrid := range derMRIDs {
		body, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: mrid})
		if err != nil {
			t.Fatalf("marshal MirrorUsagePoint %q: %v", mrid, err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %q: status = %d, want 201; body = %s", mrid, w.Code, w.Body.String())
		}
		id := strings.TrimPrefix(w.Header().Get("Location"), "/mup/")
		mirrorIDs[mrid] = id
		stored, err := mupStore.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get created MirrorUsagePoint %q (id %q): %v", mrid, id, err)
		}
		// The scope key: every mirror carries the CREATOR's LFDI, so the
		// aggregator owns all of them at once.
		if stored.DeviceLFDI != aggregatorLFDI {
			t.Fatalf("mirror %q DeviceLFDI = %q, want %q (creator stamp)", mrid, stored.DeviceLFDI, aggregatorLFDI)
		}
	}
	if mirrorIDs[derMRIDs[0]] == mirrorIDs[derMRIDs[1]] {
		t.Fatalf("both mirrors got id %q: distinct mRIDs from one owner must stay distinct resources", mirrorIDs[derMRIDs[0]])
	}

	// The aggregator posts to every mirror it created, over both routes.
	for i, mrid := range derMRIDs {
		mirrorID := mirrorIDs[mrid]
		for _, path := range bothPostRoutes(mirrorID) {
			val := int64(100 * (i + 1))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "MMR_"+mrid, val))))
			if w.Code != http.StatusCreated {
				t.Fatalf("aggregator POST %s: status = %d, want 201; body = %s", path, w.Code, w.Body.String())
			}
			loc := w.Header().Get("Location")
			id := strings.TrimPrefix(loc, "/mup/"+mirrorID+"/mr/")
			stored, err := mmrStore.Get(context.Background(), mirrorID, id)
			if err != nil {
				t.Fatalf("get stored reading for %q: %v", mrid, err)
			}
			if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != val {
				t.Errorf("mirror %q: stored Reading value not preserved: %+v", mrid, stored.Reading)
			}
		}
	}

	// A different certificate is still shut out of the aggregator's mirrors:
	// broad ownership is earned by creation, not granted by aggregator status.
	outsider := metering.HandlePostMirrorMeterReading(mupStore, mmrStore, identityProvider(outsiderLFDI))
	outMux := http.NewServeMux()
	outMux.HandleFunc("POST /mup/{id}", outsider)
	for _, mrid := range derMRIDs {
		mirrorID := mirrorIDs[mrid]
		w := httptest.NewRecorder()
		outMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup/"+mirrorID, bytes.NewReader(readingBody(t, "FORGED", 1))))
		if w.Code != http.StatusForbidden {
			t.Errorf("outsider POST /mup/%s: status = %d, want 403", mirrorID, w.Code)
		}
		assertNoBodyLeak(t, w, aggregatorLFDI, outsiderLFDI, mrid)
	}
}

// TestHandlePostMirrorMeterReading_NoIdentityDenied: an unauthenticated caller
// is denied on both routes and nothing is stored. Fails closed rather than
// treating a missing identity as a wildcard.
func TestHandlePostMirrorMeterReading_NoIdentityDenied(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)

			// identityProvider("") reports ok=false, the shape the server's
			// AuthPolicy.Identity uses for an unauthenticated request.
			mux := mirrorPostMux(mupStore, mmrStore, "")

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "FORGED", 1))))

			if w.Code != http.StatusForbidden {
				t.Fatalf("POST %s with no identity: status = %d, want 403", path, w.Code)
			}
			assertNoBodyLeak(t, w, ownerLFDI, "DEVICE_A")

			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != 0 {
				t.Errorf("stored reading count = %d, want 0", count)
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_UnstampedMirrorDeniesEveryone: a
// MirrorUsagePoint with no recorded creator has nobody who can claim it, so it
// accepts no writes. Treating an empty stored DeviceLFDI as "matches anyone"
// would turn a missing value into open access, which is the fallback
// data-invariants Rule 2 forbids.
func TestHandlePostMirrorMeterReading_UnstampedMirrorDeniesEveryone(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	seedOwnedMirror(t, mupStore, "orphan", "ORPHAN", "")

	for _, caller := range []string{"SOME_LFDI", "OTHER_LFDI"} {
		mux := mirrorPostMux(mupStore, mmrStore, caller)
		for _, path := range bothPostRoutes("orphan") {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "FORGED", 1))))
			if w.Code != http.StatusForbidden {
				t.Errorf("caller %q POST %s on an unstamped mirror: status = %d, want 403", caller, path, w.Code)
			}
			assertNoBodyLeak(t, w, caller, "ORPHAN")
		}
	}

	count, err := mmrStore.Count(context.Background(), "orphan")
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 0 {
		t.Errorf("stored reading count = %d, want 0", count)
	}
}

// --- BuildMirrorUsagePointList ---

func TestBuildMirrorUsagePointList(t *testing.T) {
	t.Parallel()
	items := []sep2.MirrorUsagePoint{
		{Resource: sep2.Resource{Href: "/mup/a"}, MRID: "A"},
		{Resource: sep2.Resource{Href: "/mup/b"}, MRID: "B"},
	}
	result := metering.BuildMirrorUsagePointList("/mup", store.ListResult[sep2.MirrorUsagePoint]{
		Items:   items,
		All:     2,
		Results: 2,
	}, 300)

	if result.Href != "/mup" {
		t.Errorf("Href = %q, want /mup", result.Href)
	}
	if result.All != 2 {
		t.Errorf("All = %d, want 2", result.All)
	}
	if len(result.MirrorUsagePoint) != 2 {
		t.Errorf("len(MirrorUsagePoint) = %d, want 2", len(result.MirrorUsagePoint))
	}
}

// TestBuildMirrorUsagePointList_OmitsMirrorMeterReading asserts item 2:
// IEEE 2030.5-2018 section 10.11.3 rule (c) requires a GET of the
// MirrorUsagePoint to return "only the first level elements (i.e.,
// sub-elements and collections are not included)". MirrorMeterReading is
// minOccurs="0" maxOccurs="unbounded" on MirrorUsagePoint (sep.xsd:6472),
// a collection, so GET /mup must omit it even though a record with stored
// readings is being served.
func TestBuildMirrorUsagePointList_OmitsMirrorMeterReading(t *testing.T) {
	t.Parallel()
	items := []sep2.MirrorUsagePoint{
		{
			Resource: sep2.Resource{Href: "/mup/a"},
			MRID:     "A",
			MirrorMeterReading: []sep2.MirrorMeterReading{
				{MRID: "MMR01"},
			},
		},
	}
	result := metering.BuildMirrorUsagePointList("/mup", store.ListResult[sep2.MirrorUsagePoint]{
		Items:   items,
		All:     1,
		Results: 1,
	}, 300)

	if len(result.MirrorUsagePoint) != 1 {
		t.Fatalf("len(MirrorUsagePoint) = %d, want 1", len(result.MirrorUsagePoint))
	}
	if result.MirrorUsagePoint[0].MirrorMeterReading != nil {
		t.Errorf("MirrorUsagePoint[0].MirrorMeterReading = %+v, want nil (rule (c): sub-elements/collections excluded on GET)",
			result.MirrorUsagePoint[0].MirrorMeterReading)
	}

	data, err := xml.Marshal(&result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "MirrorMeterReading") {
		t.Errorf("served MirrorUsagePointList carries a MirrorMeterReading element; body=%s", data)
	}
}

// --- HandleMirrorUsagePoint (GET /mup/{id}) ---

// TestHandleMirrorUsagePoint_OmitsMirrorMeterReading covers the single-
// resource GET path (item 2's other named route). Storage is asserted
// separately (via the store) to confirm the omission is serving-only: the
// POST path continues to persist whatever MirrorMeterReading children the
// client submitted, only the GET response representation changes.
func TestHandleMirrorUsagePoint_OmitsMirrorMeterReading(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	val := int64(5000)
	stored := sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/inv1"},
		MRID:       "INV001",
		DeviceLFDI: "DEVICE_A_LFDI",
		MirrorMeterReading: []sep2.MirrorMeterReading{
			{MRID: "MMR01", Reading: &sep2.Reading{Value: &val}},
		},
	}
	if err := s.Create(context.Background(), "inv1", stored); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider("DEVICE_A_LFDI")))

	req := httptest.NewRequest(http.MethodGet, "/mup/inv1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "MirrorMeterReading") {
		t.Errorf("GET /mup/{id} served a MirrorMeterReading element, violates rule (c); body=%s", body)
	}
	if !strings.Contains(body, "<mRID>INV001</mRID>") {
		t.Errorf("GET /mup/{id} missing first-level mRID element; body=%s", body)
	}

	// Storage is untouched: item 2 changes serving, not what was stored.
	got, err := s.Get(context.Background(), "inv1")
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if len(got.MirrorMeterReading) != 1 {
		t.Errorf("stored MirrorMeterReading count = %d, want 1 (POST storage must be unaffected by GET-serving change)", len(got.MirrorMeterReading))
	}
	if got.MirrorMeterReading[0].MRID != "MMR01" {
		t.Errorf("stored MirrorMeterReading[0].MRID = %q, want MMR01", got.MirrorMeterReading[0].MRID)
	}
}

func TestHandleMirrorUsagePoint_NotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider("DEVICE_A_LFDI")))

	req := httptest.NewRequest(http.MethodGet, "/mup/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleMirrorUsagePoint_NonOwnerDenied: metering readings are customer
// data, and the WADL marks GET /mup/{id} Optional (section 4.2 item (c) makes
// those modes normative), so scoping the read to the creating client costs
// nothing in conformance. A holder of a valid certificate that did not create
// the mirror gets 403 with no representation at all: not the mRID, not the
// DeviceLFDI, not the description.
func TestHandleMirrorUsagePoint_NonOwnerDenied(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	const attackerLFDI = "DEVICE_B_LFDI"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	val := int64(5000)
	err := s.Create(context.Background(), "device-a", sep2.MirrorUsagePoint{
		Resource:    sep2.Resource{Href: "/mup/device-a"},
		MRID:        "DEVICE_A",
		Description: "Customer meter",
		DeviceLFDI:  ownerLFDI,
		MirrorMeterReading: []sep2.MirrorMeterReading{
			{MRID: "MMR01", Reading: &sep2.Reading{Value: &val}},
		},
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider(attackerLFDI)))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/device-a", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /mup/device-a as a non-creator: status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
	assertNoBodyLeak(t, w, ownerLFDI, attackerLFDI, "DEVICE_A", "Customer meter", "MMR01", "5000")
}

// TestHandleMirrorUsagePoint_NoIdentityDenied: an unauthenticated read fails
// closed, same as the write path.
func TestHandleMirrorUsagePoint_NoIdentityDenied(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	if err := s.Create(context.Background(), "device-a", sep2.MirrorUsagePoint{
		MRID:       "DEVICE_A",
		DeviceLFDI: "DEVICE_A_LFDI",
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider("")))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/device-a", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	assertNoBodyLeak(t, w, "DEVICE_A_LFDI", "DEVICE_A")
}

// TestHandleMirrorUsagePoint_OwnerServedWithRuleC: the creating client still
// gets its own record, and rule (c) still holds on it. Guards against a
// scoping change that quietly turns the read path off for everyone, and
// against the ownership gate being bolted on in a way that bypasses
// stripMirrorMeterReadings.
func TestHandleMirrorUsagePoint_OwnerServedWithRuleC(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	val := int64(5000)
	err := s.Create(context.Background(), "device-a", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/device-a"},
		MRID:       "DEVICE_A",
		DeviceLFDI: ownerLFDI,
		MirrorMeterReading: []sep2.MirrorMeterReading{
			{MRID: "MMR01", Reading: &sep2.Reading{Value: &val}},
		},
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider(ownerLFDI)))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/device-a", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("GET /mup/device-a as the creator: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "<mRID>DEVICE_A</mRID>") {
		t.Errorf("owner response missing first-level mRID element; body = %s", body)
	}
	// Rule (c): first-level elements only, no MirrorMeterReading children.
	if strings.Contains(body, "MirrorMeterReading") {
		t.Errorf("GET /mup/{id} served a MirrorMeterReading element, violates rule (c); body = %s", body)
	}

	// Scoping is serving-only: the stored record keeps its children.
	got, err := s.Get(context.Background(), "device-a")
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if len(got.MirrorMeterReading) != 1 {
		t.Errorf("stored MirrorMeterReading count = %d, want 1 (ownership gate must not mutate storage)", len(got.MirrorMeterReading))
	}
}

// --- Per-owner MirrorUsagePoint identity ---

// Two distinct certificates, both POSTing the same client mRID. Observed in
// the field: nine devices with nine certificates POSTed three distinct mRIDs
// between them, so most devices collided with a mirror another device had
// already created.
const (
	mupKeyLFDIA = "1111111111111111111111111111111111111111"
	mupKeyLFDIB = "2222222222222222222222222222222222222222"
)

// createMirrorMux mounts POST /mup for one caller identity over the shared
// store, so several identities can be driven against one store the way the
// real server does with several client certificates.
func createMirrorMux(s *memory.Store[sep2.MirrorUsagePoint], caller string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, identityProvider(caller), nil))
	return mux
}

// postMirror POSTs a MirrorUsagePoint carrying mrid and returns the recorder.
func postMirror(t *testing.T, h http.Handler, mrid string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: mrid, Description: "d"})
	if err != nil {
		t.Fatalf("marshal MirrorUsagePoint: %v", err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))
	return w
}

// assertUsableLocation asserts the Location header is a header a client can
// actually parse. The EPRI reference client's uri_parse takes strlen(href)
// with no NULL guard and http_parse_uri caps the URI at 127 bytes; a Location
// that is empty, over-long, or not a single absolute path makes get_resource
// return NULL, which retrieve.c:449 dereferences unguarded. Every success and
// every collision path is held to this.
func assertUsableLocation(t *testing.T, w *httptest.ResponseRecorder, what string) string {
	t.Helper()
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatalf("%s: Location header is empty", what)
	}
	if len(loc) >= 128 {
		t.Errorf("%s: Location = %q has length %d, want < 128 (client parses into a 127-byte buffer)", what, loc, len(loc))
	}
	if !strings.HasPrefix(loc, "/mup/") {
		t.Fatalf("%s: Location = %q, want a /mup/ path", what, loc)
	}
	id := strings.TrimPrefix(loc, "/mup/")
	if id == "" {
		t.Fatalf("%s: Location = %q carries an empty id segment", what, loc)
	}
	if strings.ContainsAny(id, "/? #%") {
		t.Errorf("%s: Location id segment %q carries characters that need escaping in a path", what, id)
	}
	return id
}

// TestHandleCreateMirrorUsagePoint_SameMRIDFromTwoDevicesStaysIsolated is the
// core ownership-isolation regression. Before per-owner keying, device B's
// POST collided with device A's record on the globally-keyed store and B was
// handed 200 plus Location pointing at A's MirrorUsagePoint, so B would then
// post its readings into A's resource.
func TestHandleCreateMirrorUsagePoint_SameMRIDFromTwoDevicesStaysIsolated(t *testing.T) {
	t.Parallel()
	const sharedMRID = "0123456789ABCDEF0123456789ABCDEF"

	s := memory.NewStore[sep2.MirrorUsagePoint]()

	wA := postMirror(t, createMirrorMux(s, mupKeyLFDIA), sharedMRID)
	if wA.Code != http.StatusCreated {
		t.Fatalf("device A POST /mup: status = %d, want 201; body = %s", wA.Code, wA.Body.String())
	}
	idA := assertUsableLocation(t, wA, "device A create")

	wB := postMirror(t, createMirrorMux(s, mupKeyLFDIB), sharedMRID)
	if wB.Code != http.StatusCreated {
		t.Fatalf("device B POST /mup with a mRID device A already used: status = %d, want 201 (B owns a separate mirror); body = %s",
			wB.Code, wB.Body.String())
	}
	idB := assertUsableLocation(t, wB, "device B create")

	if idA == idB {
		t.Fatalf("device B was handed device A's MirrorUsagePoint: both Locations resolve to /mup/%s", idA)
	}

	// Two records, each stamped with its own creator.
	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count mirrors: %v", err)
	}
	if count != 2 {
		t.Errorf("stored MirrorUsagePoint count = %d, want 2 (one per device)", count)
	}

	for _, tc := range []struct {
		id, owner, name string
	}{
		{idA, mupKeyLFDIA, "device A"},
		{idB, mupKeyLFDIB, "device B"},
	} {
		got, err := s.Get(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("%s: get stored mirror %q: %v", tc.name, tc.id, err)
		}
		if got.DeviceLFDI != tc.owner {
			t.Errorf("%s: stored DeviceLFDI = %q, want %q (cert-derived stamp)", tc.name, got.DeviceLFDI, tc.owner)
		}
		if got.MRID != sharedMRID {
			t.Errorf("%s: stored mRID = %q, want %q preserved verbatim", tc.name, got.MRID, sharedMRID)
		}
		if got.Href != "/mup/"+tc.id {
			t.Errorf("%s: stored Href = %q, want %q (must agree with the Location served)", tc.name, got.Href, "/mup/"+tc.id)
		}
	}
}

// TestHandleCreateMirrorUsagePoint_SameDeviceRepostReturns204 asserts the
// idempotent-ish re-POST path survives per-owner keying: one device POSTing
// its own mRID twice gets 204 (IEEE 2030.5-2018 section 10.11.3 rule (a)(4)),
// an empty body, and a Location pointing at the SAME record it created the
// first time, not a second one.
func TestHandleCreateMirrorUsagePoint_SameDeviceRepostReturns204(t *testing.T) {
	t.Parallel()
	const mrid = "FEDCBA9876543210FEDCBA9876543210"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := createMirrorMux(s, mupKeyLFDIA)

	first := postMirror(t, mux, mrid)
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST /mup: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	idFirst := assertUsableLocation(t, first, "first create")

	second := postMirror(t, mux, mrid)
	if second.Code != http.StatusNoContent {
		t.Fatalf("re-POST of the caller's own mRID: status = %d, want 204; body = %s", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Errorf("re-POST body = %q, want empty (rule (a)(4): 204 carries no representation)", second.Body.String())
	}

	locSecond := second.Header().Get("Location")
	wantLoc := "/mup/" + idFirst
	if locSecond != wantLoc {
		t.Errorf("re-POST Location = %q, want %q (the caller's own mirror, stable across re-POSTs)", locSecond, wantLoc)
	}

	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count mirrors: %v", err)
	}
	if count != 1 {
		t.Errorf("stored MirrorUsagePoint count = %d, want 1 (a re-POST must not mint a second record)", count)
	}

	stored, err := s.Get(context.Background(), idFirst)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != mupKeyLFDIA {
		t.Errorf("stored DeviceLFDI = %q, want %q (the caller's own record)", stored.DeviceLFDI, mupKeyLFDIA)
	}
	if stored.Href != wantLoc {
		t.Errorf("stored Href = %q, want %q (must agree with the Location served)", stored.Href, wantLoc)
	}
}

// TestHandleCreateMirrorUsagePoint_RepostOverwritesStoredRecord asserts rule
// (a)(4)'s "the new data SHALL be written over the existing MirrorUsagePoint":
// a second POST of the same mRID from the same device actually updates the
// stored record's fields rather than the first version surviving untouched
// underneath a silently-discarded 204. DeviceLFDI (server-stamped from the
// certificate) and Href (derived from the same (owner, mRID) id) must stay
// stable across the overwrite, because those are exactly what re-addressing
// the resource depends on.
func TestHandleCreateMirrorUsagePoint_RepostOverwritesStoredRecord(t *testing.T) {
	t.Parallel()
	const mrid = "AAAA1111AAAA1111AAAA1111AAAA1111"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := createMirrorMux(s, mupKeyLFDIA)

	firstBody, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: mrid, Description: "first description"})
	if err != nil {
		t.Fatalf("marshal first body: %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(firstBody)))
	if w.Code != http.StatusCreated {
		t.Fatalf("first POST /mup: status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	id := strings.TrimPrefix(w.Header().Get("Location"), "/mup/")

	before, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get record after create: %v", err)
	}
	if before.Description != "first description" {
		t.Fatalf("stored Description before overwrite = %q, want %q", before.Description, "first description")
	}

	secondBody, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: mrid, Description: "second description"})
	if err != nil {
		t.Fatalf("marshal second body: %v", err)
	}
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(secondBody)))
	if w2.Code != http.StatusNoContent {
		t.Fatalf("second POST /mup: status = %d, want 204; body = %s", w2.Code, w2.Body.String())
	}

	after, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get record after overwrite: %v", err)
	}
	if after.Description != "second description" {
		t.Errorf("stored Description after overwrite = %q, want %q (rule (a)(4): new data SHALL be written over the existing record)",
			after.Description, "second description")
	}
	if after.DeviceLFDI != mupKeyLFDIA {
		t.Errorf("stored DeviceLFDI after overwrite = %q, want %q (server-stamped, must stay stable)", after.DeviceLFDI, mupKeyLFDIA)
	}
	if after.Href != before.Href {
		t.Errorf("stored Href after overwrite = %q, want %q (same (owner, mRID) id, must stay stable)", after.Href, before.Href)
	}
	if after.MRID != mrid {
		t.Errorf("stored MRID after overwrite = %q, want %q", after.MRID, mrid)
	}
}

// TestHandleCreateMirrorUsagePoint_CollisionKeepsACLIntact asserts the rule (e)
// ownership gate still holds through the new keying: after both devices create
// a mirror under the same client mRID, neither can read or write the other's.
func TestHandleCreateMirrorUsagePoint_CollisionKeepsACLIntact(t *testing.T) {
	t.Parallel()
	const sharedMRID = "0123456789ABCDEF0123456789ABCDEF"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	wA := postMirror(t, createMirrorMux(s, mupKeyLFDIA), sharedMRID)
	if wA.Code != http.StatusCreated {
		t.Fatalf("device A create: status = %d, want 201", wA.Code)
	}
	idA := assertUsableLocation(t, wA, "device A create")

	wB := postMirror(t, createMirrorMux(s, mupKeyLFDIB), sharedMRID)
	if wB.Code != http.StatusCreated {
		t.Fatalf("device B create: status = %d, want 201", wB.Code)
	}
	idB := assertUsableLocation(t, wB, "device B create")

	// Device A may not write device B's mirror, over either POST route.
	writeMux := mirrorPostMux(s, mmrStore, mupKeyLFDIA)
	for _, path := range bothPostRoutes(idB) {
		w := httptest.NewRecorder()
		writeMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "FORGED", 99))))
		if w.Code != http.StatusForbidden {
			t.Errorf("device A POST %s (device B's mirror): status = %d, want 403; body = %s", path, w.Code, w.Body.String())
		}
		assertNoBodyLeak(t, w, mupKeyLFDIB)
	}
	count, err := mmrStore.Count(context.Background(), idB)
	if err != nil {
		t.Fatalf("count readings under device B's mirror: %v", err)
	}
	if count != 0 {
		t.Errorf("readings stored under device B's mirror by device A = %d, want 0", count)
	}

	// Device A may not read device B's mirror.
	readMux := http.NewServeMux()
	readMux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, identityProvider(mupKeyLFDIA)))
	w := httptest.NewRecorder()
	readMux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/"+idB, nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("device A GET /mup/%s (device B's mirror): status = %d, want 403; body = %s", idB, w.Code, w.Body.String())
	}
	assertNoBodyLeak(t, w, mupKeyLFDIB)

	// Device A still reads its own, rule (c) intact.
	w = httptest.NewRecorder()
	readMux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/"+idA, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("device A GET its own mirror: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "MirrorMeterReading") {
		t.Errorf("GET /mup/{id} served a MirrorMeterReading element, violates rule (c); body = %s", w.Body.String())
	}
}

// TestHandleCreateMirrorUsagePoint_LocationIsBounded asserts the Location the
// create path emits stays inside the client's 127-byte URI buffer and is never
// empty, for every mRID shape a conformant client can send: ordinary, hostile
// length, and hostile characters. A client-supplied mRID must never be able to
// size or shape the URI we hand back. An absent mRID is covered separately by
// TestHandleCreateMirrorUsagePoint_NoMRID_Rejected: it is refused outright
// rather than reaching this create path at all.
func TestHandleCreateMirrorUsagePoint_LocationIsBounded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mrid string
	}{
		{"ordinary hexBinary128", "0123456789ABCDEF0123456789ABCDEF"},
		{"over-long", strings.Repeat("A", 4000)},
		{"path separators", "../../etc/passwd"},
		{"query and fragment", "a?b#c d%e"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := memory.NewStore[sep2.MirrorUsagePoint]()
			mux := createMirrorMux(s, mupKeyLFDIA)

			created := postMirror(t, mux, tc.mrid)
			if created.Code != http.StatusCreated {
				t.Fatalf("create: status = %d, want 201; body = %s", created.Code, created.Body.String())
			}
			assertUsableLocation(t, created, "create")

			// The re-POST overwrite path (rule (a)(4)) emits a Location too,
			// and it is the one the field defect exercised; hold it to the
			// same bound.
			collided := postMirror(t, mux, tc.mrid)
			if collided.Code != http.StatusNoContent {
				t.Fatalf("re-post: status = %d, want 204; body = %s", collided.Code, collided.Body.String())
			}
			assertUsableLocation(t, collided, "re-post collision")
		})
	}
}

// TestHandleCreateMirrorUsagePoint_NoMRID_Rejected asserts IEEE 2030.5-2018
// section 10.11.3 rule (a)(1): a POST /mup that omits the MirrorUsagePoint
// mRID is refused with 400, and nothing is stored. See mirror.go's fix-1
// comment on HandleCreateMirrorUsagePoint for the three independent sources
// (sep.xsd minOccurs, the rule text, the EPRI client's own schema table) that
// make mRID mandatory here, and why a synthetic per-request key is the wrong
// fix.
func TestHandleCreateMirrorUsagePoint_NoMRID_Rejected(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := createMirrorMux(s, mupKeyLFDIA)

	mup := sep2.MirrorUsagePoint{Description: "no mRID supplied"}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /mup with no mRID: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count mirrors: %v", err)
	}
	if count != 0 {
		t.Errorf("stored MirrorUsagePoint count = %d, want 0: a rejected POST must not store anything", count)
	}
}

// TestHandleCreateMirrorUsagePoint_SeededForeignRecordDenied covers the
// defense-in-depth branch inside the ErrAlreadyExists path: per-owner keying
// means a caller's own (LFDI, mRID) pair cannot naturally collide with
// another caller's, but the store is also writable by a consumer that seeds
// it directly (mirror.go's authorizeMirrorOwner doc comment describes the
// same concern). If the id a caller's POST derives is already occupied by a
// record stamped with someone else's DeviceLFDI, the create path must deny
// with 403 rather than overwrite a record it does not own.
func TestHandleCreateMirrorUsagePoint_SeededForeignRecordDenied(t *testing.T) {
	t.Parallel()
	const foreignOwner = "FOREIGN_OWNER_LFDI"
	const mrid = "SEEDED0000000000SEEDED0000000000"

	id := metering.MirrorStoreID(mupKeyLFDIA, mrid)
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	seedOwnedMirror(t, s, id, mrid, foreignOwner)

	mux := createMirrorMux(s, mupKeyLFDIA)
	w := postMirror(t, mux, mrid)

	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /mup colliding with a foreign-owned seeded record: status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
	assertNoBodyLeak(t, w, foreignOwner, mupKeyLFDIA)

	stored, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get seeded record: %v", err)
	}
	if stored.DeviceLFDI != foreignOwner {
		t.Errorf("stored DeviceLFDI = %q, want %q: a denied POST must not overwrite a record it does not own", stored.DeviceLFDI, foreignOwner)
	}
}
