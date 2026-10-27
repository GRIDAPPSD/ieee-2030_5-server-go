package assembly_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// A subscription a device made before it was paired is withheld while the pair
// stands and delivers again once the pair is removed. The aggregator's own
// subscription to the same resource is unaffected throughout.
func TestSubscriberCheck_PairedDERSubscriptionIsWithheldUntilUnpaired(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	unpairB(t, f)
	managers := f.stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	rcvB, rcvM := newNotificationReceiver(t), newNotificationReceiver(t)

	if got := subscribe(t, f.b, subDevBID, "/edev/5/frp", rcvB.srv.URL); got != http.StatusCreated {
		t.Fatalf("B subscribing before the pair: status %d, want 201", got)
	}
	if err := managers.Assign(context.Background(), subMgrLFDI, deviceLFDIB); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got := subscribe(t, f.m, subMgrID, "/edev/5/frp", rcvM.srv.URL); got != http.StatusCreated {
		t.Fatalf("manager subscribing to the managed device: status %d, want 201", got)
	}

	postWindowRequest(t, f.b, subDevBID)
	rcvM.waitFor(t, 1)
	assertNothingFor(t, rcvB, "paired B")
	if n := storedFor(t, f.stores, "/edev/5/frp"); n != 2 {
		t.Errorf("stored subscriptions while paired = %d, want 2 (withheld, not deleted)", n)
	}

	if err := managers.Unassign(context.Background(), deviceLFDIB); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	postWindowRequest(t, f.b, subDevBID)
	got := rcvB.waitFor(t, 1)
	if got[0].SubscribedResource != "/edev/5/frp" || got[0].Resource != "/edev/5/frp" {
		t.Errorf("B's notification after unassign = %+v, want one for /edev/5/frp", got[0])
	}
}

func subscriberCheckFor(t *testing.T, fleet managementFleet) coresub.SubscriberCheck {
	t.Helper()
	return assembly.NewSubscriberCheck(assembly.RouterConfig{}, fleet.stores)
}

func TestSubscriberCheck_PairedWithholdsFailsClosedAndLogsWithinBudget(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	check := subscriberCheckFor(t, fleet)
	ctx := context.Background()
	paired := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + victimID + "/sub/s1"}},
		SubscribedResource:   "/edev/" + victimID + "/der",
	}
	unpaired := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + unmanagedID + "/sub/s1"}},
		SubscribedResource:   "/edev/" + unmanagedID + "/der",
	}

	if err := check(ctx, unpaired); err != nil {
		t.Errorf("control: unpaired subscriber withheld: %v", err)
	}
	for range 20 {
		err := check(ctx, paired)
		if !errors.Is(err, coresub.ErrSubscribedResourceRefused) {
			t.Fatalf("paired subscriber: err = %v, want a refusal", err)
		}
	}
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if lineWith(line, denialLogMarker, `caller="`+victimLFDI+`"`, "reason=managed-subscriber", `"/edev/`+victimID+`/sub/s1"`) {
			n++
		}
	}
	if n < 1 || n > 5 {
		t.Errorf("managed-subscriber lines for one caller after 20 withheld deliveries = %d, want 1 to 5", n)
	}

	if err := fleet.managers.Unassign(ctx, victimLFDI); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if err := check(ctx, paired); err != nil {
		t.Errorf("after unassign: err = %v, want delivery admitted", err)
	}
}

func TestSubscriberCheck_ManagementStoreErrorWithholds(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	fault := &storetest.Fault{}
	fleet.stores.EndDeviceManagers = faultyManagers{inner: fleet.managers, fault: fault}
	check := subscriberCheckFor(t, fleet)
	fault.Arm(storetest.ErrBackendUnavailable)

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + unmanagedID + "/sub/s1"}},
		SubscribedResource:   "/edev/" + unmanagedID + "/der",
	}
	err := check(context.Background(), sub)
	if !errors.Is(err, storetest.ErrBackendUnavailable) {
		t.Fatalf("err = %v, want the store failure to withhold", err)
	}
	if !lineWith(buf.String(), denialLogMarker, `caller="`+unmanagedLFDI+`"`, "reason=management-store-error") {
		t.Errorf("no budgeted line for the store failure; log=%q", buf.String())
	}
}
