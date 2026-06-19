package handler_test

// Race-loss tests for IEEE-010.
//
// The /edev and /mup POST handlers, on receiving store.ErrAlreadyExists from
// Create, fall back to Get to fetch the existing record and respond 200.
// If the record was deleted between Create and Get, the handler must not
// return 200 with a zero-value body and blank Location — that is silent
// data loss. The fix returns 500 in that narrow window.
//
// Tests inject the race via mock stores that implement the store interfaces
// directly: Create unconditionally returns ErrAlreadyExists, and Get
// unconditionally returns ErrNotFound. No production store changes.

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	coremetering "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/handlers/metering"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// ----------------------- EndDevice race-loss double ------------------------

// raceLossEndDeviceStore satisfies store.EndDeviceStore. Create always
// reports ErrAlreadyExists; Get always reports ErrNotFound. This is the
// exact race-loss timing: the record exists at the moment Create runs but
// is gone by the time the handler's fallback Get reaches it.
type raceLossEndDeviceStore struct{}

func (raceLossEndDeviceStore) Get(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, store.ErrNotFound
}

func (raceLossEndDeviceStore) List(context.Context, store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return store.ListResult[sep2.EndDevice]{}, nil
}

func (raceLossEndDeviceStore) Create(context.Context, string, sep2.EndDevice) error {
	return store.ErrAlreadyExists
}

func (raceLossEndDeviceStore) Update(context.Context, string, sep2.EndDevice) error {
	return nil
}

func (raceLossEndDeviceStore) Delete(context.Context, string) error {
	return nil
}

func (raceLossEndDeviceStore) Count(context.Context) (uint32, error) {
	return 0, nil
}

func (raceLossEndDeviceStore) GetBySFDI(context.Context, string) (sep2.EndDevice, error) {
	// Simulate: the secondary-index lookup misses, so the handler proceeds
	// to Create; Create returns ErrAlreadyExists (another goroutine just
	// committed) and then the fallback Get hits the post-delete window.
	return sep2.EndDevice{}, store.ErrNotFound
}

func (raceLossEndDeviceStore) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, store.ErrNotFound
}

// --------------------------- MUP race-loss double --------------------------

// raceLossMUPStore satisfies store.ResourceStore[sep2.MirrorUsagePoint]
// with the same race-loss semantics as the EndDevice double.
type raceLossMUPStore struct{}

func (raceLossMUPStore) Get(context.Context, string) (sep2.MirrorUsagePoint, error) {
	return sep2.MirrorUsagePoint{}, store.ErrNotFound
}

func (raceLossMUPStore) List(context.Context, store.ListOptions) (store.ListResult[sep2.MirrorUsagePoint], error) {
	return store.ListResult[sep2.MirrorUsagePoint]{}, nil
}

func (raceLossMUPStore) Create(context.Context, string, sep2.MirrorUsagePoint) error {
	return store.ErrAlreadyExists
}

func (raceLossMUPStore) Update(context.Context, string, sep2.MirrorUsagePoint) error {
	return nil
}

func (raceLossMUPStore) Delete(context.Context, string) error {
	return nil
}

func (raceLossMUPStore) Count(context.Context) (uint32, error) {
	return 0, nil
}

// ------------------------------- Tests ------------------------------------

// TestHandleCreateEndDeviceRaceLoss covers the /edev fix. The mock store
// simulates: GetBySFDI misses, Create reports ErrAlreadyExists, fallback
// Get reports ErrNotFound. The handler must return 500, not 200 with a
// zero-value body.
func TestHandleCreateEndDeviceRaceLoss(t *testing.T) {
	t.Parallel()

	h := handler.HandleCreateEndDevice(raceLossEndDeviceStore{})

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	req = addIdentity(req, "123456789012", "AABBCCDD00112233445566778899AABBCCDDEEFF")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("race-loss status = %d, want 500; body=%q", w.Code, w.Body.String())
	}

	// Critical: must NOT have leaked a 200 with a blank Location and
	// zero-value body. The 500 path writes no Location and writes a
	// plain-text error body, not XML.
	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("race-loss Location = %q, want empty", got)
	}

	// Ensure body is not a zero-value EndDevice XML payload — i.e. confirm
	// that the previous silent-success behavior is gone.
	var leaked sep2.EndDevice
	if err := xml.Unmarshal(w.Body.Bytes(), &leaked); err == nil && leaked.Href != "" {
		t.Errorf("race-loss body parsed as EndDevice with Href=%q — silent success leaked", leaked.Href)
	}
}

// TestHandleCreateMirrorUsagePointRaceLoss covers the /mup fix.
func TestHandleCreateMirrorUsagePointRaceLoss(t *testing.T) {
	t.Parallel()

	// authLFDIProvider is defined in mirror_test.go (same handler_test package).
	h := coremetering.HandleCreateMirrorUsagePoint(raceLossMUPStore{}, authLFDIProvider)

	mup := sep2.MirrorUsagePoint{MRID: "INV-RACE", Description: "race-loss probe"}
	body, _ := xml.Marshal(&mup)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req = addIdentity(req, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("race-loss status = %d, want 500; body=%q", w.Code, w.Body.String())
	}

	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("race-loss Location = %q, want empty", got)
	}

	var leaked sep2.MirrorUsagePoint
	if err := xml.Unmarshal(w.Body.Bytes(), &leaked); err == nil && leaked.Href != "" {
		t.Errorf("race-loss body parsed as MUP with Href=%q — silent success leaked", leaked.Href)
	}
}

// TestHandleCreateEndDeviceInvalidXML exercises the XML-parse-failure
// branch added by io.ReadAll + xml.Unmarshal. Drives /edev coverage above
// the 80% gate; the body bytes are malformed XML and must produce 400.
func TestHandleCreateEndDeviceInvalidXML(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := handler.HandleCreateEndDevice(s)

	req := httptest.NewRequest(http.MethodPost, "/edev", bytes.NewBufferString("<not-xml"))
	req = addIdentity(req, "123456789012", "AABB")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid-XML status = %d, want 400", w.Code)
	}
}

// TestHandleCreateEndDeviceMethodNotPost covers the method-guard branch.
func TestHandleCreateEndDeviceMethodNotPost(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := handler.HandleCreateEndDevice(s)

	req := httptest.NewRequest(http.MethodGet, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("non-POST status = %d, want 405", w.Code)
	}
}

// TestHandleCreateMirrorUsagePointInvalidXML exercises the XML-parse branch
// for /mup, lifting coverage on HandleCreateMirrorUsagePoint past 80%.
func TestHandleCreateMirrorUsagePointInvalidXML(t *testing.T) {
	t.Parallel()

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	// authLFDIProvider is defined in mirror_test.go (same handler_test package).
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewBufferString("<not-xml"))
	req = addIdentity(req, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid-XML status = %d, want 400", w.Code)
	}
}

// TestHandleCreateMirrorUsagePointMethodNotPost covers the method guard.
func TestHandleCreateMirrorUsagePointMethodNotPost(t *testing.T) {
	t.Parallel()

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	// authLFDIProvider is defined in mirror_test.go (same handler_test package).
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

	req := httptest.NewRequest(http.MethodGet, "/mup", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("non-POST status = %d, want 405", w.Code)
	}
}

// TestHandleCreateMirrorUsagePointNoIdentity covers the missing-identity guard.
func TestHandleCreateMirrorUsagePointNoIdentity(t *testing.T) {
	t.Parallel()

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewBufferString("<MirrorUsagePoint/>"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("no-identity status = %d, want 403", w.Code)
	}
}

// TestHandleCreateMirrorUsagePointEmptyMRID covers the time-based-ID fallback.
func TestHandleCreateMirrorUsagePointEmptyMRID(t *testing.T) {
	t.Parallel()

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

	mup := sep2.MirrorUsagePoint{Description: "no mrid"}
	body, _ := xml.Marshal(&mup)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req = addIdentity(req, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("empty-MRID status = %d, want 201", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/mup/mup-") {
		t.Errorf("empty-MRID Location = %q, want /mup/mup-...", loc)
	}
}

// TestHandleCreateMirrorUsagePointDuplicate verifies that the existing
// already-exists semantics are preserved by the IEEE-010 fix: a second POST
// of the same MRID returns 200 with a non-empty Location and the existing
// record's payload. The race-loss change must not regress this path.
func TestHandleCreateMirrorUsagePointDuplicate(t *testing.T) {
	t.Parallel()

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

	mup := sep2.MirrorUsagePoint{MRID: "INV-DUP", Description: "duplicate probe"}
	body, _ := xml.Marshal(&mup)

	// First POST — 201 Created.
	req1 := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req1 = addIdentity(req1, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201; body=%s", w1.Code, w1.Body.String())
	}
	loc1 := w1.Header().Get("Location")
	if !strings.HasPrefix(loc1, "/mup/") {
		t.Fatalf("first POST Location = %q, want /mup/...", loc1)
	}

	// Second POST with the same MRID — 200 with existing record, valid Location.
	req2 := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req2 = addIdentity(req2, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("duplicate POST status = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	loc2 := w2.Header().Get("Location")
	if loc2 == "" {
		t.Errorf("duplicate POST missing Location header")
	}
	if loc2 != loc1 {
		t.Errorf("duplicate POST Location = %q, want %q (same record)", loc2, loc1)
	}

	var got sep2.MirrorUsagePoint
	if err := xml.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("duplicate POST body unmarshal: %v", err)
	}
	if got.Href == "" {
		t.Errorf("duplicate POST body Href empty — zero-value response leaked")
	}
	if got.MRID != "INV-DUP" {
		t.Errorf("duplicate POST body MRID = %q, want INV-DUP", got.MRID)
	}
}
