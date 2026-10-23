//go:build csip_test_hooks

// CSIP V1.2 section 11.1 - Inverter Maintenance (Out-Of-Band).
//
// MAINT-001 asserts the server-side topology shape when an EndDevice
// is removed from the aggregator's EDList via an out-of-band path
// (the #27 /test/mutations/edev-delete-oob hook, NOT the
// client-driven DELETE on /edev/{id} which is MAINT-002's territory).
//
// V1.2 procedure step -> assertion mapping:
//
//	Step 1: pre-seed an EndDevice and a Subscription that names
//	        the EndDeviceList href (/edev) as the SubscribedResource.
//	                                          -> EndDevices.Create + POST /edev/{id}/sub
//	                                          -> sanity: ListByResource("/edev") returns the sub
//	Step 2: drive the OOB delete via the test mutation hook.
//	                                          -> POST /test/mutations/edev-delete-oob
//	                                          -> 204 No Content
//	                                          -> server's store no longer contains the EndDevice
//	Step 3: server fires a Notification on EndDeviceList (the hook
//	        serves the same handler as DELETE /edev/{id}); aggregator
//	        GETs the deleted href and gets 404.
//	                                          -> receiver.Wait(1)
//	                                          -> GET /edev/{id} -> 404
//
// Steps 1 and 3 are shared with the admin-route variant (runMAINT001 in
// maint_001_admin_delete_test.go). Build-tag: csip_test_hooks is required
// because Step 2 drives the test-only mutation surface. Untagged builds skip
// this file entirely.
//
// #155 / Phase 6.

package csip_test

import (
	"context"
	"net/http"
	"testing"

	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestMAINT_001_InverterMaintenanceOOB implements CSIP V1.2 section 11.1.
func TestMAINT_001_InverterMaintenanceOOB(t *testing.T) {
	t.Parallel()
	runMAINT001(t, func(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, _ *coresub.Manager) {
		// Step 2: OOB delete via the test mutation hook. A 404 here on
		// an otherwise plumb wire means TestMain didn't set the env or
		// the build was untagged; both are caught earlier in this file
		// at the build-tag predicate.
		status, body := postMutationJSON(t, srv, mutateEdevDeleteOOB, map[string]string{
			"end_device_id": maint001EndDeviceID,
		})
		if status != http.StatusNoContent {
			t.Fatalf("MAINT-001 Step 2: POST %s status = %d, want 204: %s",
				mutateEdevDeleteOOB, status, string(body))
		}
	})
}
