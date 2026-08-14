package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Per CSIP V1.2 section 11.6 (GRIDAPPSD/ieee-2030_5-server-go#169):
// DELETE /edev/{id}/sub/{subId} fires a final Removed Notification at
// the just-deleted subscriber before the 204 is returned. Spec doesn't
// require it; correct behavior unblocks MAINT-006 strengthening.

// recordingNotifier is the test double for the notifyRemoved callback.
// It records every call so the test can assert which subscription was notified.
type recordingNotifier struct {
	mu     sync.Mutex
	calls  []sep2.Subscription
	failOn map[string]error // map subID to error to return; nil = success
}

func (r *recordingNotifier) NotifyRemoved(_ context.Context, sub sep2.Subscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sub)
	if err, ok := r.failOn[sub.Href]; ok {
		return err
	}
	return nil
}

func (r *recordingNotifier) snapshot() []sep2.Subscription {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sep2.Subscription, len(r.calls))
	copy(out, r.calls)
	return out
}

// TestHandleDeleteSubscription_NotifiesBeforeDelete verifies that on a
// successful DELETE, the handler calls notifyRemoved with the exact
// Subscription record (Href, NotificationURI, SubscribedResource) that
// the store had pre-delete.
func TestHandleDeleteSubscription_NotifiesBeforeDelete(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-1/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-1",
		NotificationURI:    "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	notifier := &recordingNotifier{}

	mux := http.NewServeMux()
	// Option B: pass method value, not interface.
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("NotifyRemoved call count = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.Href != sub.Href {
		t.Errorf("notified sub.Href = %q, want %q", got.Href, sub.Href)
	}
	if got.NotificationURI != sub.NotificationURI {
		t.Errorf("notified sub.NotificationURI = %q, want %q",
			got.NotificationURI, sub.NotificationURI)
	}
	if got.SubscribedResource != sub.SubscribedResource {
		t.Errorf("notified sub.SubscribedResource = %q, want %q",
			got.SubscribedResource, sub.SubscribedResource)
	}

	// And the underlying store no longer has it.
	if _, err := s.Store.Get(context.Background(), "sub-1"); err == nil {
		t.Error("sub still present in store after DELETE")
	}
}

// TestHandleDeleteSubscription_NotifyFailureDoesNotFailDelete verifies
// that a notifyRemoved callback returning an error does NOT change the
// 204 status: the final Notification is best-effort by design.
func TestHandleDeleteSubscription_NotifyFailureDoesNotFailDelete(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-1/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-1",
		NotificationURI:    "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	notifier := &recordingNotifier{
		failOn: map[string]error{
			sub.Href: errNotifyBoom,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204 even with notify failure; body=%s",
			rec.Code, rec.Body.String())
	}

	if len(notifier.snapshot()) != 1 {
		t.Errorf("notifier call count = %d, want 1", len(notifier.snapshot()))
	}
}

// TestHandleDeleteSubscription_NilNotifierAllowed verifies the handler
// remains backward-compatible: nil notifyRemoved means "skip the final
// Notification". Required so server.go can wire nil during tests that
// don't exercise the subscription pipeline.
func TestHandleDeleteSubscription_NilNotifierAllowed(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-1/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-1",
		NotificationURI:    "http://subscriber.test/notify",
	}
	if err := s.Create(context.Background(), "sub-1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, nil))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleDeleteSubscription_NotFoundDoesNotNotify verifies that a
// DELETE against an unknown subId returns 404 and does NOT call
// notifyRemoved (no subscriber existed to notify).
func TestHandleDeleteSubscription_NotFoundDoesNotNotify(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	notifier := &recordingNotifier{}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/missing", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := len(notifier.snapshot()); got != 0 {
		t.Errorf("notifyRemoved was called %d times for unknown sub; want 0", got)
	}
}

// TestHandleDeleteSubscription_MethodNotAllowed verifies that a non-DELETE
// method on the registered route returns 405 and does not touch the
// store or notifier.
func TestHandleDeleteSubscription_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	s := memory.NewSubscriptionStore()
	notifier := &recordingNotifier{}

	h := subscription.HandleDeleteSubscription(s, notifier.NotifyRemoved)

	req := httptest.NewRequest(http.MethodGet, "/edev/edev-1/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if got := len(notifier.snapshot()); got != 0 {
		t.Errorf("notifier called %d times on 405 path; want 0", got)
	}
}

// TestHandleDeleteSubscription_Integration uses a real *subscription.Manager
// to confirm the wire-up: receiver server captures the Removed Notification
// and asserts Status=3 and the subscription Href.
//
// This is the end-to-end shape MAINT-006 strengthening would call from.
func TestHandleDeleteSubscription_Integration(t *testing.T) {
	t.Parallel()

	var receivedCount atomic.Int32
	gotBody := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body)
		select {
		case gotBody <- body:
		default:
		}
		receivedCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-int/sub/sub-int"},
		},
		SubscribedResource: "/edev/edev-int",
		NotificationURI:    receiver.URL,
	}
	if err := s.Create(context.Background(), "sub-int", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mgr := newRealManager(t, s)

	mux := http.NewServeMux()
	// Option B: pass the manager's NotifyRemoved method value directly.
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, mgr.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-int/sub/sub-int", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}

	select {
	case body := <-gotBody:
		n, err := unmarshalNotification(body)
		if err != nil {
			t.Fatalf("unmarshal notification: %v\nbody=%s", err, string(body))
		}
		if n.Status != sep2.NotificationStatusRemoved {
			t.Errorf("Notification.Status = %d, want %d (Removed)",
				n.Status, sep2.NotificationStatusRemoved)
		}
		if n.SubscriptionURI != sub.Href {
			t.Errorf("Notification.SubscriptionURI = %q, want %q",
				n.SubscriptionURI, sub.Href)
		}
	case <-timeoutAfter(2):
		t.Fatalf("no Notification received within 2s (count=%d)", receivedCount.Load())
	}
}
