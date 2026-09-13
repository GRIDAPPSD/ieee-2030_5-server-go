package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// GRIDAPPSD/ieee-2030_5-server-go#435: HandleDeleteSubscription looked up
// {subId} alone, so any caller who owns (or manages) SOME EndDevice could
// delete a subscription created under a DIFFERENT EndDevice, as long as
// they guessed its subId. The ownership gate validates the caller against
// path {id}; it never sees {subId}, so nothing upstream caught this.

// TestHandleDeleteSubscription_CrossDeviceSubIdNotFound proves the
// acceptance criterion: a DELETE whose {subId} was created under a
// different EndDevice than the path {id} answers 404 and leaves that
// subscription stored.
func TestHandleDeleteSubscription_CrossDeviceSubIdNotFound(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-A/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-A",
		NotificationURI:    "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	notifier := &recordingNotifier{}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

	// edev-B did not create sub-1; edev-A did.
	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-B/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := len(notifier.snapshot()); got != 0 {
		t.Errorf("notifyRemoved was called %d times for a cross-device subId; want 0", got)
	}

	// The subscription must still be in the store: the delete must not
	// have gone through.
	stillThere, err := s.Store.Get(context.Background(), "sub-1")
	if err != nil {
		t.Fatalf("sub-1 was removed from the store by a cross-device DELETE: %v", err)
	}
	if stillThere.Href != sub.Href {
		t.Errorf("sub-1 in store has Href = %q, want unchanged %q", stillThere.Href, sub.Href)
	}
}

// TestHandleDeleteSubscription_SameDeviceStillSucceeds proves the second
// acceptance criterion: a DELETE whose path {id} matches the EndDevice the
// subscription was created under still answers 204 and fires the final
// Removed notification. The ownership gate (assembly.ownershipGate) is what
// distinguishes the EndDevice's owner from its provisioned manager; both
// are let through to this handler with the same path {id}, so one test at
// this layer covers both callers.
func TestHandleDeleteSubscription_SameDeviceStillSucceeds(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-A/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-A",
		NotificationURI:    "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	notifier := &recordingNotifier{}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-A/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("NotifyRemoved call count = %d, want 1", len(calls))
	}
	if calls[0].Href != sub.Href {
		t.Errorf("notified sub.Href = %q, want %q", calls[0].Href, sub.Href)
	}

	if _, err := s.Store.Get(context.Background(), "sub-1"); err == nil {
		t.Error("sub-1 still present in store after a same-device DELETE")
	}
}
