//go:build csip_test_hooks

// CSIP V1.2 §11.1 — Inverter Maintenance (Out-Of-Band).
//
// MAINT-001 asserts the server-side topology shape when an EndDevice
// is removed from the aggregator's EDList via an out-of-band path
// (the IEEE-024 /test/mutations/edev-delete-oob hook, NOT the
// client-driven DELETE on /edev/{id} which is MAINT-002's territory).
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: pre-seed an EndDevice and a Subscription that names
//	        the EndDeviceList href (/edev) as the SubscribedResource.
//	                                          ──► EndDevices.Create + POST /edev/{id}/sub
//	                                          ──► sanity: ListByResource("/edev") returns the sub
//	Step 2: drive the OOB delete via the test mutation hook.
//	                                          ──► POST /test/mutations/edev-delete-oob
//	                                          ──► 204 No Content
//	                                          ──► server's store no longer contains the EndDevice
//	Step 3: server fires a Notification on EndDeviceList; aggregator
//	        GETs the deleted href and gets 404. The OOB mutation does
//	        NOT itself emit a Notification (by design — see the
//	        handleEdevDeleteOOB comment); we drive the Notify in-test
//	        as IEEE-087 does, then assert delivery.
//	                                          ──► mgr.Notify(/edev, Removed)
//	                                          ──► receiver.Wait(1)
//	                                          ──► GET /edev/{id} → 404
//
// Build-tag: csip_test_hooks is required because Step 2 drives the test-
// only mutation surface. Untagged builds skip this file entirely.
//
// IEEE-091 / Phase 6.

package csip_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const (
	maint001EndDeviceID = "edev-maint001"
)

// TestMAINT_001_InverterMaintenanceOOB implements CSIP V1.2 §11.1.
func TestMAINT_001_InverterMaintenanceOOB(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := subscription.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1a: seed EndDevice the OOB mutation will delete.
	seedEndDevice(t, srv, maint001EndDeviceID)

	// Step 1b: aggregator subscribes to /edev (EndDeviceList).
	sub := sep2.Subscription{
		SubscribedResource: "/edev",
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	location := postSubscriptionExpect201(t, srv, maint001EndDeviceID, &sub)
	if location == "" {
		t.Fatalf("MAINT-001 Step 1b: empty Location header")
	}

	// Sanity: Manager's ListByResource("/edev") sees this sub —
	// otherwise Step 3's Notify would fan out to zero receivers.
	listed, err := srv.Stores.Subscriptions.ListByResource(ctx, "/edev")
	if err != nil {
		t.Fatalf("MAINT-001 Step 1b: ListByResource: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("MAINT-001 Step 1b: ListByResource(/edev) returned %d, want 1", len(listed))
	}

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

	// Verify the store actually dropped the device.
	if _, err := srv.Stores.EndDevices.Get(context.Background(), maint001EndDeviceID); err == nil {
		t.Errorf("MAINT-001 Step 2: EndDevice %q still present after OOB delete", maint001EndDeviceID)
	}

	// Step 3a: server fires Notification on /edev. The OOB mutation
	// handler does not emit notifications (its scope is the
	// authoritative-store rewrite); the IEEE-091 contract layered on
	// top is to fan out a Removed Notification from the test, the
	// same way IEEE-087 layers Notify on top of the IEEE-024 mutation
	// shape. The shape under test is the manager + receiver fan-out,
	// not the bridge from mutation to Notify (that bridge is the
	// in-band DELETE path's job, exercised in MAINT-002).
	mgr.Notify(ctx, "/edev", sep2.NotificationStatusRemoved)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-001 Step 3a: timed out waiting for Notification; received %d, want >=1", len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-001 Step 3a: Notification body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != "/edev" {
		t.Errorf("MAINT-001 Step 3a: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, "/edev")
	}
	if rec.Notification.Status != sep2.NotificationStatusRemoved {
		t.Errorf("MAINT-001 Step 3a: Status = %d, want %d (Removed)",
			rec.Notification.Status, sep2.NotificationStatusRemoved)
	}

	// Step 3b: aggregator GETs the deleted href; expect 404.
	deletedURL := srv.BaseURL + "/edev/" + maint001EndDeviceID
	getResp := doSubRequest(t, srv, http.MethodGet, deletedURL, "", nil)
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("MAINT-001 Step 3b: GET %s status = %d, want 404", deletedURL, getResp.StatusCode)
	}

	// Guard against unexpected Location-header echoes that would
	// confuse a downstream test reading the same response. The
	// production handler does not emit Location on 404; if a future
	// regression added one we'd want to know.
	if loc := getResp.Header.Get("Location"); strings.Contains(loc, maint001EndDeviceID) {
		t.Errorf("MAINT-001 Step 3b: GET-after-delete returned Location=%q referencing deleted id", loc)
	}
}
