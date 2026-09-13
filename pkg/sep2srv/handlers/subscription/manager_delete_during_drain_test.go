package subscription_test

import (
	"context"
	"errors"
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

// TestDeleteDuringDrainDoesNotPanic runs DELETEs concurrently with Close so
// the race detector sees enqueue and Close interleave. Whether any DELETE
// lands after Close is left to scheduling; TestDeleteHeldAcrossCloseSeesClosedError
// forces that overlap and asserts the closed error.
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

// TestDeleteHeldAcrossCloseSeesClosedError fixes the overlap instead of
// hoping for it: a first batch of DELETEs completes before shutdown, and a
// second batch is held inside the handler, after the store delete, until
// Close has run and a real http.Server drain is waiting on those requests.
//
// Not parallel: it swaps the process-wide log output (captureLog).
func TestDeleteHeldAcrossCloseSeesClosedError(t *testing.T) {
	logs := captureLog(t)

	const perBatch = 4
	s := memory.NewSubscriptionStore()
	ids := make([]string, 0, 2*perBatch)
	for _, batch := range []string{"before", "held"} {
		for i := range perBatch {
			id := batch + "-" + strconv.Itoa(i)
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
			ids = append(ids, id)
		}
	}

	mgr := subscription.NewManager(&staticLister{}, 1, 2*perBatch, loopbackReceivers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(startDone)
	}()

	entered := make(chan struct{}, perBatch)
	release := make(chan struct{})
	var mu sync.Mutex
	notifyErrs := map[string]error{}
	notify := func(ctx context.Context, sub sep2.Subscription) error {
		if strings.Contains(sub.Href, "/held-") {
			entered <- struct{}{}
			<-release
		}
		err := mgr.NotifyRemoved(ctx, sub)
		mu.Lock()
		notifyErrs[sub.Href] = err
		mu.Unlock()
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}", subscription.HandleDeleteSubscription(s, notify))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := srv.Client()

	del := func(id string) (int, error) {
		req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/edev-1/sub/"+id, nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}

	for i := range perBatch {
		id := "before-" + strconv.Itoa(i)
		if code, err := del(id); err != nil || code != http.StatusNoContent {
			t.Fatalf("DELETE %s before shutdown: status %d, err %v; want 204", id, code, err)
		}
	}

	statuses := make([]int, perBatch)
	reqErrs := make([]error, perBatch)
	var wg sync.WaitGroup
	for i := range perBatch {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], reqErrs[i] = del("held-" + strconv.Itoa(i))
		}(i)
	}
	for range perBatch {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("held DELETEs never reached the notify step")
		}
	}

	cancel()
	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down within 2s")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Config.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("server drain finished (err %v) while %d DELETEs were still in flight", err, perBatch)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	wg.Wait()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("server drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server drain did not finish after the held DELETEs were released")
	}

	for i := range perBatch {
		if reqErrs[i] != nil || statuses[i] != http.StatusNoContent {
			t.Errorf("DELETE held-%d during drain: status %d, err %v; want 204", i, statuses[i], reqErrs[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		href := "/edev/edev-1/sub/" + id
		err, ok := notifyErrs[href]
		switch {
		case !ok:
			t.Errorf("%s: notify was never called", href)
		case strings.HasPrefix(id, "held-") && !errors.Is(err, subscription.ErrManagerClosed):
			t.Errorf("%s: NotifyRemoved after Close returned %v, want ErrManagerClosed", href, err)
		case strings.HasPrefix(id, "before-") && err != nil:
			t.Errorf("%s: NotifyRemoved before Close returned %v, want nil", href, err)
		}
		if _, err := s.Store.Get(context.Background(), id); err == nil {
			t.Errorf("%s still present in store after DELETE", id)
		}
	}
	if got := strings.Count(logs.String(), "notification manager closed"); got != perBatch {
		t.Errorf("handler log lines naming the closed manager = %d, want %d; log:\n%s", got, perBatch, logs.String())
	}
}
