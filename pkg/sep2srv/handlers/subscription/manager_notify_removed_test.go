package subscription_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Per CSIP V1.2 section 11.6 (GRIDAPPSD/ieee-2030_5-server-go#169): final
// Removed-Notification on subscription delete. The Manager exposes
// NotifyRemoved as a per-subscriber dispatch
// (the existing Notify(href) broadcasts by SubscribedResource, which would
// hit every co-tenant subscriber on the same resource: wrong semantics
// for "this one subscription was deleted").

// TestManagerNotifyRemovedPostsToSubscriber verifies that NotifyRemoved
// dispatches a Notification with Status = NotificationStatusRemoved (3)
// to exactly the subscription's NotificationURI, carrying both the
// SubscribedResource and the Subscription.Href in the SubscriptionURI
// field.
func TestManagerNotifyRemovedPostsToSubscriber(t *testing.T) {
	t.Parallel()

	type captured struct {
		path         string
		contentType  string
		notification sep2.Notification
	}
	gotCh := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var n sep2.Notification
		_ = xml.Unmarshal(body, &n)
		gotCh <- captured{
			path:         r.URL.Path,
			contentType:  r.Header.Get("Content-Type"),
			notification: n,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/edev-1/sub/sub-1"},
		},
		SubscribedResource: "/edev/edev-1",
		NotificationURI:    srv.URL + "/notify-target",
	}

	mgr := subscription.NewManager(&staticLister{}, 1, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	if err := mgr.NotifyRemoved(ctx, sub); err != nil {
		t.Fatalf("NotifyRemoved returned error: %v", err)
	}

	select {
	case got := <-gotCh:
		if got.path != "/notify-target" {
			t.Errorf("path = %q, want /notify-target", got.path)
		}
		if got.contentType != "application/sep+xml" {
			t.Errorf("Content-Type = %q, want application/sep+xml", got.contentType)
		}
		if got.notification.Status != sep2.NotificationStatusRemoved {
			t.Errorf("status = %d, want %d (Removed)",
				got.notification.Status, sep2.NotificationStatusRemoved)
		}
		if got.notification.SubscribedResource != sub.SubscribedResource {
			t.Errorf("subscribedResource = %q, want %q",
				got.notification.SubscribedResource, sub.SubscribedResource)
		}
		if got.notification.SubscriptionURI != sub.Href {
			t.Errorf("subscriptionURI = %q, want %q",
				got.notification.SubscriptionURI, sub.Href)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Removed notification POST")
	}
}

// TestManagerNotifyRemovedQueueFullReturnsError verifies that a full
// queue causes NotifyRemoved to return a non-nil error rather than block.
// The caller (HandleDeleteSubscription) treats this as best-effort and
// logs without failing the DELETE: see handler tests.
func TestManagerNotifyRemovedQueueFullReturnsError(t *testing.T) {
	t.Parallel()

	// No worker started, so the queue cannot drain, so the second enqueue
	// (queue size 1) must report full.
	mgr := subscription.NewManager(&staticLister{}, 0, 1)
	// Intentionally do NOT call Start. Queue size 1; fill it once, then
	// the next enqueue must return ErrQueueFull-ish error.

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: "/edev/1",
		NotificationURI:    "http://127.0.0.1:1/never",
	}

	ctx := context.Background()
	if err := mgr.NotifyRemoved(ctx, sub); err != nil {
		t.Fatalf("first NotifyRemoved unexpectedly returned %v; queue should have space", err)
	}
	if err := mgr.NotifyRemoved(ctx, sub); err == nil {
		t.Fatal("second NotifyRemoved returned nil; want queue-full error")
	}
}

// TestManagerNotifyRemovedInvalidURI verifies that an obviously bad URI
// (empty NotificationURI) is rejected synchronously with an error rather
// than enqueueing a doomed task. Marshal errors are difficult to provoke
// on the fixed-shape Notification, so URL validation stands in.
func TestManagerNotifyRemovedInvalidURI(t *testing.T) {
	t.Parallel()

	mgr := subscription.NewManager(&staticLister{}, 1, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: "/edev/1",
		NotificationURI:    "", // invalid: no transport target
	}

	if err := mgr.NotifyRemoved(ctx, sub); err == nil {
		t.Fatal("NotifyRemoved returned nil for empty URI; want error")
	}
}

// TestManagerNotifyRemovedHonorsBackgroundDelivery verifies that a
// receiver returning 500 still drains via the worker without blocking
// the caller. The caller's contract is "enqueued for delivery"; the
// worker's log line is the only signal of failure.
func TestManagerNotifyRemovedHonorsBackgroundDelivery(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: "/edev/1",
		NotificationURI:    srv.URL,
	}

	mgr := subscription.NewManager(&staticLister{}, 1, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	if err := mgr.NotifyRemoved(ctx, sub); err != nil {
		t.Fatalf("NotifyRemoved returned error on enqueue: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for hits.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("worker never POSTed to the 500-returning receiver")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// staticLister is a no-op SubscriptionLister; NotifyRemoved doesn't
// touch the store (it has the Subscription handed to it).
type staticLister struct{}

func (staticLister) ListByResource(context.Context, string) ([]memory.SubscriptionRecord, error) {
	return nil, nil
}
