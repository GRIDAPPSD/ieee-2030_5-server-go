package assembly_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// GRIDAPPSD/ieee-2030_5-server-go#721's second case: a Registration left
// behind by a failed DELETE must be reachable by a retried HTTP DELETE, not
// just by a direct store retry.
//
// The reproduction needs a Registration store that fails ONLY its Delete
// call while Get keeps working, which storetest.Fault cannot do: arming it
// fails every method uniformly, so it would trip probeDelete's own
// readability check before ever reaching the removal this test is about.
// deleteFailingRegistrations is the narrow test double that isolates the
// removal step alone, the "a failed persistence flush" case the issue names.

var errRegistrationDeleteFailed = errors.New("test: registration delete failed")

type deleteFailingRegistrations struct {
	store.ResourceStore[sep2.Registration]
	armed bool
}

func (s *deleteFailingRegistrations) Delete(ctx context.Context, id string) error {
	if s.armed {
		return errRegistrationDeleteFailed
	}
	return s.ResourceStore.Delete(ctx, id)
}

// TestEndDeviceDelete_RegistrationDeleteFailureLeavesTheDeviceInPlace pins
// the fix: with the Registration removed before the EndDevice, a failure
// removing it leaves the EndDevice untouched, so a retried HTTP DELETE
// reaches the store again instead of the ownership gate answering 404 on an
// id that is already gone.
func TestEndDeviceDelete_RegistrationDeleteFailureLeavesTheDeviceInPlace(t *testing.T) {
	t.Parallel()

	stores := testStores()
	regs := &deleteFailingRegistrations{ResourceStore: memory.NewRegistrationStore()}
	stores.Registrations = regs
	stores.RegistrationPolicy = testRegistrationPolicy()

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), testSFDI, testLFDI, nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	postFixtureDevice(t, srv.URL)

	// Control: the device is provisioned, so its Registration is really
	// being served before anything is armed.
	controlResp, err := srv.Client().Get(srv.URL + "/edev/1/rg")
	if err != nil {
		t.Fatalf("control GET /edev/1/rg: %v", err)
	}
	_ = controlResp.Body.Close()
	if controlResp.StatusCode != http.StatusOK {
		t.Fatalf("control: GET /edev/1/rg = %d, want 200", controlResp.StatusCode)
	}

	regs.armed = true

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first DELETE /edev/1 = %d, want 500 while the Registration delete fails", resp.StatusCode)
	}

	getResp, err := srv.Client().Get(srv.URL + "/edev/1")
	if err != nil {
		t.Fatalf("GET /edev/1: %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev/1 after the failed delete = %d, want 200: the device must still be present "+
			"so a retry reaches the store again instead of the ownership gate's 404", getResp.StatusCode)
	}

	regs.armed = false

	req2, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/1", nil)
	if err != nil {
		t.Fatalf("new retried DELETE request: %v", err)
	}
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatalf("retried DELETE /edev/1: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("retried DELETE /edev/1 = %d, want 204", resp2.StatusCode)
	}

	getResp2, err := srv.Client().Get(srv.URL + "/edev/1")
	if err != nil {
		t.Fatalf("GET /edev/1 after the retried delete: %v", err)
	}
	_ = getResp2.Body.Close()
	if getResp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edev/1 after the retried delete = %d, want 404", getResp2.StatusCode)
	}
}
