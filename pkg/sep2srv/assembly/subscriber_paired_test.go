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
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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

// faultyDevices fails Get while fault is armed.
type faultyDevices struct {
	store.EndDeviceStore
	fault *storetest.Fault
}

func (f faultyDevices) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	if err := f.fault.Err(); err != nil {
		return sep2.EndDevice{}, err
	}
	return f.EndDeviceStore.Get(ctx, id)
}

// GET /edev is a route the gate does not decide, so the pair is the only
// thing that can withhold a delivery for it; a device read that fails must not
// let a paired DER's subscription through.
func TestSubscriberCheck_DeviceReadErrorWithholdsEvenOnAnUngatedRoute(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	fault := &storetest.Fault{}
	fleet.stores.EndDevices = faultyDevices{EndDeviceStore: fleet.stores.EndDevices, fault: fault}
	check := subscriberCheckFor(t, fleet)
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + victimID + "/sub/s1"}},
		SubscribedResource:   "/edev",
	}

	if err := check(context.Background(), sub); !errors.Is(err, coresub.ErrSubscribedResourceRefused) {
		t.Fatalf("control: readable paired record on /edev: err = %v, want the pair to withhold", err)
	}
	if err := fleet.managers.Unassign(context.Background(), victimLFDI); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if err := check(context.Background(), sub); err != nil {
		t.Fatalf("control: unpaired on /edev: err = %v, want admitted", err)
	}
	if err := fleet.managers.Assign(context.Background(), managerLFDI, victimLFDI); err != nil {
		t.Fatalf("assign: %v", err)
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	err := check(context.Background(), sub)
	if !errors.Is(err, storetest.ErrBackendUnavailable) {
		t.Fatalf("err = %v, want the device read failure to withhold", err)
	}
	if !lineWith(buf.String(), denialLogMarker, "reason=subscriber-unread", `"/edev/`+victimID+`/sub/s1"`) {
		t.Errorf("no budgeted line for the unread subscriber; log=%q", buf.String())
	}
}

// A standing pair or an outage is logged within the denial budget only: the
// manager adds no line of its own per notification.
func TestSubscriberCheck_StandingWithholdLogsWithinBudgetOnly(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	mgr := coresub.NewManager(fleet.stores.Subscriptions, 1, 8, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	mgr.SetSubscriberCheck(subscriberCheckFor(t, fleet))
	rcv := newNotificationReceiver(t)
	for _, c := range []struct{ id, href string }{{victimID, "/edev/" + victimID + "/sub/s1"}, {unmanagedID, "/edev/" + unmanagedID + "/sub/s1"}} {
		if err := fleet.stores.Subscriptions.Create(context.Background(), "sub-"+c.id, sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: c.href}},
			SubscribedResource:   "/edev/" + c.id + "/der",
			NotificationURI:      rcv.srv.URL,
			Limit:                10,
		}); err != nil {
			t.Fatalf("create subscription: %v", err)
		}
	}
	// The unmanaged device's resource is gated and readable by its owner, so
	// the manager line count below comes from the paired subscription alone.
	for range 20 {
		mgr.Notify(context.Background(), "/edev/"+victimID+"/der", sep2.NotificationStatusDefault)
	}
	if n := strings.Count(buf.String(), "not notifying subscription"); n != 0 {
		t.Errorf("manager wrote %d unbudgeted lines for a recorded withhold, want 0", n)
	}
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if lineWith(line, denialLogMarker, "reason=managed-subscriber") {
			n++
		}
	}
	if n < 1 || n > 5 {
		t.Errorf("budgeted lines after 20 notifications = %d, want 1 to 5", n)
	}
}
