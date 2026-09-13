package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestERR002RestartPreservesSubscriptions exercises the CSIP V1.2 ERR-002
// happy path via Path A's test-only snapshot/restore hooks: a subscription
// is created against an initial server (Manager A + Store A), the harness
// simulates a power-reset by snapshotting Store A, cancelling Manager A,
// constructing a fresh Manager B around a fresh Store B that has been
// restored from the snapshot, and finally triggering Notify on the new
// manager. The subscriber callback must receive the notification, proving
// the subscription survived the restart.
func TestERR002RestartPreservesSubscriptions(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const subID = "sub-err002-1"
	const resourceHref = "/edev/1"
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: resourceHref,
		NotificationURI:    srv.URL + "/notify",
		Encoding:           sep2.EncodingXML,
	}

	// --- Phase 1: original server (Manager A + Store A) accepts the sub.
	storeA := memory.NewSubscriptionStore()
	ctxA, cancelA := context.WithCancel(context.Background())
	if err := storeA.Create(ctxA, subID, sub); err != nil {
		cancelA()
		t.Fatalf("Create on Store A: %v", err)
	}

	mgrA := subscription.NewManager(storeA, 2, 10, loopbackReceivers)
	doneA := make(chan struct{})
	go func() {
		mgrA.Start(ctxA)
		close(doneA)
	}()

	// Capture the in-memory state before "restart".
	snapshot := storeA.SnapshotForTesting()
	if len(snapshot) != 1 || snapshot[0].ID != subID {
		t.Fatalf("snapshot before restart = %+v, want one record with ID %q",
			snapshot, subID)
	}

	// --- Phase 2: simulate restart — cancel Manager A, build fresh state.
	cancelA()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager A did not shut down within 2s")
	}

	storeB := memory.NewSubscriptionStore()
	storeB.RestoreForTesting(snapshot)

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	mgrB := subscription.NewManager(storeB, 2, 10, loopbackReceivers)
	doneB := make(chan struct{})
	go func() {
		mgrB.Start(ctxB)
		close(doneB)
	}()

	// --- Phase 3: Notify on the new manager and verify delivery.
	mgrB.Notify(ctxB, resourceHref, sep2.NotificationStatusChanged)

	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("did not receive notification after restart; received = %d",
				received.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancelB()
	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager B did not shut down within 2s")
	}
}

// TestERR002RestartReceiverReturns400 covers the second leg of ERR-002:
// after a simulated restart the server emits a Notification whose receiver
// rejects with HTTP 400. Per CSIP V1.2 ERR-002, a 4xx response from the
// receiver indicates the subscription should be considered terminated and
// the server MUST delete it from its store rather than retrying.
//
// This test pins the delete-on-4xx behavior. The companion
// TestERR002RestartReceiverReturns503 pins the 5xx leave-in-place behavior
// (5xx is transient; deletion would be data loss).
func TestERR002RestartReceiverReturns400(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	var rejected atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		rejected.Add(1)
		http.Error(w, "invalid notification body", http.StatusBadRequest)
	}))
	defer srv.Close()

	const subID = "sub-err002-2"
	const resourceHref = "/edev/2"
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/2/sub/1"},
		},
		SubscribedResource: resourceHref,
		NotificationURI:    srv.URL + "/notify",
		Encoding:           sep2.EncodingXML,
	}

	// Snapshot/restore round-trip across a fresh store.
	storeA := memory.NewSubscriptionStore()
	if err := storeA.Create(context.Background(), subID, sub); err != nil {
		t.Fatalf("Create on Store A: %v", err)
	}
	snapshot := storeA.SnapshotForTesting()

	storeB := memory.NewSubscriptionStore()
	storeB.RestoreForTesting(snapshot)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := subscription.NewManager(storeB, 2, 10, loopbackReceivers)
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	mgr.Notify(ctx, resourceHref, sep2.NotificationStatusChanged)

	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("receiver never observed the notification; received = %d",
				received.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if rejected.Load() == 0 {
		t.Fatalf("receiver did not return 400; rejected = %d", rejected.Load())
	}

	// Manager must delete the subscription after the 4xx response. The
	// Delete is dispatched from the worker goroutine asynchronously after
	// the POST returns, so poll briefly rather than asserting on the same
	// instant the receiver sees the call.
	deadline = time.After(2 * time.Second)
	for {
		if _, err := storeB.Get(ctx, subID); err != nil {
			break // gone — desired
		}
		select {
		case <-deadline:
			t.Fatalf("subscription %q still present after 4xx response; want delete-on-4xx",
				subID)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager did not shut down within 2s")
	}
}

// TestERR002RestartReceiverReturns503 pins the complementary half of the
// ERR-002 second-leg rule: a 5xx response from the receiver is transient.
// The Manager MUST NOT delete the subscription on 5xx — deletion would be
// data loss on what is by spec a recoverable upstream failure. Retry /
// back-off on 5xx is a separate ticket; this test only asserts that the
// subscription survives the failed delivery.
func TestERR002RestartReceiverReturns503(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		http.Error(w, "receiver overloaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	const subID = "sub-err002-3"
	const resourceHref = "/edev/3"
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/3/sub/1"},
		},
		SubscribedResource: resourceHref,
		NotificationURI:    srv.URL + "/notify",
		Encoding:           sep2.EncodingXML,
	}

	store := memory.NewSubscriptionStore()
	if err := store.Create(context.Background(), subID, sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := subscription.NewManager(store, 2, 10, loopbackReceivers)
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	mgr.Notify(ctx, resourceHref, sep2.NotificationStatusChanged)

	// Wait for the receiver to observe the failed POST.
	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("receiver never observed the notification; received = %d",
				received.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Give the worker a small grace window to (incorrectly) delete the
	// subscription if delete-on-5xx were misapplied. 100ms is well above
	// the dispatch overhead of the worker but short enough to keep the
	// test snappy.
	time.Sleep(100 * time.Millisecond)
	if _, err := store.Get(ctx, subID); err != nil {
		t.Errorf("subscription %q removed after 5xx response: %v (5xx must leave it in place)",
			subID, err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager did not shut down within 2s")
	}
}
