package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// A task still buffered when shutdown cancels the workers' context is never
// delivered. Each one must be counted and logged, and Start must return
// without waiting for the receiver that holds the in-flight delivery.
//
// Not parallel: it swaps the process-wide log output (captureLog).
func TestShutdownDropsBufferedTasksCountedAndLogged(t *testing.T) {
	logs := captureLog(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	const buffered = 4
	mgr := subscription.NewManager(&staticLister{}, 1, buffered, loopbackReceivers)
	rec := &outcomeRecorder{}
	mgr.SetObserver(rec.record)
	cancel, done := startManager(t, mgr)

	sub := func(i int) sep2.Subscription {
		s := closedSub("/edev/1/sub/" + strconv.Itoa(i))
		s.NotificationURI = srv.URL + "/n"
		return s
	}
	if err := mgr.NotifyRemoved(context.Background(), sub(0)); err != nil {
		t.Fatalf("NotifyRemoved(0): %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached the receiver, so the queue behind it cannot be shown buffered")
	}
	for i := 1; i <= buffered; i++ {
		if err := mgr.NotifyRemoved(context.Background(), sub(i)); err != nil {
			t.Fatalf("NotifyRemoved(%d): %v", i, err)
		}
	}

	cancel()
	waitClosed(t, done, "Start returning after cancel with a receiver still holding a delivery")

	if got := hits.Load(); got != 1 {
		t.Errorf("receiver hits = %d, want 1: buffered tasks must not be delivered after shutdown", got)
	}
	if got := rec.count("manager_closed"); got != buffered {
		t.Errorf("manager_closed observed = %d, want %d (one per buffered task)", got, buffered)
	}
	const dropLine = "notification: manager shutting down, dropping for http://127.0.0.1:"
	if got := strings.Count(logs.String(), dropLine); got != buffered {
		t.Errorf("shutdown drop log lines = %d, want %d; log:\n%s", got, buffered, logs.String())
	}
}
