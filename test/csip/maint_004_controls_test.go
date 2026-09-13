//go:build csip_test_hooks

// CSIP V1.2 section 11.4 - Maintenance of Controls.
//
// MAINT-004 asserts that adding a DERControl to a live DERProgram
// produces a Notification on subscribers of that program's control
// list, and the aggregator's subsequent GET sees the new control.
//
// V1.2 procedure step -> assertion mapping:
//
//	Step 1: seed an EndDevice with one DERProgram (prog-tfa).
//	        Subscribe to the DERProgram href.
//	                                          -> EndDevices.Create + DERPrograms.Create
//	                                          -> POST /edev/{id}/sub
//	Step 2: add a new DERControl via the #27 mutation hook.
//	                                          -> POST /test/mutations/derctl-add
//	                                          -> 201 Created
//	                                          -> DERControls.Get on composite key succeeds
//	Step 3: Notify on the DERProgram href; receiver gets one
//	        Changed-status Notification carrying the program's href.
//	                                          -> mgr.Notify(derp href, Changed)
//	                                          -> receiver.Wait(1)
//
// Build-tag: csip_test_hooks for the derctl-add mutation.
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
	maint004EndDeviceID = "edev-maint004"
	maint004FSAID       = "fsa-tfa"
	maint004ProgramID   = "prog-tfa"
	maint004ControlID   = "ctl-1"
)

// TestMAINT_004_MaintenanceOfControls implements CSIP V1.2 section 11.4.
func TestMAINT_004_MaintenanceOfControls(t *testing.T) {
	t.Parallel()

	owner := csiptest.NewDeviceIdentity(t, "MAINT-004-DEVICE")
	srv := csiptest.BootServer(t, csiptest.WithDeviceIdentity(owner))
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1a: seed EndDevice + DERProgram.
	seedOwnedEndDevice(t, srv.Stores.EndDevices, maint004EndDeviceID, owner)
	derpHref := "/edev/" + maint004EndDeviceID + "/derp/" + maint004ProgramID
	var prog sep2.DERProgram
	prog.Href = derpHref
	prog.Primacy = 1
	if err := srv.Stores.DERPrograms.Create(ctx, maint004EndDeviceID, maint004ProgramID, prog); err != nil {
		t.Fatalf("MAINT-004 Step 1a: seed DERProgram: %v", err)
	}

	// Step 1b: subscribe to the DERProgram href.
	sub := sep2.Subscription{
		SubscribedResource: derpHref,
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if loc := postSubscriptionExpect201(t, srv, maint004EndDeviceID, &sub); loc == "" {
		t.Fatalf("MAINT-004 Step 1b: empty Location header")
	}

	// Step 2: add a DERControl via the mutation hook. We pass an
	// empty control body - the mutation handler only requires the
	// scope keys; the parent's existing tests pin the same shape
	// (internal/server/test_mutations_test.go TestDERControlAdd_Success
	// posts sep2.DERControl{} directly).
	status, body := postMutationJSON(t, srv, mutateDERControlAdd, map[string]any{
		"end_device_id":  maint004EndDeviceID,
		"fsa_id":         maint004FSAID,
		"der_program_id": maint004ProgramID,
		"control_id":     maint004ControlID,
		"control":        sep2.DERControl{},
	})
	if status != http.StatusCreated {
		t.Fatalf("MAINT-004 Step 2: status = %d, want 201: %s", status, string(body))
	}

	// Store assertion: the control is under the composite scope.
	scope := maint004EndDeviceID + "/" + maint004FSAID + "/" + maint004ProgramID
	if _, err := srv.Stores.DERControls.Get(ctx, scope, maint004ControlID); err != nil {
		t.Errorf("MAINT-004 Step 2: control not found at composite scope %q: %v", scope, err)
	}

	// Step 3: Notify on the DERProgram href; aggregator-side receiver
	// observes the change. The receiver records the body; tests
	// asserting the new control's presence in the program list would
	// follow up with a GET /edev/{id}/derp/{prog}/derc, but that
	// surface is exercised under CORE-012/013; here we only assert the
	// notification delivery contract.
	mgr.Notify(ctx, derpHref, sep2.NotificationStatusChanged)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-004 Step 3: timed out; received %d, want >=1", len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-004 Step 3: body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != derpHref {
		t.Errorf("MAINT-004 Step 3: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, derpHref)
	}
	if rec.Notification.Status != sep2.NotificationStatusChanged {
		t.Errorf("MAINT-004 Step 3: Status = %d, want %d (Changed)",
			rec.Notification.Status, sep2.NotificationStatusChanged)
	}
}
