//go:build csip_test_hooks

// CSIP V1.2 §11.3 — Group Maintenance (EndDevice → FSA reassignment).
//
// MAINT-003 asserts the FSA-swap flow: the server moves an EndDevice's
// FunctionSetAssignment from one FSA id to another (the IEEE-078
// /test/mutations/fsa-swap hook), and the aggregator observes a
// Notification on the FSAList href so it can update its subscription
// topology.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: seed an EndDevice with a FSA at id "fsa-old"; subscribe
//	        to the EndDevice's FSAList (/edev/{id}/fsa).
//	                                          ──► EndDevices.Create + FSAs.ForParent.Create
//	                                          ──► POST /edev/{id}/sub on the FSA list href
//	Step 2: drive the FSA-swap mutation to move the FSA from "fsa-old"
//	        to "fsa-new".
//	                                          ──► POST /test/mutations/fsa-swap
//	                                          ──► 204 No Content
//	                                          ──► store: GET fsa-new succeeds, fsa-old gone
//	Step 3: server emits FSAList Notification. The mutation handler
//	        is intentionally store-only; we drive Notify in-test the
//	        same way IEEE-087 does. The aggregator (test receiver)
//	        observes the Notification and reacts (in production it
//	        would re-subscribe to the new FSA's DERProgramList; that
//	        re-subscribe step is plan-1's concern. Server-side scope
//	        ends at the Notification delivery).
//	                                          ──► mgr.Notify(/edev/{id}/fsa, Changed)
//	                                          ──► receiver.Wait(1)
//
// Build-tag: csip_test_hooks is required for the fsa-swap mutation.
//
// IEEE-091 / Phase 6.

package csip_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	coresub "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/handlers/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
)

const (
	maint003EndDeviceID = "edev-maint003"
	maint003FromFSA     = "fsa-old"
	maint003ToFSA       = "fsa-new"
)

// TestMAINT_003_GroupMaintenanceFSAReassignment implements CSIP V1.2 §11.3.
func TestMAINT_003_GroupMaintenanceFSAReassignment(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1a: seed EndDevice + initial FSA at id "fsa-old".
	seedEndDevice(t, srv, maint003EndDeviceID)
	fromHref := "/edev/" + maint003EndDeviceID + "/fsa/" + maint003FromFSA
	fsa := sep2.FunctionSetAssignments{
		Resource: sep2.Resource{Href: fromHref},
	}
	if err := srv.Stores.FSAs.ForParent(maint003EndDeviceID).Create(ctx, maint003FromFSA, fsa); err != nil {
		t.Fatalf("MAINT-003 Step 1a: seed FSA %q: %v", maint003FromFSA, err)
	}

	// Step 1b: subscribe to the FSAList.
	fsaListHref := "/edev/" + maint003EndDeviceID + "/fsa"
	sub := sep2.Subscription{
		SubscribedResource: fsaListHref,
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if loc := postSubscriptionExpect201(t, srv, maint003EndDeviceID, &sub); loc == "" {
		t.Fatalf("MAINT-003 Step 1b: empty Location header")
	}

	// Step 2: fsa-swap mutation.
	status, body := postMutationJSON(t, srv, mutateFSASwap, map[string]string{
		"end_device_id": maint003EndDeviceID,
		"from_fsa":      maint003FromFSA,
		"to_fsa":        maint003ToFSA,
	})
	if status != http.StatusNoContent {
		t.Fatalf("MAINT-003 Step 2: status = %d, want 204: %s", status, string(body))
	}

	// Store assertion: fsa-new exists, fsa-old gone.
	if _, err := srv.Stores.FSAs.Get(ctx, maint003EndDeviceID, maint003ToFSA); err != nil {
		t.Errorf("MAINT-003 Step 2: target FSA %q not found post-swap: %v", maint003ToFSA, err)
	}
	if _, err := srv.Stores.FSAs.Get(ctx, maint003EndDeviceID, maint003FromFSA); err == nil {
		t.Errorf("MAINT-003 Step 2: source FSA %q still present post-swap", maint003FromFSA)
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MAINT-003 Step 2: unexpected error checking source FSA: %v", err)
	}

	// Step 3: drive FSAList Notification.
	mgr.Notify(ctx, fsaListHref, sep2.NotificationStatusChanged)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-003 Step 3: timed out; received %d, want >=1", len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-003 Step 3: body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != fsaListHref {
		t.Errorf("MAINT-003 Step 3: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, fsaListHref)
	}
	if rec.Notification.Status != sep2.NotificationStatusChanged {
		t.Errorf("MAINT-003 Step 3: Status = %d, want %d (Changed)",
			rec.Notification.Status, sep2.NotificationStatusChanged)
	}
}
