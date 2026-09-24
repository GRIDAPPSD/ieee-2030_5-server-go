package assembly_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// GRIDAPPSD/ieee-2030_5-server-go#435: the ownership gate validates the
// caller against path {id} and never sees {subId}, so the cross-device
// check has to live in the handler. This drives that check through the
// assembled router with the same identity and management wiring the gate
// itself uses, rather than mounting the handler alone.
//
// ADR-007 B2's write allow-list (issue 510) does not grant
// DELETE /edev/{id}/sub/{subId} to a manager at all (X-06, write half): an
// aggregator posts to its own SubscriptionListLink, not a managed device's.
// So a manager's delete of any subscription under a managed {id} is now
// refused by the ownership gate itself, before the handler's own
// href-scoping check ever runs; #435's handler-level scoping is exercised
// here only through self access, which the allow-list does not touch.
func TestManagement_SubscriptionDeleteIsScopedToItsEndDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)

	seedSub := func(id, ownerID string) {
		t.Helper()
		sub := sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/" + ownerID + "/sub/" + id},
			},
			SubscribedResource: "/edev/" + ownerID,
			NotificationURI:    "http://subscriber.test/notify",
		}
		if err := fleet.stores.Subscriptions.Create(ctx, id, sub); err != nil {
			t.Fatalf("seed subscription %s under %s: %v", id, ownerID, err)
		}
	}
	stillStored := func(t *testing.T, id string) {
		t.Helper()
		if _, err := fleet.stores.Subscriptions.Store.Get(ctx, id); err != nil {
			t.Errorf("%s was removed from the store: %v", id, err)
		}
	}
	removed := func(t *testing.T, id string) {
		t.Helper()
		if _, err := fleet.stores.Subscriptions.Store.Get(ctx, id); err == nil {
			t.Errorf("%s is still stored after a delete that should have removed it", id)
		}
	}

	// subVictim belongs to the manager's managed device (victimID). subChild
	// belongs to the manager's OTHER managed device (secondChildID).
	seedSub("sub-victim", victimID)
	seedSub("sub-child", secondChildID)

	srv := gateServer(t, fleet.stores, gateTestPolicy())

	// A device that owns its own path id but not the subscription's real
	// EndDevice is refused, and the subscription stays stored. callerLFDI
	// owns callerID, not victimID.
	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+callerID+"/sub/sub-victim", callerLFDI, ""); status != http.StatusNotFound {
		t.Errorf("non-owner cross-device delete: status %d, want 404; body=%q", status, raw)
	}
	stillStored(t, "sub-victim")

	// The manager of victimID deletes victimID's own subscription: refused by
	// the gate (X-06 write half is not on the allow-list), left stored.
	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID+"/sub/sub-victim", managerLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("manager delete of managed device's own subscription: status %d, want 403; body=%q", status, raw)
	}
	stillStored(t, "sub-victim")

	// The manager addresses its OTHER managed device's subscription under
	// victimID's path: the gate refuses the manager on this pattern outright
	// now, so the handler's own cross-device scoping check is never reached.
	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID+"/sub/sub-child", managerLFDI, ""); status != http.StatusForbidden {
		t.Errorf("manager cross-device delete under its own managed id: status %d, want 403; body=%q", status, raw)
	}
	stillStored(t, "sub-child")

	// The owner (victimLFDI) deletes its own subscription directly: 204.
	seedSub("sub-victim-2", victimID)
	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID+"/sub/sub-victim-2", victimLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("owner delete of its own subscription: status %d, want 204; body=%q", status, raw)
	}
	removed(t, "sub-victim-2")
}
