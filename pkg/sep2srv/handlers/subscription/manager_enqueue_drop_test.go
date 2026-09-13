package subscription_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// outcomeRecorder is a concurrency-safe SetObserver callback target.
type outcomeRecorder struct {
	mu     sync.Mutex
	counts map[string]int
}

func (r *outcomeRecorder) record(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[outcome]++
}

func (r *outcomeRecorder) count(outcome string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[outcome]
}

func startManager(t *testing.T, mgr *subscription.Manager) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	return cancel, done
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not happen within 2s", what)
	}
}

// Every enqueue the manager refuses is a dropped notification, so each one
// reaches the observer: the closed-manager refusals on both enqueue paths,
// and a queue-full refusal on NotifyRemoved.
//
// Not parallel: it swaps the process-wide log output (captureLog).
func TestEnqueueDropsReachObserver(t *testing.T) {
	t.Run("closed manager on Notify and NotifyRemoved", func(t *testing.T) {
		captureLog(t)
		store := &mockSubStore{subs: []sep2.Subscription{
			closedSub("/edev/1/sub/1"), closedSub("/edev/1/sub/2"), closedSub("/edev/1/sub/3"),
		}}
		mgr := subscription.NewManager(store, 1, 4, loopbackReceivers)
		rec := &outcomeRecorder{}
		mgr.SetObserver(rec.record)
		cancel, done := startManager(t, mgr)
		cancel()
		waitClosed(t, done, "manager shutdown")

		mgr.Notify(context.Background(), "/edev/1", sep2.NotificationStatusChanged)
		if got := rec.count("manager_closed"); got != 3 {
			t.Errorf("after Notify to 3 subscribers: manager_closed = %d, want 3", got)
		}
		_ = mgr.NotifyRemoved(context.Background(), closedSub("/edev/1/sub/4"))
		if got := rec.count("manager_closed"); got != 4 {
			t.Errorf("after NotifyRemoved: manager_closed = %d, want 4", got)
		}
		if got := rec.count("queue_full"); got != 0 {
			t.Errorf("queue_full = %d, want 0: a closed manager is not a full queue", got)
		}
	})

	t.Run("queue full on NotifyRemoved", func(t *testing.T) {
		// No Start, so nothing drains the one-slot queue.
		mgr := subscription.NewManager(&staticLister{}, 1, 1)
		rec := &outcomeRecorder{}
		mgr.SetObserver(rec.record)
		if err := mgr.NotifyRemoved(context.Background(), closedSub("/edev/1/sub/1")); err != nil {
			t.Fatalf("first NotifyRemoved: %v", err)
		}
		_ = mgr.NotifyRemoved(context.Background(), closedSub("/edev/1/sub/2"))
		if got := rec.count("queue_full"); got != 1 {
			t.Errorf("queue_full = %d, want 1", got)
		}
	})
}
