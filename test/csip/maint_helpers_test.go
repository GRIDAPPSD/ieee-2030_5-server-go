//go:build csip_test_hooks

// CSIP V1.2 MAINT-* helpers — shared HTTP poster and seeding utilities
// for the mutation-driven maintenance tests (MAINT-001/003/004/005/006).
//
// MAINT-002 does NOT use this file; it drives the production DELETE
// /edev/{id} surface and lives in an untagged test file. ERR-002 also
// does not use this file — it builds its own subscription.Manager and
// receives via NotificationReceiver(WithStatusCode(400)).
//
// All helpers in this file assume the server was booted with
// SEP2_TEST_MUTATION_TOKEN set in env (see TestMain in
// maint_testmain_test.go) AND the binary was built with
// `-tags csip_test_hooks`. Without those, RegisterMutationHandlers is a
// no-op and every mutation POST returns 404 — surfaced as a t.Fatal at
// the call site.
//
// IEEE-091 / Phase 6.

package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const (
	maintMutationToken    = "ieee-091-maint-token"
	maintMutationTokenEnv = "SEP2_TEST_MUTATION_TOKEN"
	maintMutationHeader   = "X-CSIP-Test-Token"

	mutateEdevDeleteOOB      = "/test/mutations/edev-delete-oob"
	mutateFSASwap            = "/test/mutations/fsa-swap"
	mutateDERControlAdd      = "/test/mutations/derctl-add"
	mutateDERProgPrimacy     = "/test/mutations/derprog-primacy"
	mutateSubscriptionCancel = "/test/mutations/subscription-cancel"
)

// postMutationJSON marshals body, POSTs it to the booted server at the
// given mutation path with the auth header, and returns the parsed
// status code plus response body. Failure to dial or marshal is fatal;
// non-2xx status is the caller's call (404 on a wrong route is a real
// signal worth distinguishing from a write failure).
func postMutationJSON(t *testing.T, srv *csiptest.BootedServer, path string, body any) (status int, respBody []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("mutation %s: encode body: %v", path, err)
		}
	}
	url := srv.BaseURL + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("mutation %s: build request: %v", path, err)
	}
	req.Header.Set(maintMutationHeader, maintMutationToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("mutation %s: dial: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body2, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("mutation %s: read body: %v", path, err)
	}
	return resp.StatusCode, body2
}

// seedEndDevice creates a minimal EndDevice in the booted server's store.
// The MAINT-* tests need an EndDevice present before exercising the
// delete / FSA-swap / derctl-add / primacy / subscription-cancel paths.
// Test failure on Create is fatal — without a seeded device the test
// can't reach the assertion under examination.
func seedEndDevice(t *testing.T, srv *csiptest.BootedServer, edevID string) {
	t.Helper()
	var dev sep2.EndDevice
	dev.Href = "/edev/" + edevID
	if err := srv.Stores.EndDevices.Create(context.Background(), edevID, dev); err != nil {
		t.Fatalf("seed EndDevice %q: %v", edevID, err)
	}
}
