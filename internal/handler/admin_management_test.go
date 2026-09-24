package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #440 handler tests. The acceptance criteria in the issue map directly to
// the subtests below: create/list/remove, normalization, re-key, and the
// unauthenticated-refusal shape is covered at the router level
// (internal/server), not here, since AdminAuthMiddleware wraps the mux, not
// the handler.

const (
	mgHandlerManagerA = "AAAA000000000000000000000000000000000001"
	mgHandlerManagerB = "BBBB000000000000000000000000000000000002"
	mgHandlerChildA   = "C001000000000000000000000000000000000001"
	mgHandlerChildB   = "C002000000000000000000000000000000000002"
)

func newManagementHandler(t *testing.T) *handler.AdminManagementHandler {
	t.Helper()
	return &handler.AdminManagementHandler{Managers: memory.NewEndDeviceManagementStore()}
}

func doJSON(t *testing.T, fn http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

// --- POST /api/management-pairs ---------------------------------------------

func TestCreateManagementPair_Happy(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerChildA+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	manager, err := h.Managers.ManagerOf(t.Context(), mgHandlerChildA)
	if err != nil || manager != mgHandlerManagerA {
		t.Errorf("stored ManagerOf = %q, %v; want %q", manager, err, mgHandlerManagerA)
	}
}

func TestCreateManagementPair_IdenticalIsIdempotent(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	body := `{"managerLFDI":"` + mgHandlerManagerA + `","managedLFDI":"` + mgHandlerChildA + `"}`
	first := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", first.Code)
	}
	second := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs", body)
	if second.Code != http.StatusCreated {
		t.Errorf("repeat create: status = %d, want 201 (idempotent), body = %s", second.Code, second.Body.String())
	}
	if got, _ := h.Managers.ManagedBy(t.Context(), mgHandlerManagerA); len(got) != 1 {
		t.Errorf("ManagedBy after repeat create = %v, want exactly one entry", got)
	}
}

func TestCreateManagementPair_DifferentManagerConflicts(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerChildA+`"}`)
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerB+`","managedLFDI":"`+mgHandlerChildA+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); manager != mgHandlerManagerA {
		t.Errorf("refused reassignment changed the manager to %q", manager)
	}
}

func TestCreateManagementPair_EmptyLFDIIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	for name, body := range map[string]string{
		"empty manager": `{"managerLFDI":"","managedLFDI":"` + mgHandlerChildA + `"}`,
		"empty managed": `{"managerLFDI":"` + mgHandlerManagerA + `","managedLFDI":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs", body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateManagementPair_SelfManagementIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerManagerA+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// TestCreateManagementPair_NormalizesLowerCase is item 2's decisive
// assertion: a lowercase LFDI is ACCEPTED (not refused) and the value
// actually stored is canonical (uppercase), asserted by reading it back
// through the store rather than trusting a 201 alone.
func TestCreateManagementPair_NormalizesLowerCase(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+strings.ToLower(mgHandlerManagerA)+`","managedLFDI":"`+strings.ToLower(mgHandlerChildA)+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["managerLFDI"] != mgHandlerManagerA || got["managedLFDI"] != mgHandlerChildA {
		t.Errorf("response = %v, want canonical upper-case LFDIs", got)
	}
	// The control: a store that received the raw lowercase value would
	// refuse it with ErrInvalidManagementPair, and ManagerOf(canonical)
	// would find nothing. This assertion fails if normalization is ever
	// skipped on this path.
	manager, err := h.Managers.ManagerOf(t.Context(), mgHandlerChildA)
	if err != nil || manager != mgHandlerManagerA {
		t.Errorf("stored ManagerOf(canonical) = %q, %v; want %q (normalization did not reach the store)", manager, err, mgHandlerManagerA)
	}
}

func TestCreateManagementPair_InvalidHexIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"not-hex","managedLFDI":"`+mgHandlerChildA+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// --- GET /api/management-pairs ----------------------------------------------

func TestListManagementPairs_ByManager(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildB)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?manager="+mgHandlerManagerA, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var got struct {
		ManagerLFDI  string   `json:"managerLFDI"`
		ManagedLFDIs []string `json:"managedLFDIs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ManagerLFDI != mgHandlerManagerA || len(got.ManagedLFDIs) != 2 {
		t.Errorf("got = %+v, want manager %q with 2 managed devices", got, mgHandlerManagerA)
	}
}

// TestListManagementPairs_ByManagerNormalizesLowerCase is item 2's sweep for
// the list-by-manager entry point.
func TestListManagementPairs_ByManagerNormalizesLowerCase(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?manager="+strings.ToLower(mgHandlerManagerA), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), mgHandlerChildA) {
		t.Errorf("body = %s, want it to list %q (lowercase query must still find the pair)", w.Body.String(), mgHandlerChildA)
	}
}

func TestListManagementPairs_ByManagedDevice(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["managerLFDI"] != mgHandlerManagerA {
		t.Errorf("got = %v, want managerLFDI %q", got, mgHandlerManagerA)
	}
}

// TestListManagementPairs_ByManagedDeviceNormalizesLowerCase closes item 6's
// C1: the fifth LFDI entry point (the ?managed= query param) had no case
// test, so a regression removing its normalizeLFDI call left the whole
// suite green. This is that missing case, mirrored on
// TestListManagementPairs_ByManagerNormalizesLowerCase.
func TestListManagementPairs_ByManagedDeviceNormalizesLowerCase(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?managed="+strings.ToLower(mgHandlerChildA), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["managerLFDI"] != mgHandlerManagerA || got["managedLFDI"] != mgHandlerChildA {
		t.Errorf("got = %v, want canonical upper-case LFDIs %q, %q (lowercase query must still find the pair)", got, mgHandlerManagerA, mgHandlerChildA)
	}
}

func TestListManagementPairs_UnmanagedDeviceIsNotFound(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestListManagementPairs_NoQueryParamIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestListManagementPairs_BothQueryParamsIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?manager="+mgHandlerManagerA+"&managed="+mgHandlerChildA, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// --- DELETE /api/management-pairs -------------------------------------------

func TestRemoveManagementPair_Happy(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleRemoveManagementPair(), http.MethodDelete, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", w.Code, w.Body.String())
	}
	if _, err := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); err == nil {
		t.Error("pair still present after remove")
	}
}

func TestRemoveManagementPair_NotFound(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleRemoveManagementPair(), http.MethodDelete, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

// TestRemoveManagementPair_NormalizesLowerCase is item 2's sweep for the
// remove entry point.
func TestRemoveManagementPair_NormalizesLowerCase(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleRemoveManagementPair(), http.MethodDelete, "/api/management-pairs?managed="+strings.ToLower(mgHandlerChildA), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", w.Code, w.Body.String())
	}
}

// --- POST /api/management-pairs/rekey ---------------------------------------

func TestRekeyManagementPair_ManagerHappy(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"manager","from":"`+mgHandlerManagerA+`","to":"`+mgHandlerManagerB+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); manager != mgHandlerManagerB {
		t.Errorf("ManagerOf after rekey = %q, want %q", manager, mgHandlerManagerB)
	}
}

// TestRekeyManagementPair_RetiredManagerRefused is the brief's decisive
// test: "a test shows the retired manager LFDI refused after a re-key."
func TestRekeyManagementPair_RetiredManagerRefused(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"manager","from":"`+mgHandlerManagerA+`","to":"`+mgHandlerManagerB+`"}`)

	// The retired manager LFDI now manages nothing: creating a new pair
	// naming it as manager succeeds (it is an ordinary, unmanaging LFDI
	// again), and re-keying it a second time finds nothing to move.
	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"manager","from":"`+mgHandlerManagerA+`","to":"`+mgHandlerChildB+`"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("second rekey from the retired manager: status = %d, want 404 (nothing to rekey); body = %s", w.Code, w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); manager != mgHandlerManagerB {
		t.Errorf("ManagerOf(child) = %q, want %q unchanged", manager, mgHandlerManagerB)
	}
}

func TestRekeyManagementPair_ManagedHappy(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"managed","from":"`+mgHandlerChildA+`","to":"`+mgHandlerChildB+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildB); manager != mgHandlerManagerA {
		t.Errorf("ManagerOf(new managed) = %q, want %q", manager, mgHandlerManagerA)
	}
}

func TestRekeyManagementPair_ManagedCollisionConflicts(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	mustAssignH(t, h, mgHandlerManagerB, mgHandlerChildB)

	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"managed","from":"`+mgHandlerChildA+`","to":"`+mgHandlerChildB+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already has a manager") {
		t.Errorf("body = %s, want it to say the target already has a manager", w.Body.String())
	}
}

// TestRekeyManagementPair_ManagerCollisionConflicts is item 5's handler-level
// assertion: the two rekey directions now agree about a collision, so the
// manager side answers 409 the same way TestRekeyManagementPair_ManagedCollisionConflicts
// already does for the managed side, rather than merging two fleets. The
// message names what actually collided (manages other devices, not "has a
// manager") since the two directions collide on different things.
func TestRekeyManagementPair_ManagerCollisionConflicts(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	mustAssignH(t, h, mgHandlerManagerB, mgHandlerChildB)

	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"manager","from":"`+mgHandlerManagerA+`","to":"`+mgHandlerManagerB+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already manages other devices") {
		t.Errorf("body = %s, want it to say the target already manages other devices", w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); manager != mgHandlerManagerA {
		t.Errorf("ManagerOf(childA) after refused collision = %q, want unchanged %q", manager, mgHandlerManagerA)
	}
}

func TestRekeyManagementPair_BadRoleIsBadRequest(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"aggregator","from":"`+mgHandlerManagerA+`","to":"`+mgHandlerManagerB+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// TestRekeyManagementPair_NormalizesLowerCase is item 2's sweep for the
// rekey entry point's from/to fields.
func TestRekeyManagementPair_NormalizesLowerCase(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"manager","from":"`+strings.ToLower(mgHandlerManagerA)+`","to":"`+strings.ToLower(mgHandlerManagerB)+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if manager, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); manager != mgHandlerManagerB {
		t.Errorf("ManagerOf after lowercase-input rekey = %q, want canonical %q", manager, mgHandlerManagerB)
	}
}

// TestCreateManagementPair_WriteFailureBodyHasNoPath is H9's handler-level
// assertion: a create that fails to persist answers 500 with a message that
// does not repeat the server's absolute snapshot path, even though the
// underlying store error does carry it (see
// memory.TestManagementPersistence_WriteFailureBodyHasNoPath). The
// sanitizing has to happen here, at the boundary that writes the HTTP
// response body.
func TestCreateManagementPair_WriteFailureBodyHasNoPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "management.json")
	store, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence: %v", err)
	}
	h := &handler.AdminManagementHandler{Managers: store}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerChildA+`"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), path) || strings.Contains(w.Body.String(), dir) {
		t.Errorf("body = %s, must not name the server's snapshot path %q", w.Body.String(), path)
	}
	if strings.Contains(w.Body.String(), "/") {
		t.Errorf("body = %s, want no path-shaped content at all", w.Body.String())
	}
}

func mustAssignH(t *testing.T, h *handler.AdminManagementHandler, manager, managed string) {
	t.Helper()
	if err := h.Managers.Assign(t.Context(), manager, managed); err != nil {
		t.Fatalf("Assign(%q, %q): %v", manager, managed, err)
	}
}
