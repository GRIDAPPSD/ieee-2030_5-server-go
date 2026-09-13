package subscription_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

func closedSub(href string) sep2.Subscription {
	return sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: href},
		},
		SubscribedResource: "/edev/1",
		NotificationURI:    "http://127.0.0.1:1/never",
	}
}

// TestNotifyRemovedAfterShutdownDoesNotPanic reproduces #460: closing the
// queue at shutdown, then calling NotifyRemoved, must not panic, and the
// caller sees ErrManagerClosed rather than a generic error.
func TestNotifyRemovedAfterShutdownDoesNotPanic(t *testing.T) {
	mgr := subscription.NewManager(&staticLister{}, 1, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down in time")
	}

	err := mgr.NotifyRemoved(ctx, closedSub("/edev/1/sub/1"))
	if err == nil {
		t.Fatal("NotifyRemoved after shutdown returned nil, want ErrManagerClosed")
	}
	if !errors.Is(err, subscription.ErrManagerClosed) {
		t.Fatalf("NotifyRemoved after shutdown: err = %v, want errors.Is(err, ErrManagerClosed)", err)
	}
}

// TestNotifyAfterShutdownDoesNotPanic covers the second enqueue path
// (resource-change fan-out) reported alongside NotifyRemoved in #460.
func TestNotifyAfterShutdownDoesNotPanic(t *testing.T) {
	logs := captureLog(t)

	store := &mockSubStore{subs: []sep2.Subscription{closedSub("/edev/1/sub/1")}}
	mgr := subscription.NewManager(store, 1, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down in time")
	}

	// Must not panic.
	mgr.Notify(ctx, "/edev/1", sep2.NotificationStatusChanged)

	if !strings.Contains(logs.String(), "notification: manager closed, dropping for") {
		t.Errorf("Notify after shutdown did not log the closed-manager drop; log:\n%s", logs.String())
	}
}

// TestManagerCloseIdempotent asserts a second Close does not panic (a double
// close(m.queue) would).
func TestManagerCloseIdempotent(t *testing.T) {
	mgr := subscription.NewManager(&staticLister{}, 1, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down in time")
	}

	// Start already closed the manager via ctx.Done(); a direct second
	// Close (as e.g. a test double or a defensive shutdown path might
	// call) must be a no-op, not a panic.
	mgr.Close()
}

// TestManagerCloseRacesEnqueue is the concurrency property from the issue:
// concurrent NotifyRemoved and Notify calls racing against Close must never
// panic, and every call after the observed close must return
// ErrManagerClosed. Run with -race.
func TestManagerCloseRacesEnqueue(t *testing.T) {
	store := &mockSubStore{subs: []sep2.Subscription{closedSub("/edev/1/sub/1")}}
	mgr := subscription.NewManager(store, 2, 4, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mgr.Start(ctx)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.NotifyRemoved(ctx, closedSub("/edev/1/sub/1"))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				mgr.Notify(ctx, "/edev/1", sep2.NotificationStatusChanged)
			}
		}
	}()

	time.Sleep(5 * time.Millisecond)
	cancel() // triggers Start's Close via ctx.Done(), concurrently with the loops above
	time.Sleep(5 * time.Millisecond)
	close(stop)
	wg.Wait()

	// The manager is now closed (cancel already fired); confirm the
	// post-close contract holds after the race settles.
	if err := mgr.NotifyRemoved(context.Background(), closedSub("/edev/1/sub/1")); !errors.Is(err, subscription.ErrManagerClosed) {
		t.Fatalf("NotifyRemoved after the race settled: err = %v, want ErrManagerClosed", err)
	}
}
