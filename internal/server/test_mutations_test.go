//go:build csip_test_hooks

// Tests for the build-tag-gated test-only mutation surface. These run
// only when the csip_test_hooks tag is set; the companion file
// test_mutations_notag_test.go (no tag predicate) verifies that the
// surface is absent from a default build.

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

const (
	tmTestToken  = "test-mutation-token-xyz"
	tmTokenEnv   = "SEP2_TEST_MUTATION_TOKEN"
	tmTokenHdr   = "X-CSIP-Test-Token"
	tmEdevDelete = "/test/mutations/edev-delete-oob"
	tmPrimacy    = "/test/mutations/derprog-primacy"
	tmDERCtlAdd  = "/test/mutations/derctl-add"
)

// newRouterWithTokenAndStores builds a router with the token env set so
// the mutation surface is registered. Returns the handler and the stores
// for assertion access.
func newRouterWithTokenAndStores(t *testing.T) (http.Handler, *server.Stores) {
	t.Helper()
	t.Setenv(tmTokenEnv, tmTestToken)
	stores := newTestStores()
	cfg := &config.Config{}
	return server.NewRouter(cfg, stores, nil, "", ""), stores
}

func postJSON(t *testing.T, h http.Handler, path string, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	if token != "" {
		req.Header.Set(tmTokenHdr, token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// --- Auth / scaffolding ---

func TestMutationSurface_MissingToken_Unauthorized(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, "", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestMutationSurface_WrongToken_Unauthorized(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, "not-the-token", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestMutationSurface_EmptyEnvDisablesSurface(t *testing.T) {
	// Tag is set but the env is empty — RegisterMutationHandlers should
	// log a warning and register no routes. A request to a mutation path
	// must miss the mux and return 404.
	t.Setenv(tmTokenEnv, "")
	stores := newTestStores()
	cfg := &config.Config{}
	h := server.NewRouter(cfg, stores, nil, "", "")
	rr := postJSON(t, h, tmEdevDelete, "anything", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (surface should be off)", rr.Code)
	}
}

// --- /test/mutations/edev-delete-oob ---

func TestEdevDeleteOOB_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.EndDevices.Create(context.Background(), "edev-1", sep2.EndDevice{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if _, err := stores.EndDevices.Get(context.Background(), "edev-1"); err == nil {
		t.Fatal("EndDevice still present after OOB delete")
	}
}

func TestEdevDeleteOOB_NotFound(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{"end_device_id": "missing"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestEdevDeleteOOB_MissingID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_EmptyBody(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmEdevDelete, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_MalformedJSON(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmEdevDelete, strings.NewReader("{not json"))
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_UnknownField_Rejected(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"extra_garbage":  true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (DisallowUnknownFields)", rr.Code)
	}
}

// --- /test/mutations/derprog-primacy ---

func TestDERProgPrimacy_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{Primacy: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	primacy := uint8(42)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"program_id":    "prog-1",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	got, err := stores.DERPrograms.Get(ctx, "edev-1", "prog-1")
	if err != nil {
		t.Fatalf("post-mutation get: %v", err)
	}
	if got.Primacy != 42 {
		t.Fatalf("primacy = %d, want 42", got.Primacy)
	}
}

func TestDERProgPrimacy_ProgramNotFound(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(5)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-X",
		"program_id":    "prog-X",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestDERProgPrimacy_MissingPrimacy(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"program_id":    "prog-1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERProgPrimacy_MissingEdevID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(1)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"program_id": "prog-1",
		"primacy":    primacy,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERProgPrimacy_MissingProgramID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(1)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// --- /test/mutations/derctl-add ---

func TestDERControlAdd_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	// Verify the control landed under the deep composite key
	// used by the production router.
	got, err := stores.DERControls.Get(ctx, "edev-1/fsa-1/prog-1", "ctl-1")
	if err != nil {
		t.Fatalf("post-mutation get: %v", err)
	}
	_ = got
}

func TestDERControlAdd_ParentProgramMissing(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-X",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestDERControlAdd_DuplicateID_Conflict(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	// First add: should succeed
	first := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first add status = %d, want 201", first.Code)
	}
	// Second add with same ID: 409 Conflict
	second := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("second add status = %d, want 409", second.Code)
	}
}

func TestDERControlAdd_MissingScopeFields(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	// Missing fsa_id
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERControlAdd_MissingControlID(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// --- Method-not-POST sanity ---

func TestMutationSurface_GET_NotAllowed(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodGet, tmEdevDelete, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	// http.ServeMux returns 405 when the method-qualified pattern misses.
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
