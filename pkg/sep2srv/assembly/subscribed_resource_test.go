package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A Subscription is held to the read access of its subscriber: device A owns
// /edev/3, device B owns /edev/5, and manager M owns /edev/7 and manages B.
// Each device talks to its own router, as each would over its own TLS
// session; the routers share one store set and one subscription Manager.

const (
	subDevAID  = "3"
	subDevBID  = "5"
	subMgrID   = "7"
	subMgrLFDI = "AA00000000000000000000000000000000000077"
)

type subscribedResourceFleet struct {
	stores *assembly.Stores
	a      *httptest.Server
	b      *httptest.Server
	m      *httptest.Server
}

func fixedIdentityPolicy(lfdi string) assembly.AuthPolicy {
	p := testAuthPolicy()
	p.Identity = func(context.Context) (string, string, bool) { return lfdi, "", true }
	return p
}

func newSubscribedResourceFleet(t *testing.T) subscribedResourceFleet {
	t.Helper()
	stores := testStores()
	seedDevice(t, stores.EndDevices, subDevAID, deviceLFDIA, "")
	seedDevice(t, stores.EndDevices, subDevBID, deviceLFDIB, "")
	seedDevice(t, stores.EndDevices, subMgrID, subMgrLFDI, "")
	managers, ok := stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	if !ok {
		t.Fatalf("EndDeviceManagers is %T, want the memory store", stores.EndDeviceManagers)
	}
	if err := managers.Assign(context.Background(), subMgrLFDI, deviceLFDIB); err != nil {
		t.Fatalf("assign manager: %v", err)
	}

	mgr := coresub.NewManager(stores.Subscriptions, 2, 16, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	router := func(lfdi string) *httptest.Server {
		cfg := assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond}
		h, _ := assembly.BuildProtocolRouter(cfg, stores, fixedIdentityPolicy(lfdi), testSFDI, testLFDI, mgr)
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}
	return subscribedResourceFleet{stores: stores, a: router(deviceLFDIA), b: router(deviceLFDIB), m: router(subMgrLFDI)}
}

// subscribe POSTs a Subscription to resource under edevID and returns the status.
func subscribe(t *testing.T, srv *httptest.Server, edevID, resource, notificationURI string) int {
	t.Helper()
	body := `<Subscription xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>` + xmlEscape(resource) +
		`</subscribedResource><encoding>0</encoding><level>+S1</level><limit>10</limit>` +
		`<notificationURI>` + xmlEscape(notificationURI) + `</notificationURI></Subscription>`
	resp, err := srv.Client().Post(srv.URL+"/edev/"+edevID+"/sub", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST sub: %v", err)
	}
	_ = resp.Body.Close() // only the status is read
	return resp.StatusCode
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// assertNothingFor fails if rcv received anything within a grace period that
// starts after another subscriber of the same change was already notified.
func assertNothingFor(t *testing.T, rcv *notificationReceiver, who string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	rcv.mu.Lock()
	defer rcv.mu.Unlock()
	if len(rcv.got) != 0 {
		t.Errorf("%s received %d notifications, want none: %+v", who, len(rcv.got), rcv.got)
	}
}

func storedFor(t *testing.T, stores *assembly.Stores, resource string) int {
	t.Helper()
	recs, err := stores.Subscriptions.ListByResource(context.Background(), resource)
	if err != nil {
		t.Fatalf("ListByResource: %v", err)
	}
	return len(recs)
}

func TestSubscribedResource_CrossDeviceSubscribeIsRefusedAndNotNotified(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcvA, rcvB := newNotificationReceiver(t), newNotificationReceiver(t)

	if got := subscribe(t, f.a, subDevAID, "/edev/5/frp", rcvA.srv.URL); got != http.StatusBadRequest {
		t.Fatalf("A subscribing to B's /edev/5/frp: status %d, want 400", got)
	}
	if got := subscribe(t, f.b, subDevBID, "/edev/5/frp", rcvB.srv.URL); got != http.StatusCreated {
		t.Fatalf("B subscribing to its own /edev/5/frp: status %d, want 201", got)
	}
	if n := storedFor(t, f.stores, "/edev/5/frp"); n != 1 {
		t.Fatalf("subscriptions stored for /edev/5/frp = %d, want only B's", n)
	}

	postWindowRequest(t, f.b, subDevBID)
	got := rcvB.waitFor(t, 1)
	if got[0].SubscribedResource != "/edev/5/frp" {
		t.Fatalf("B's notification = %+v, want one for /edev/5/frp", got[0])
	}
	assertNothingFor(t, rcvA, "A")
}

func TestSubscribedResource_OwnResourceIsAcceptedAndNotified(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcvA := newNotificationReceiver(t)

	if got := subscribe(t, f.a, subDevAID, "/edev/3/frp", rcvA.srv.URL); got != http.StatusCreated {
		t.Fatalf("A subscribing to its own /edev/3/frp: status %d, want 201", got)
	}
	postWindowRequest(t, f.a, subDevAID)
	got := rcvA.waitFor(t, 1)
	if got[0] != (receivedNotification{Resource: "/edev/3/frp", SubscribedResource: "/edev/3/frp", Status: sep2.NotificationStatusDefault}) {
		t.Errorf("A's notification = %+v, want a change notification on /edev/3/frp", got[0])
	}
}

func TestSubscribedResource_ManagerMaySubscribeToAManagedDeviceUntilRevoked(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcvM, rcvB := newNotificationReceiver(t), newNotificationReceiver(t)

	if got := subscribe(t, f.m, subMgrID, "/edev/5/frp", rcvM.srv.URL); got != http.StatusCreated {
		t.Fatalf("manager subscribing to managed /edev/5/frp: status %d, want 201", got)
	}
	if got := subscribe(t, f.b, subDevBID, "/edev/5/frp", rcvB.srv.URL); got != http.StatusCreated {
		t.Fatalf("B subscribing to its own /edev/5/frp: status %d, want 201", got)
	}
	postWindowRequest(t, f.b, subDevBID)
	if got := rcvM.waitFor(t, 1); got[0].SubscribedResource != "/edev/5/frp" {
		t.Fatalf("manager's notification = %+v, want one for /edev/5/frp", got[0])
	}
	rcvB.waitFor(t, 1)

	// Revoking management takes effect at delivery for a subscription
	// accepted while it held.
	managers := f.stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	if err := managers.Unassign(context.Background(), deviceLFDIB); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	postWindowRequest(t, f.b, subDevBID)
	rcvB.waitFor(t, 2)
	time.Sleep(300 * time.Millisecond)
	rcvM.mu.Lock()
	defer rcvM.mu.Unlock()
	if len(rcvM.got) != 1 {
		t.Errorf("manager received %d notifications, want only the one before revocation: %+v", len(rcvM.got), rcvM.got)
	}
}

func TestSubscribedResource_StoredCrossDeviceSubscriptionIsNotNotified(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcvA, rcvB := newNotificationReceiver(t), newNotificationReceiver(t)

	planted := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/sub/planted"}},
		SubscribedResource:   "/edev/5/frp",
		NotificationURI:      rcvA.srv.URL,
		Limit:                10,
	}
	if err := f.stores.Subscriptions.Create(context.Background(), "planted", planted); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if got := subscribe(t, f.b, subDevBID, "/edev/5/frp", rcvB.srv.URL); got != http.StatusCreated {
		t.Fatalf("B subscribing to its own /edev/5/frp: status %d, want 201", got)
	}
	if n := storedFor(t, f.stores, "/edev/5/frp"); n != 2 {
		t.Fatalf("subscriptions stored for /edev/5/frp = %d, want the planted one and B's", n)
	}

	postWindowRequest(t, f.b, subDevBID)
	rcvB.waitFor(t, 1)
	assertNothingFor(t, rcvA, "the stored cross-device subscription")
}

func TestSubscribedResource_NonCanonicalHrefIsRefused(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcvA := newNotificationReceiver(t)

	for _, resource := range []string{
		"/edev/3/../5/frp",
		"/edev/5/../3/frp",
		"https://host.example/edev/3/frp",
		"//host.example/edev/3/frp",
		"/edev/3/frp?s=0",
		"/edev/3/frp/1?s=0",
		"/edev/3/frp/1#x",
		"/edev/3/frp/%31",
		"/edev/3/./frp",
		"/edev/3/frp/",
		"edev/3/frp",
		"",
		"/tm",
		"/mup/3",
	} {
		if got := subscribe(t, f.a, subDevAID, resource, rcvA.srv.URL); got != http.StatusBadRequest {
			t.Errorf("subscribedResource %q: status %d, want 400", resource, got)
		}
	}
	if got := subscribe(t, f.a, subDevAID, "/edev/3/frp", rcvA.srv.URL); got != http.StatusCreated {
		t.Errorf("control: canonical /edev/3/frp: status %d, want 201", got)
	}
}
