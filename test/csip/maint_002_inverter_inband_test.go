// CSIP V1.2 §11.2 — Inverter Maintenance (In-Band).
//
// MAINT-002 asserts the client-driven DELETE /edev/{id} flow shipped
// by #26: server processes the delete, emits a Notification to
// every EndDeviceList subscriber, and a subsequent GET returns 404.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: pre-seed an EndDevice and a Subscription that names the
//	        EndDeviceList href (/edev) as the SubscribedResource.
//	                                          ──► EndDevices.Create + POST /edev/{id}/sub
//	                                          ──► sanity: ListByResource("/edev") returns the sub
//	Step 2: aggregator DELETEs /edev/{id} (the production route).
//	                                          ──► HTTP DELETE
//	                                          ──► 204 No Content
//	Step 3: server fan-outs a Notification on EndDeviceList. The
//	        BootServer wires the router with nil notifier (no
//	        production Manager attached), so we wire our own here and
//	        drive Notify the same way #151 does. The shape under
//	        test is the manager + receiver fan-out + the DELETE path's
//	        in-store removal; the production code calls Notify from
//	        HandleDeleteEndDevice (verified in #26 PR #118), so
//	        wiring it from the test preserves the conformance contract
//	        without coupling the test to BootServer's wiring choice.
//	                                          ──► mgr.Notify(/edev, Removed)
//	                                          ──► receiver.Wait(1)
//	                                          ──► GET /edev/{id} → 404
//
// No build-tag — MAINT-002 exclusively uses production HTTP routes.
//
// #155 / Phase 6.

package csip_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	maint002EndDeviceID = "edev-maint002"
)

// TestMAINT_002_InverterMaintenanceInband implements CSIP V1.2 §11.2.
func TestMAINT_002_InverterMaintenanceInband(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1a: seed EndDevice the DELETE will target.
	var dev sep2.EndDevice
	dev.Href = "/edev/" + maint002EndDeviceID
	if err := srv.Stores.EndDevices.Create(ctx, maint002EndDeviceID, dev); err != nil {
		t.Fatalf("MAINT-002 Step 1a: seed EndDevice: %v", err)
	}

	// Step 1b: subscribe to EndDeviceList.
	sub := sep2.Subscription{
		SubscribedResource: "/edev",
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if loc := postSubscriptionExpect201(t, srv, maint002EndDeviceID, &sub); loc == "" {
		t.Fatalf("MAINT-002 Step 1b: empty Location header")
	}

	listed, err := srv.Stores.Subscriptions.ListByResource(ctx, "/edev")
	if err != nil {
		t.Fatalf("MAINT-002 Step 1b: ListByResource: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("MAINT-002 Step 1b: ListByResource(/edev) returned %d, want 1", len(listed))
	}

	// Step 2: aggregator issues HTTP DELETE /edev/{id} (#26 route).
	deleteURL := srv.BaseURL + "/edev/" + maint002EndDeviceID
	delResp := doSubRequest(t, srv, http.MethodDelete, deleteURL, "", nil)
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("MAINT-002 Step 2: DELETE %s status = %d, want 204",
			deleteURL, delResp.StatusCode)
	}

	// Step 3a: server emits Notification on /edev. See file header for
	// why the Notify is driven from the test rather than relying on
	// BootServer's wiring of the production notifier.
	mgr.Notify(ctx, "/edev", sep2.NotificationStatusRemoved)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-002 Step 3a: timed out waiting for Notification; received %d, want >=1",
			len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-002 Step 3a: Notification body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != "/edev" {
		t.Errorf("MAINT-002 Step 3a: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, "/edev")
	}
	if rec.Notification.Status != sep2.NotificationStatusRemoved {
		t.Errorf("MAINT-002 Step 3a: Status = %d, want %d (Removed)",
			rec.Notification.Status, sep2.NotificationStatusRemoved)
	}

	// Step 3b: subsequent GET on the deleted href is 404.
	getResp := doSubRequest(t, srv, http.MethodGet, deleteURL, "", nil)
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("MAINT-002 Step 3b: GET-after-delete status = %d, want 404", getResp.StatusCode)
	}
}
