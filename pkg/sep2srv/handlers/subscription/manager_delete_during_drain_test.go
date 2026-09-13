package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandleDeleteSubscription_ManagerClosedLogsAndReturns204 covers
// acceptance criterion 3 of #460: once the Manager is closed, the DELETE
// handler still returns 204 (the final Notification is best-effort per
// CSIP V1.2 section 11.6) and it logs the closed-manager outcome instead
// of propagating a panic.
//
// Not parallel: it swaps the process-wide log output (captureLog).
func TestHandleDeleteSubscription_ManagerClosedLogsAndReturns204(t *testing.T) {
	logs := captureLog(t)

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

	mgr := subscription.NewManager(&staticLister{}, 1, 4, loopbackReceivers)
	mgr.Close() // simulates the manager already having drained at shutdown

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, mgr.NotifyRemoved))

	req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204 even with the manager closed; body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "subscription: notify removed for") ||
		!strings.Contains(logs.String(), "notification manager closed") {
		t.Errorf("log missing the closed-manager notify-removed line; got:\n%s", logs.String())
	}
	if _, err := s.Store.Get(context.Background(), "sub-1"); err == nil {
		t.Error("sub still present in store after DELETE with the manager closed")
	}
}

// TestDeleteDuringDrainDoesNotPanic covers acceptance criterion 4: the
// shutdown order between the HTTP server drain and the manager close is
// not synchronized (see the Start doc comment in manager.go, and
// internal/server/server.go:198 vs :360, where both select on the SAME
// ctx.Done() independently). This test drives a DELETE through the full
// handler concurrently with Manager.Close, standing in for a DELETE
// arriving while the HTTP server is draining in-flight requests.
func TestDeleteDuringDrainDoesNotPanic(t *testing.T) {
	s := memory.NewSubscriptionStore()
	for i := range 50 {
		id := "sub-" + strconv.Itoa(i)
		sub := sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/edev-1/sub/" + id},
			},
			SubscribedResource: "/edev/edev-1",
			NotificationURI:    "http://127.0.0.1:1/never",
		}
		if err := s.Create(context.Background(), id, sub); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	mgr := subscription.NewManager(&staticLister{}, 2, 8, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}",
		subscription.HandleDeleteSubscription(s, mgr.NotifyRemoved))

	var wg sync.WaitGroup
	statuses := make([]int, 50)
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "sub-" + strconv.Itoa(i)
			req := httptest.NewRequest(http.MethodDelete, "/edev/edev-1/sub/"+id, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req) // must never panic, closed manager or not
			statuses[i] = rec.Code
		}(i)
	}

	// Cancel mid-flight: this is the drain-arrives-during-DELETE race
	// from #460, since Start's ctx.Done() and this cancel fire together
	// while the DELETE goroutines above are still calling NotifyRemoved.
	time.Sleep(1 * time.Millisecond)
	cancel()

	wg.Wait()

	for i, code := range statuses {
		if code != http.StatusNoContent {
			t.Errorf("sub-%d: DELETE status = %d, want 204", i, code)
		}
		id := "sub-" + strconv.Itoa(i)
		if _, err := s.Store.Get(context.Background(), id); err == nil {
			t.Errorf("sub-%d still present in store after DELETE", i)
		}
	}
}
