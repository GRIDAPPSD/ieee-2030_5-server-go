//go:build csip_test_hooks

// CSIP V1.2 §11.5 — Primacy Swap.
//
// MAINT-005 asserts that updating a DERProgram's primacy field via the
// #27 mutation hook produces a Notification, and the underlying
// store reflects the new value.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: seed an EndDevice + DERProgram with primacy=1.
//	        Subscribe to the DERProgram href.
//	                                          ──► EndDevices.Create + DERPrograms.Create
//	                                          ──► POST /edev/{id}/sub
//	Step 2: drive the primacy swap to primacy=7.
//	                                          ──► POST /test/mutations/derprog-primacy
//	                                          ──► 204 No Content
//	                                          ──► store: program.Primacy == 7
//	Step 3: Notify on the DERProgram href; receiver gets one
//	        Changed-status Notification.
//	                                          ──► mgr.Notify(derp href, Changed)
//	                                          ──► receiver.Wait(1)
//
// Build-tag: csip_test_hooks for the derprog-primacy mutation.
//
// #155 / Phase 6.

package csip_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	maint005EndDeviceID = "edev-maint005"
	maint005ProgramID   = "prog-primacy"
	maint005FromPrimacy = uint8(1)
	maint005ToPrimacy   = uint8(7)
)

// TestMAINT_005_PrimacySwap implements CSIP V1.2 §11.5.
func TestMAINT_005_PrimacySwap(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1a: seed EndDevice + DERProgram with primacy=1.
	seedEndDevice(t, srv, maint005EndDeviceID)
	derpHref := "/edev/" + maint005EndDeviceID + "/derp/" + maint005ProgramID
	var prog sep2.DERProgram
	prog.Href = derpHref
	prog.Primacy = maint005FromPrimacy
	if err := srv.Stores.DERPrograms.Create(ctx, maint005EndDeviceID, maint005ProgramID, prog); err != nil {
		t.Fatalf("MAINT-005 Step 1a: seed DERProgram: %v", err)
	}

	// Step 1b: subscribe.
	sub := sep2.Subscription{
		SubscribedResource: derpHref,
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if loc := postSubscriptionExpect201(t, srv, maint005EndDeviceID, &sub); loc == "" {
		t.Fatalf("MAINT-005 Step 1b: empty Location header")
	}

	// Step 2: primacy swap mutation.
	to := maint005ToPrimacy
	status, body := postMutationJSON(t, srv, mutateDERProgPrimacy, map[string]any{
		"end_device_id": maint005EndDeviceID,
		"program_id":    maint005ProgramID,
		"primacy":       to,
	})
	if status != http.StatusNoContent {
		t.Fatalf("MAINT-005 Step 2: status = %d, want 204: %s", status, string(body))
	}

	// Store assertion: primacy is now the new value.
	updated, err := srv.Stores.DERPrograms.Get(ctx, maint005EndDeviceID, maint005ProgramID)
	if err != nil {
		t.Fatalf("MAINT-005 Step 2: re-read DERProgram: %v", err)
	}
	if updated.Primacy != maint005ToPrimacy {
		t.Errorf("MAINT-005 Step 2: Primacy = %d, want %d", updated.Primacy, maint005ToPrimacy)
	}

	// Step 3: Notify on the DERProgram href.
	mgr.Notify(ctx, derpHref, sep2.NotificationStatusChanged)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-005 Step 3: timed out; received %d, want >=1", len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-005 Step 3: body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != derpHref {
		t.Errorf("MAINT-005 Step 3: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, derpHref)
	}
	if rec.Notification.Status != sep2.NotificationStatusChanged {
		t.Errorf("MAINT-005 Step 3: Status = %d, want %d (Changed)",
			rec.Notification.Status, sep2.NotificationStatusChanged)
	}
}
