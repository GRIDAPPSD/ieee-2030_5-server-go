package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

type mockSubStore struct {
	subs []sep2.Subscription
}

func (m *mockSubStore) ListByResource(_ context.Context, href string) ([]sep2.Subscription, error) {
	var result []sep2.Subscription
	for _, s := range m.subs {
		if s.SubscribedResource == href {
			result = append(result, s)
		}
	}
	return result, nil
}

func TestManagerNotifiesSubscribers(t *testing.T) {
	var received atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &mockSubStore{
		subs: []sep2.Subscription{
			{
				SubscribableResource: sep2.SubscribableResource{
					Resource: sep2.Resource{Href: "/edev/1/sub/1"},
				},
				SubscribedResource: "/edev/1",
				NotificationURI:    srv.URL + "/notify",
			},
			{
				SubscribableResource: sep2.SubscribableResource{
					Resource: sep2.Resource{Href: "/edev/1/sub/2"},
				},
				SubscribedResource: "/edev/1",
				NotificationURI:    srv.URL + "/notify2",
			},
		},
	}

	mgr := subscription.NewManager(store, 2, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go mgr.Start(ctx)

	mgr.Notify(ctx, "/edev/1", sep2.NotificationStatusChanged)

	// Wait for delivery
	deadline := time.After(2 * time.Second)
	for {
		if received.Load() >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for notifications, received %d", received.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
}

func TestManagerNoSubscribersNoop(t *testing.T) {
	store := &mockSubStore{}
	mgr := subscription.NewManager(store, 1, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go mgr.Start(ctx)

	// Should not panic or block
	mgr.Notify(ctx, "/edev/999", sep2.NotificationStatusChanged)

	time.Sleep(50 * time.Millisecond)
	cancel()
}

func TestManagerGracefulShutdown(t *testing.T) {
	store := &mockSubStore{}
	mgr := subscription.NewManager(store, 2, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// OK, clean shutdown
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down in time")
	}
}
