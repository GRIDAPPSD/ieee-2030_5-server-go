package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestERR002RealRestartViaDiskPersistence covers the durable-persistence
// happy path (GRIDAPPSD/ieee-2030_5-server-go#224) equivalent of
// TestERR002RestartPreservesSubscriptions, but uses the durable JSON-file
// backend instead of the test-only
// snapshot/restore hooks. The simulated "restart" is now a fresh
// SubscriptionStore that reads its initial state from disk: the
// binary-restart shape Pike flagged as missing from Path A.
func TestERR002RealRestartViaDiskPersistence(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const subID = "sub-err002-persisted-1"
	const resourceHref = "/edev/1"
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: resourceHref,
		NotificationURI:    srv.URL + "/notify",
		Encoding:           sep2.EncodingXML,
	}

	path := filepath.Join(t.TempDir(), "subs.json")

	// --- Phase 1: original server (Store A + Manager A) writes the sub
	// to disk via the persistence path.
	storeA, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence (A): %v", err)
	}
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

	// --- Phase 2: simulate a real binary restart: tear down Manager A,
	// build a brand-new SubscriptionStore wired to the SAME path. No
	// in-memory hand-off: the new store reads its state from disk.
	cancelA()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager A did not shut down within 2s")
	}

	storeB, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence (B): %v", err)
	}
	// The rehydrated store must observe the subscription via primary
	// Get and via the secondary resource index: both are exercised by
	// the Notify call below.
	if _, err := storeB.Get(context.Background(), subID); err != nil {
		t.Fatalf("Get(%q) on rehydrated store B: %v", subID, err)
	}

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	mgrB := subscription.NewManager(storeB, 2, 10, loopbackReceivers)
	doneB := make(chan struct{})
	go func() {
		mgrB.Start(ctxB)
		close(doneB)
	}()

	// --- Phase 3: Notify on the new manager: the subscription survived
	// the binary restart and the callback must fire.
	mgrB.Notify(ctxB, resourceHref, sep2.NotificationStatusChanged)

	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("did not receive notification after disk-based restart; received = %d",
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

// TestERR002RealRestartDeletePersistsAcrossRestart pins the symmetric
// case: a Delete that lands before the "restart" must also survive. If
// we forgot to flush on Delete the rehydrated store would still hold
// the canceled subscription, and the harness would see a spurious
// notification.
func TestERR002RealRestartDeletePersistsAcrossRestart(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const subID = "sub-err002-deleted"
	const resourceHref = "/edev/2"
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/2/sub/1"},
		},
		SubscribedResource: resourceHref,
		NotificationURI:    srv.URL + "/notify",
		Encoding:           sep2.EncodingXML,
	}

	path := filepath.Join(t.TempDir(), "subs.json")

	storeA, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence (A): %v", err)
	}
	if err := storeA.Create(context.Background(), subID, sub); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := storeA.Delete(context.Background(), subID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Rehydrate from disk.
	storeB, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence (B): %v", err)
	}
	if _, err := storeB.Get(context.Background(), subID); err == nil {
		t.Errorf("deleted subscription %q survived restart", subID)
	}

	// Notify against the rehydrated store: receiver must NOT see a
	// callback for the deleted subscription.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := subscription.NewManager(storeB, 2, 10, loopbackReceivers)
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	mgr.Notify(ctx, resourceHref, sep2.NotificationStatusChanged)

	// Give the worker pool ~250ms to NOT deliver anything.
	time.Sleep(250 * time.Millisecond)
	if received.Load() != 0 {
		t.Errorf("receiver got %d notifications for deleted subscription; want 0",
			received.Load())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager did not shut down within 2s")
	}
}
