package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A distinct response for a name that fails to resolve would tell a client
// whether an internal name resolves, so both outcomes answer identically.
func TestCreateSubscriptionUnresolvedAndRefusedResponsesMatch(t *testing.T) {
	t.Parallel()

	sink := newRecordingServer(t, nil)
	fn := standardNet(sink, sink)
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn)
	h := subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI)

	refused := postSubscription(t, h, "1", "/edev/1/fsa", destURI("loopback.test"))
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("policy refusal status = %d, want 400", refused.Code)
	}
	for _, host := range []string{"nxdomain.test", "timeout.test", "empty.test"} {
		rec := postSubscription(t, h, "1", "/edev/1/fsa", destURI(host))
		if rec.Code != refused.Code || rec.Body.String() != refused.Body.String() {
			t.Errorf("%s: response %d %q, want the refusal response %d %q",
				host, rec.Code, rec.Body.String(), refused.Code, refused.Body.String())
		}
		if !reflect.DeepEqual(rec.Header(), refused.Header()) {
			t.Errorf("%s: headers %v, want the refusal headers %v", host, rec.Header(), refused.Header())
		}
	}
	assertNothingStored(t, store, "1", "/edev/1/fsa")
}

// Not parallel: it swaps the process-wide log output.
//
// Every unresolvable outcome answers exactly like a policy refusal, and only
// the server log says which one happened.
func TestCreationLogNamesTheResolutionOutcome(t *testing.T) {
	logs := captureLog(t)

	sink := newRecordingServer(t, nil)
	fn := standardNet(sink, sink)
	fn.hangLookupOn("hang.test")
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn)
	subscription.SetCreationResolveTimeout(mgr, 200*time.Millisecond)
	h := subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI)

	refused := postSubscription(t, h, "refused", "/edev/1/fsa", destURI("loopback.test"))
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("policy refusal status = %d, want 400", refused.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newSubscriptionRequest(t, ctx, "canceled", "/edev/1/fsa", destURI("hang.test"))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveSubscription(h, req) }()
	select {
	case <-fn.lookupEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled request never reached the lookup")
	}
	cancel()
	var canceled *httptest.ResponseRecorder
	select {
	case canceled = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled request did not finish")
	}

	responses := map[string]*httptest.ResponseRecorder{
		"canceled":      canceled,
		"lookuptimeout": postSubscriptionWithin(t, h, "lookuptimeout", "/edev/1/fsa", destURI("hang.test"), 5*time.Second),
		"nxdomain":      postSubscription(t, h, "nxdomain", "/edev/1/fsa", destURI("nxdomain.test")),
		"resolverfail":  postSubscription(t, h, "resolverfail", "/edev/1/fsa", destURI("timeout.test")),
	}
	for name, rec := range responses {
		if rec.Code != refused.Code || rec.Body.String() != refused.Body.String() {
			t.Errorf("%s: response %d %q, want the refusal response %d %q",
				name, rec.Code, rec.Body.String(), refused.Code, refused.Body.String())
		}
	}
	assertNothingStored(t, store, "canceled", "/edev/1/fsa")

	got := logs.String()
	line := func(edev string) string {
		for _, l := range strings.Split(got, "\n") {
			if strings.Contains(l, `EndDevice "`+edev+`"`) {
				return l
			}
		}
		return ""
	}
	for edev, want := range map[string]string{
		"refused":       `subscription: refused notificationURI "` + destURI("loopback.test") + `"`,
		"canceled":      `subscription: request ended while resolving notificationURI "` + destURI("hang.test") + `"`,
		"lookuptimeout": `subscription: could not resolve notificationURI "` + destURI("hang.test") + `"`,
		"nxdomain":      `subscription: no such host for notificationURI "` + destURI("nxdomain.test") + `"`,
		"resolverfail":  `subscription: could not resolve notificationURI "` + destURI("timeout.test") + `"`,
	} {
		if l := line(edev); !strings.Contains(l, want) {
			t.Errorf("EndDevice %q: log line %q, want it to contain %q", edev, l, want)
		}
	}
}

// A blackholed first address must not consume the whole delivery timeout
// and starve an address that would answer.
func TestDeliverySplitsConnectBudgetAcrossAddresses(t *testing.T) {
	t.Parallel()

	const deliveryTimeout = 4 * time.Second
	receiver := newRecordingServer(t, nil)
	fn := newFakeNet()
	fn.setHost("multi.test", "192.0.2.50", "192.0.2.51")
	fn.hangOn("192.0.2.50", destPort)
	fn.route("192.0.2.51", destPort, receiver)
	store := memory.NewSubscriptionStore()
	seedStored(t, store, "multi", "1", "/edev/1/fsa", destURI("multi.test"))
	mgr := newSeamedManager(t, store, fn)
	subscription.SetDeliveryTimeout(mgr, deliveryTimeout)
	runManager(t, mgr)

	start := time.Now()
	mgr.Notify(context.Background(), "/edev/1/fsa", sep2.NotificationStatusChanged)
	waitUntil(t, "delivery through the second address", func() bool { return len(receiver.received()) == 1 })

	if elapsed := time.Since(start); elapsed >= deliveryTimeout {
		t.Errorf("delivered after %v, want within the %v delivery timeout", elapsed, deliveryTimeout)
	}
	if got, want := fn.dials(), []string{"192.0.2.50:" + destPort, "192.0.2.51:" + destPort}; !reflect.DeepEqual(got, want) {
		t.Errorf("dialed %v, want %v", got, want)
	}
	if b, ok := fn.budget("192.0.2.50:" + destPort); !ok || b > deliveryTimeout/2+250*time.Millisecond {
		t.Errorf("first address connect budget = %v (deadline set: %v), want at most half of %v", b, ok, deliveryTimeout)
	}
}
