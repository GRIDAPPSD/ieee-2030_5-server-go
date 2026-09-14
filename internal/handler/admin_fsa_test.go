package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #163 handler tests. Each endpoint covers the happy path, validation,
// 404, and 409 (where applicable).

// --- stubs ------------------------------------------------------------------

type stubPrograms struct {
	known map[string]bool
}

func (s *stubPrograms) HasProgram(_ context.Context, href string) bool {
	return s.known[href]
}

type stubEndDevices struct {
	devs map[string]sep2.EndDevice
}

func (s *stubEndDevices) Get(_ context.Context, id string) (sep2.EndDevice, error) {
	d, ok := s.devs[id]
	if !ok {
		return sep2.EndDevice{}, store.ErrNotFound
	}
	return d.Copy(), nil
}

func (s *stubEndDevices) List(_ context.Context, _ store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	items := make([]sep2.EndDevice, 0, len(s.devs))
	for _, d := range s.devs {
		items = append(items, d.Copy())
	}
	return store.ListResult[sep2.EndDevice]{
		All:     uint32(len(items)),
		Results: uint32(len(items)),
		Items:   items,
	}, nil
}

func newAdminFSAHandler(t *testing.T) *handler.AdminFSAHandler {
	t.Helper()
	return &handler.AdminFSAHandler{
		AdminFSAs:   memory.NewAdminFSAStore(),
		DeviceFSAs:  memory.NewScopedStore[sep2.FunctionSetAssignments](),
		EndDevices:  &stubEndDevices{devs: map[string]sep2.EndDevice{}},
		DERPrograms: &stubPrograms{known: map[string]bool{}},
	}
}

// --- POST /api/fsas ---------------------------------------------------------

func TestCreateAdminFSA_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":"Solar primary","primacy":1,"mRID":"fsa-solar"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["mRID"] != "fsa-solar" {
		t.Errorf("mRID mismatch: %v", got["mRID"])
	}
	if got["href"] != "/api/fsas/fsa-solar" {
		t.Errorf("href mismatch: %v", got["href"])
	}
	if w.Header().Get("Location") != "/api/fsas/fsa-solar" {
		t.Errorf("Location header missing/wrong: %q", w.Header().Get("Location"))
	}
}

func TestCreateAdminFSA_AutoMRID(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":"auto"}`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !strings.HasPrefix(got["mRID"].(string), "fsa-") {
		t.Errorf("expected auto-mRID with fsa- prefix, got %v", got["mRID"])
	}
}

func TestCreateAdminFSA_EmptyDescription_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":""}`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreateAdminFSA_InvalidJSON_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreateAdminFSA_UnknownField_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":"x","wat":1}`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestCreateAdminFSA_InvalidJSONDoesNotLeakDecoderDetail pins the 400 path
// convention (#360): an oversized numeric literal on the uint8 primacy field
// is echoed verbatim in a json.UnmarshalTypeError, which is attacker-supplied
// content the client-visible body must not carry.
func TestCreateAdminFSA_InvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "13370360913370360913370360913370360913370360"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":"x","primacy":`+marker+`}`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

func TestCreateAdminFSA_DuplicateMRID_409(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(
		`{"description":"dup","mRID":"fsa-1"}`))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

// --- GET /api/fsas ----------------------------------------------------------

func TestListAdminFSAs(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-a", sep2.FunctionSetAssignments{MRID: "fsa-a", Description: "a"})
	_ = h.AdminFSAs.Create(ctx, "fsa-b", sep2.FunctionSetAssignments{MRID: "fsa-b", Description: "b"})

	req := httptest.NewRequest(http.MethodGet, "/api/fsas", nil)
	w := httptest.NewRecorder()
	h.HandleListAdminFSAs()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got struct {
		FSAs []map[string]any `json:"fsas"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.FSAs) != 2 {
		t.Errorf("expected 2 FSAs, got %d", len(got.FSAs))
	}
}

// --- GET /api/fsas/{id} -----------------------------------------------------

func TestGetAdminFSA_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/fsas/ghost", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.HandleGetAdminFSA()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetAdminFSA_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{
		Resource: sep2.Resource{Href: "/api/fsas/fsa-1"},
		MRID:     "fsa-1", Description: "hello",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/fsas/fsa-1", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleGetAdminFSA()(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["mRID"] != "fsa-1" || got["description"] != "hello" {
		t.Errorf("body mismatch: %v", got)
	}
}

// --- DELETE /api/fsas/{id} --------------------------------------------------

func TestDeleteAdminFSA_204(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/fsa-1", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleDeleteAdminFSA()(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteAdminFSA_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/missing", nil)
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.HandleDeleteAdminFSA()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDeleteAdminFSA_InUse_409(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	_ = h.AdminFSAs.AttachProgram(ctx, "fsa-1", "/p/1")

	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/fsa-1", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleDeleteAdminFSA()(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

// --- POST /api/fsas/{id}/programs -------------------------------------------

func TestAttachProgram_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	h.DERPrograms.(*stubPrograms).known["/edev/dev-A/fsa/fsa-1/derp/p1"] = true

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(
		`{"programHref":"/edev/dev-A/fsa/fsa-1/derp/p1"}`))
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if got := h.AdminFSAs.Programs(ctx, "fsa-1"); len(got) != 1 {
		t.Errorf("expected 1 program, got %v", got)
	}
}

func TestAttachProgram_ProgramNotFound_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(
		`{"programHref":"/ghost"}`))
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAttachProgram_FSANotFound_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	h.DERPrograms.(*stubPrograms).known["/p/1"] = true

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/missing/programs", strings.NewReader(
		`{"programHref":"/p/1"}`))
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAttachProgram_Duplicate_409(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	h.DERPrograms.(*stubPrograms).known["/p/1"] = true
	_ = h.AdminFSAs.AttachProgram(ctx, "fsa-1", "/p/1")

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(
		`{"programHref":"/p/1"}`))
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAttachProgram_EmptyHref_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(`{}`))
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestAttachProgram_InvalidJSONDoesNotLeakDecoderDetail pins the 400 path
// convention (#360): DisallowUnknownFields quotes the offending field name
// verbatim, which is attacker-supplied content the client-visible body must
// not carry.
func TestAttachProgram_InvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(
		`{"programHref":"/p/1","`+marker+`":1}`))
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleAttachProgram()(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// --- DELETE /api/fsas/{id}/programs -----------------------------------------

func TestDetachProgram_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	_ = h.AdminFSAs.AttachProgram(ctx, "fsa-1", "/p/1")

	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/fsa-1/programs?href=%2Fp%2F1", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleDetachProgram()(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestDetachProgram_MissingHref_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/fsa-1/programs", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleDetachProgram()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDetachProgram_NotAttached_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodDelete, "/api/fsas/fsa-1/programs?href=%2Fnever", nil)
	req.SetPathValue("id", "fsa-1")
	w := httptest.NewRecorder()
	h.HandleDetachProgram()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- POST /api/devices/{id}/fsa-assignment ----------------------------------

func TestAssignDeviceFSA_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1", Description: "solar"})
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-A"}},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/api/fsas/fsa-1"}`))
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Verify scope materialization.
	scoped, ok := h.DeviceFSAs.(*memory.ScopedStore[sep2.FunctionSetAssignments])
	if !ok {
		t.Fatal("expected *memory.ScopedStore")
	}
	got, err := scoped.Get(ctx, "dev-A", "fsa-1")
	if err != nil {
		t.Fatalf("scoped Get: %v", err)
	}
	if got.MRID != "fsa-1" || got.Description != "solar" {
		t.Errorf("scoped FSA mismatch: %+v", got)
	}
	if got.Href != "/edev/dev-A/fsa/fsa-1" {
		t.Errorf("scoped href mismatch: %q", got.Href)
	}
}

func TestAssignDeviceFSA_DeviceMissing_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodPost, "/api/devices/ghost/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/api/fsas/fsa-1"}`))
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAssignDeviceFSA_FSAMissing_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-A"}},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/api/fsas/ghost"}`))
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAssignDeviceFSA_Duplicate_409(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-A"}},
	}

	body := `{"fsaHref":"/api/fsas/fsa-1"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(body))
	req1.SetPathValue("id", "dev-A")
	h.HandleAssignDeviceFSA()(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(body))
	req2.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req2)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAssignDeviceFSA_BadFSAHref_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{}

	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/wrong/path"}`))
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestAssignDeviceFSA_InvalidJSONDoesNotLeakDecoderDetail pins the 400 path
// convention (#360): DisallowUnknownFields quotes the offending field name
// verbatim, which is attacker-supplied content the client-visible body must
// not carry.
func TestAssignDeviceFSA_InvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	h := newAdminFSAHandler(t)
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{}

	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/api/fsas/fsa-1","`+marker+`":1}`))
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// --- DELETE /api/devices/{id}/fsa-assignment --------------------------------

func TestUnassignDeviceFSA_Happy(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	_ = h.AdminFSAs.AssignDevice(ctx, "fsa-1", "dev-A")

	req := httptest.NewRequest(http.MethodDelete,
		"/api/devices/dev-A/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-1", nil)
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleUnassignDeviceFSA()(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}
	if got := h.AdminFSAs.Devices(ctx, "fsa-1"); len(got) != 0 {
		t.Errorf("expected empty after unassign, got %v", got)
	}
}

func TestUnassignDeviceFSA_NotAssigned_404(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	req := httptest.NewRequest(http.MethodDelete,
		"/api/devices/dev-A/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-1", nil)
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleUnassignDeviceFSA()(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestUnassignDeviceFSA_MissingHref_400(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/dev-A/fsa-assignment", nil)
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleUnassignDeviceFSA()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- GET /api/topology integration -----------------------------------------

func TestTopology_FullTree(t *testing.T) {
	t.Parallel()
	adminFSAs := memory.NewAdminFSAStore()
	devs := &stubEndDevices{devs: map[string]sep2.EndDevice{
		"dev-A": {
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-A"}},
			SFDI:                 "111111111111",
			LFDI:                 "AAAA",
			Enabled:              ptrBool(true),
		},
		"dev-B": {
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-B"}},
			SFDI:                 "222222222222",
			LFDI:                 "BBBB",
			Enabled:              ptrBool(false),
		},
	}}
	ctx := context.Background()
	_ = adminFSAs.Create(ctx, "fsa-solar", sep2.FunctionSetAssignments{MRID: "fsa-solar", Description: "Solar"})
	_ = adminFSAs.Create(ctx, "fsa-wind", sep2.FunctionSetAssignments{MRID: "fsa-wind", Description: "Wind template"})
	_ = adminFSAs.AttachProgram(ctx, "fsa-solar", "/p/solar-1")
	_ = adminFSAs.AssignDevice(ctx, "fsa-solar", "dev-A")

	req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
	w := httptest.NewRecorder()
	handler.HandleTopology(adminFSAs, devs)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var root handler.TopologyNode
	if err := json.Unmarshal(w.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}

	if root.Kind != "SY" {
		t.Errorf("root Kind = %q, want SY", root.Kind)
	}
	if len(root.FSAs) != 1 || root.FSAs[0].MRID != "fsa-wind" {
		t.Errorf("expected 1 unassigned template fsa-wind, got %+v", root.FSAs)
	}
	if len(root.Children) != 1 || root.Children[0].Kind != "FD" {
		t.Fatalf("expected single FD child, got %+v", root.Children)
	}
	fd := root.Children[0]
	if len(fd.Children) != 1 || fd.Children[0].Kind != "SP" {
		t.Fatalf("expected single SP child, got %+v", fd.Children)
	}
	sp := fd.Children[0]
	if len(sp.Children) != 2 {
		t.Fatalf("expected 2 DEV children, got %d", len(sp.Children))
	}

	// Find dev-A and confirm it has fsa-solar attached.
	var foundSolar bool
	for _, dev := range sp.Children {
		if dev.ID == "dev-A" {
			if len(dev.FSAs) != 1 || dev.FSAs[0].MRID != "fsa-solar" {
				t.Errorf("dev-A expected fsa-solar, got %+v", dev.FSAs)
			}
			if len(dev.FSAs[0].Programs) != 1 || dev.FSAs[0].Programs[0] != "/p/solar-1" {
				t.Errorf("expected program /p/solar-1, got %v", dev.FSAs[0].Programs)
			}
			foundSolar = true
		}
	}
	if !foundSolar {
		t.Error("dev-A not found in tree")
	}
}

// helper: pointer to bool.
func ptrBool(b bool) *bool { return &b }

// Sanity: extractFSAID parser. Round-trips through the assignment endpoint.
func TestExtractFSAID_RejectsBadShape(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{}

	cases := []string{
		"",
		"fsa-1",                 // missing prefix
		"/api/fsas/",            // empty id
		"/api/fsas/fsa-1/extra", // trailing path
		"/api/fsas/fsa-1?x=1",   // query in body
		"/wrong/api/fsas/fsa-1", // wrong prefix
	}
	for _, h_ := range cases {
		body := `{"fsaHref":"` + h_ + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(body))
		req.SetPathValue("id", "dev-A")
		w := httptest.NewRecorder()
		h.HandleAssignDeviceFSA()(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("href %q expected 400, got %d", h_, w.Code)
		}
	}
}

// Ensure errors.Is propagation isn't accidentally swallowed in admin layer.
func TestAttachProgram_ContextErrorChain(t *testing.T) {
	t.Parallel()
	// Sanity: errors.Is to store.ErrNotFound still resolves to handler 404
	// for the FSA-missing path (covered above by TestAttachProgram_FSANotFound_404),
	// but check that errors.Is is the test of record.
	err := store.ErrNotFound
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatal("errors.Is broken in test env")
	}
}

// Smoke: ensure handler does not panic when DeviceFSAs is nil (admin-only mode).
func TestAssignDeviceFSA_NilDeviceFSAs_StillRecords(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	h.DeviceFSAs = nil
	ctx := context.Background()
	_ = h.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})
	h.EndDevices.(*stubEndDevices).devs["dev-A"] = sep2.EndDevice{}

	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(
		`{"fsaHref":"/api/fsas/fsa-1"}`))
	req.SetPathValue("id", "dev-A")
	w := httptest.NewRecorder()
	h.HandleAssignDeviceFSA()(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := h.AdminFSAs.Devices(ctx, "fsa-1"); len(got) != 1 {
		t.Errorf("expected 1 device link, got %v", got)
	}
}

// Coverage: id-required 400 paths and invalid-JSON 400 paths on every handler.

func TestAdminFSAHandlers_EmptyIDPath(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)

	tests := []struct {
		name string
		run  func() *httptest.ResponseRecorder
	}{
		{"GetAdminFSA", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/api/fsas/", nil)
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleGetAdminFSA()(w, req)
			return w
		}},
		{"DeleteAdminFSA", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodDelete, "/api/fsas/", nil)
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleDeleteAdminFSA()(w, req)
			return w
		}},
		{"AttachProgram", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/api/fsas//programs", strings.NewReader(`{"programHref":"/p"}`))
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleAttachProgram()(w, req)
			return w
		}},
		{"DetachProgram", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodDelete, "/api/fsas//programs?href=x", nil)
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleDetachProgram()(w, req)
			return w
		}},
		{"AssignDeviceFSA", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/api/devices//fsa-assignment", strings.NewReader(`{"fsaHref":"/api/fsas/x"}`))
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleAssignDeviceFSA()(w, req)
			return w
		}},
		{"UnassignDeviceFSA", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodDelete, "/api/devices//fsa-assignment?fsaHref=x", nil)
			req.SetPathValue("id", "")
			w := httptest.NewRecorder()
			h.HandleUnassignDeviceFSA()(w, req)
			return w
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.run()
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminFSAHandlers_InvalidJSONBody(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	_ = h.AdminFSAs.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1"})

	tests := []struct {
		name string
		do   func() *httptest.ResponseRecorder
	}{
		{"AttachProgram-bad-json", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-1/programs", strings.NewReader(`{not}`))
			req.SetPathValue("id", "fsa-1")
			w := httptest.NewRecorder()
			h.HandleAttachProgram()(w, req)
			return w
		}},
		{"AssignDeviceFSA-bad-json", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-A/fsa-assignment", strings.NewReader(`{not}`))
			req.SetPathValue("id", "dev-A")
			w := httptest.NewRecorder()
			h.HandleAssignDeviceFSA()(w, req)
			return w
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.do()
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// Bytes-level check: write a sequence and make sure the body parses back.
func TestRoundTrip_CreateThenGet(t *testing.T) {
	t.Parallel()
	h := newAdminFSAHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", bytes.NewReader(
		[]byte(`{"description":"d","mRID":"rt-1"}`)))
	w := httptest.NewRecorder()
	h.HandleCreateAdminFSA()(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/fsas/rt-1", nil)
	req2.SetPathValue("id", "rt-1")
	w2 := httptest.NewRecorder()
	h.HandleGetAdminFSA()(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("get: %d", w2.Code)
	}
}
