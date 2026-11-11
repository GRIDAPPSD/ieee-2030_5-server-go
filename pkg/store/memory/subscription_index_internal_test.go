package memory

import (
	"context"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func indexedSub(href, resource string) sep2.Subscription {
	return sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: href}},
		SubscribedResource:   resource,
		NotificationURI:      "http://x.test/n",
	}
}

// A DELETE racing a PUT that moves subscribedResource must leave no index
// entry for the deleted record. The listing methods hide a stale entry (they
// skip ids the store no longer holds), so the index maps are read directly.
func TestSubscriptionDeleteRacingUpdateLeavesNoStaleIndex(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 3000; round++ {
		s := NewSubscriptionStore()
		if err := s.Create(ctx, "x", indexedSub("/edev/1/sub/x", "/edev/1")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.Update(ctx, "x", indexedSub("/edev/1/sub/x", "/edev/1/der"))
		}()
		go func() {
			defer wg.Done()
			_ = s.Delete(ctx, "x")
		}()
		wg.Wait()

		if _, err := s.Get(ctx, "x"); err == nil {
			t.Fatalf("round %d: record survived the delete", round)
		}
		s.idxMu.RLock()
		stale := len(s.resourceIndex["/edev/1"]) + len(s.resourceIndex["/edev/1/der"]) + len(s.deviceIndex["1"])
		s.idxMu.RUnlock()
		if stale != 0 {
			t.Fatalf("round %d: %d stale index entries for a deleted subscription", round, stale)
		}
	}
}
