package subscription_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Not parallel: it swaps the process-wide ID clock. Holding the clock still
// forces every POST onto one reading, the collision #1010 saw in CI.
func TestCreateSubscriptionParallelPostsGetDistinctIDs(t *testing.T) {
	t.Cleanup(subscription.HoldClock(time.Unix(1700000000, 42)))

	store := memory.NewSubscriptionStore()
	allowURI := func(context.Context, string) error { return nil }
	h := subscription.HandleCreateSubscription(store, allowURI, allowAnyResource)

	const n = 16
	var wg sync.WaitGroup
	recs := make([]int, n)
	locs := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := postSubscriptionWith(t, h, "/edev/3/fsa")
			recs[i], locs[i] = rec.Code, rec.Header().Get("Location")
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i := range n {
		if recs[i] != http.StatusCreated {
			t.Errorf("POST %d status = %d, want 201", i, recs[i])
			continue
		}
		if locs[i] == "" || seen[locs[i]] {
			t.Errorf("POST %d Location = %q, empty or repeated", i, locs[i])
		}
		seen[locs[i]] = true
	}
}
