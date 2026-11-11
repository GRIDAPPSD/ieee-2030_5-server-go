package subscription

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func rejectingReceiver(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedForDelivery(t *testing.T, uri string) (*memory.SubscriptionStore, sep2.Subscription) {
	t.Helper()
	s := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/1/sub/s1"}},
		SubscribedResource:   "/edev/1",
		NotificationURI:      uri,
	}
	if err := s.Create(context.Background(), "s1", sub); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, sub
}

// A receiver's 4xx ends a subscription only while that subscription still
// points at that receiver. A notification queued before a PUT carries the old
// URI, and the old receiver's refusal must not delete the updated record.
func TestDeliver4xxFromReplacedReceiverKeepsTheUpdatedSubscription(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldRecv := rejectingReceiver(t)
	s, sub := seedForDelivery(t, oldRecv.URL+"/notify")
	m := NewManager(s, 1, 1, loopbackReceivers)
	task := notificationTask{subscriptionID: "s1", notificationURI: oldRecv.URL + "/notify", payload: []byte("<Notification/>")}

	moved := sub
	moved.NotificationURI = "https://new.example.test/notify"
	if err := s.Update(ctx, "s1", moved); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := m.deliver(ctx, task); err == nil {
		t.Fatal("deliver returned nil for a 4xx")
	}
	got, err := s.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("the updated subscription was deleted by the old receiver's 4xx: %v", err)
	}
	if got.NotificationURI != moved.NotificationURI {
		t.Errorf("NotificationURI = %q, want the updated %q", got.NotificationURI, moved.NotificationURI)
	}
}

// Control for the test above: the same 4xx with no PUT still deletes.
func TestDeliver4xxFromCurrentReceiverStillDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recv := rejectingReceiver(t)
	s, _ := seedForDelivery(t, recv.URL+"/notify")
	m := NewManager(s, 1, 1, loopbackReceivers)
	task := notificationTask{subscriptionID: "s1", notificationURI: recv.URL + "/notify", payload: []byte("<Notification/>")}

	if err := m.deliver(ctx, task); err == nil {
		t.Fatal("deliver returned nil for a 4xx")
	}
	if _, err := s.Get(ctx, "s1"); err == nil {
		t.Error("subscription survived a 4xx from its own receiver")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
}

// The worker counts a 4xx as a client error even when the subscription was
// kept because it now names another receiver.
func TestWorkerCountsClientErrorWhenSubscriptionKept(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldRecv := rejectingReceiver(t)
	s, sub := seedForDelivery(t, oldRecv.URL+"/notify")
	m := NewManager(s, 1, 1, loopbackReceivers)
	outcomes := make(chan string, 4)
	m.SetObserver(func(o string) { outcomes <- o })

	moved := sub
	moved.NotificationURI = "https://new.example.test/notify"
	if err := s.Update(ctx, "s1", moved); err != nil {
		t.Fatalf("Update: %v", err)
	}
	done := make(chan struct{})
	go func() { m.Start(ctx); close(done) }()
	task := notificationTask{subscriptionID: "s1", notificationURI: oldRecv.URL + "/notify", payload: []byte("<Notification/>")}
	if err := m.enqueue(task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case got := <-outcomes:
		if got != "client_error" {
			t.Errorf("outcome = %q, want client_error", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no outcome observed for a 4xx from a replaced receiver")
	}
	cancel()
	<-done
}
