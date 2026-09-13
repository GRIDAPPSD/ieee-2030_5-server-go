package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestHandleDeleteSubscription_PrefixBoundaryRefused pins the comparison
// against a regression to a prefix or suffix match that omits the "/"
// separator: an id or subId that is a string prefix of the real one must
// still be refused.
func TestHandleDeleteSubscription_PrefixBoundaryRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		seedID     string
		seedHref   string
		pathEdevID string
		pathSubID  string
	}{
		{
			name:       "EndDevice id is a prefix of the owning device's id",
			seedID:     "sub-1",
			seedHref:   "/edev/10/sub/sub-1",
			pathEdevID: "1",
			pathSubID:  "sub-1",
		},
		{
			name:       "subId is a prefix of the stored subId",
			seedID:     "sub-1",
			seedHref:   "/edev/edev-A/sub/sub-10",
			pathEdevID: "edev-A",
			pathSubID:  "sub-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := memory.NewSubscriptionStore()
			sub := sep2.Subscription{
				SubscribableResource: sep2.SubscribableResource{
					Resource: sep2.Resource{Href: tc.seedHref},
				},
				NotificationURI: "http://subscriber.test/notify",
			}
			if err := s.Create(context.Background(), tc.seedID, sub); err != nil {
				t.Fatalf("seed: %v", err)
			}

			notifier := &recordingNotifier{}
			mux := http.NewServeMux()
			mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
				subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

			req := httptest.NewRequest(http.MethodDelete, "/edev/"+tc.pathEdevID+"/sub/"+tc.pathSubID, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
			}
			if got := len(notifier.snapshot()); got != 0 {
				t.Errorf("notifyRemoved was called %d times; want 0", got)
			}
			stillThere, err := s.Store.Get(context.Background(), tc.seedID)
			if err != nil {
				t.Fatalf("%s was removed from the store by a refused DELETE: %v", tc.seedID, err)
			}
			if stillThere.Href != tc.seedHref {
				t.Errorf("stored Href changed to %q, want unchanged %q", stillThere.Href, tc.seedHref)
			}
		})
	}
}

// TestHandleDeleteSubscription_CrossDeviceAndUnknownBodiesMatch pins the
// anti-probe property the handler comment states: a cross-device refusal
// must be indistinguishable from a refusal for a subId that does not exist
// at all, or the response itself discloses that the subId belongs to some
// other EndDevice.
func TestHandleDeleteSubscription_CrossDeviceAndUnknownBodiesMatch(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-A/sub/sub-1"},
		},
		NotificationURI: "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, nil))

	crossDevice := httptest.NewRecorder()
	mux.ServeHTTP(crossDevice, httptest.NewRequest(http.MethodDelete, "/edev/edev-B/sub/sub-1", nil))

	unknown := httptest.NewRecorder()
	mux.ServeHTTP(unknown, httptest.NewRequest(http.MethodDelete, "/edev/edev-B/sub/does-not-exist", nil))

	if crossDevice.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound {
		t.Fatalf("status codes = %d (cross-device), %d (unknown), want 404, 404", crossDevice.Code, unknown.Code)
	}
	if crossDevice.Body.String() != unknown.Body.String() {
		t.Errorf("cross-device body %q != unknown-subId body %q", crossDevice.Body.String(), unknown.Body.String())
	}
	if got, want := crossDevice.Header().Get("Content-Type"), unknown.Header().Get("Content-Type"); got != want {
		t.Errorf("cross-device Content-Type %q != unknown-subId Content-Type %q", got, want)
	}
}

// TestHandleDeleteSubscription_CrossDeviceRefusalIsLogged proves the
// refusal is visible in the log (issue #435 review), naming the path
// EndDevice id and subId, and not the other device's id or href. Not
// parallel: captureLog swaps the process-wide logger.
func TestHandleDeleteSubscription_CrossDeviceRefusalIsLogged(t *testing.T) {
	logs := captureLog(t)

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-A/sub/sub-1"},
		},
		NotificationURI: "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, nil))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-B/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	line := logs.String()
	if !strings.Contains(line, "edev-B") || !strings.Contains(line, "sub-1") {
		t.Errorf("log = %q, want it to name the path EndDevice id %q and subId %q", line, "edev-B", "sub-1")
	}
	if strings.Contains(line, "edev-A") {
		t.Errorf("log = %q, leaks the owning EndDevice id edev-A", line)
	}
}
