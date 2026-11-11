package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func idsOf(recs []memory.SubscriptionRecord) []string {
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids
}

// Moving one of two subscribers off a shared resource must drop only its own
// index entry, and must not reorder the device index that paging relies on.
func TestSubscriptionUpdate_MovingOneSubscriberKeepsTheOther(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewSubscriptionStore()
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Create(ctx, id, newSub("/edev/1/sub/"+id, "/edev/1", "http://"+id+".test/n")); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}

	if err := s.Update(ctx, "b", newSub("/edev/1/sub/b", "/edev/1/der", "http://b.test/n")); err != nil {
		t.Fatalf("Update: %v", err)
	}

	old, _ := s.ListByResource(ctx, "/edev/1")
	if got := idsOf(old); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("old resource holds %v, want [a c]", got)
	}
	moved, _ := s.ListByResource(ctx, "/edev/1/der")
	if got := idsOf(moved); len(got) != 1 || got[0] != "b" {
		t.Errorf("new resource holds %v, want [b]", got)
	}
	byDev, _ := s.ListByDeviceWithIDs(ctx, "1")
	if got := idsOf(byDev); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("device index holds %v, want [a b c] in insertion order", got)
	}
}
