//go:build !csip_test_hooks

// Verifies the test-only mutation surface is absent from default
// production builds (no csip_test_hooks tag). The endpoints must miss
// the mux and return 404 regardless of token or env.

package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
)

func TestMutationSurface_AbsentWithoutBuildTag(t *testing.T) {
	// Even with the env var set, no routes should be registered when the
	// build tag is off — the gated file isn't compiled, so the no-op
	// stub runs and the mux has no /test/mutations/ handler.
	t.Setenv("SEP2_TEST_MUTATION_TOKEN", "anything")
	stores := newTestStores()
	cfg := &config.Config{}
	h := server.NewRouter(cfg, stores, nil, "", "", nil)

	paths := []string{
		"/test/mutations/edev-delete-oob",
		"/test/mutations/derprog-primacy",
		"/test/mutations/derctl-add",
		"/test/mutations/time-advance",
		"/test/mutations/fsa-swap",
	}
	for _, p := range paths {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(map[string]string{"end_device_id": "x"})
		req := httptest.NewRequest(http.MethodPost, p, &buf)
		req.Header.Set("X-CSIP-Test-Token", "anything")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404 (surface should not exist in production builds)", p, rr.Code)
		}
	}
}
