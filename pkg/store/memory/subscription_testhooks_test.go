package memory_test

import (
	"context"
	"sort"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// Helper: build a representative subscription with all fields the snapshot
// must round-trip — Href, SubscribedResource, NotificationURI, Encoding,
// Limit, and an optional Condition pointer.
func newSub(href, subResource, notifyURI string) sep2.Subscription {
	return sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: href},
		},
		SubscribedResource: subResource,
		NotificationURI:    notifyURI,
		Encoding:           sep2.EncodingXML,
		Limit:              10,
		Condition: &sep2.Condition{
			AttributeIdentifier: 1,
			LowerThreshold:      100,
			UpperThreshold:      200,
		},
	}
}

// sortRecords gives the snapshot a deterministic order for comparison.
func sortRecords(recs []memory.SubscriptionRecord) {
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
}

func TestSnapshotForTestingEmptyStore(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()

	got := store.SnapshotForTesting()
	if len(got) != 0 {
		t.Fatalf("SnapshotForTesting on fresh store = %d records, want 0", len(got))
	}
}

func TestSnapshotForTestingReturnsAllFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	sub := newSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")
	if err := store.Create(ctx, "sub1", sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := store.SnapshotForTesting()
	if len(got) != 1 {
		t.Fatalf("SnapshotForTesting len = %d, want 1", len(got))
	}
	rec := got[0]
	if rec.ID != "sub1" {
		t.Errorf("ID = %q, want %q", rec.ID, "sub1")
	}
	if rec.Subscription.Href != sub.Href {
		t.Errorf("Href = %q, want %q", rec.Subscription.Href, sub.Href)
	}
	if rec.Subscription.SubscribedResource != sub.SubscribedResource {
		t.Errorf("SubscribedResource = %q, want %q",
			rec.Subscription.SubscribedResource, sub.SubscribedResource)
	}
	if rec.Subscription.NotificationURI != sub.NotificationURI {
		t.Errorf("NotificationURI = %q, want %q",
			rec.Subscription.NotificationURI, sub.NotificationURI)
	}
	if rec.Subscription.Encoding != sub.Encoding {
		t.Errorf("Encoding = %d, want %d", rec.Subscription.Encoding, sub.Encoding)
	}
	if rec.Subscription.Limit != sub.Limit {
		t.Errorf("Limit = %d, want %d", rec.Subscription.Limit, sub.Limit)
	}
	if rec.Subscription.Condition == nil {
		t.Fatal("Condition is nil, want non-nil after round-trip")
	}
	if *rec.Subscription.Condition != *sub.Condition {
		t.Errorf("Condition = %+v, want %+v",
			*rec.Subscription.Condition, *sub.Condition)
	}
}

func TestSnapshotForTestingIsIndependentCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	sub := newSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")
	if err := store.Create(ctx, "sub1", sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	snap := store.SnapshotForTesting()
	// Mutating the snapshot must not affect a re-snapshot — proves the
	// snapshot is a copy, not a view into live state.
	snap[0].Subscription.NotificationURI = "http://mutated.test/notify"
	if snap[0].Subscription.Condition != nil {
		snap[0].Subscription.Condition.UpperThreshold = 99999
	}

	again := store.SnapshotForTesting()
	if again[0].Subscription.NotificationURI != sub.NotificationURI {
		t.Errorf("snapshot mutation leaked into store: NotificationURI = %q",
			again[0].Subscription.NotificationURI)
	}
	if again[0].Subscription.Condition.UpperThreshold != sub.Condition.UpperThreshold {
		t.Errorf("snapshot mutation leaked into store: Condition.UpperThreshold = %d",
			again[0].Subscription.Condition.UpperThreshold)
	}
}

func TestRestoreForTestingPopulatesIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	subA := newSub("/edev/1/sub/1", "/edev/1", "http://a.test/notify")
	subB := newSub("/edev/2/sub/1", "/edev/2", "http://b.test/notify")
	snapshot := []memory.SubscriptionRecord{
		{ID: "subA", Subscription: subA},
		{ID: "subB", Subscription: subB},
	}

	store.RestoreForTesting(snapshot)

	// Verify primary store contents via ListByResource (which exercises
	// the resourceIndex secondary index).
	gotA, err := store.ListByResource(ctx, "/edev/1")
	if err != nil {
		t.Fatalf("ListByResource(/edev/1): %v", err)
	}
	if len(gotA) != 1 || gotA[0].ID != "subA" || gotA[0].Subscription.NotificationURI != subA.NotificationURI {
		t.Errorf("ListByResource(/edev/1) = %+v, want one entry for subA", gotA)
	}
	gotB, err := store.ListByResource(ctx, "/edev/2")
	if err != nil {
		t.Fatalf("ListByResource(/edev/2): %v", err)
	}
	if len(gotB) != 1 || gotB[0].ID != "subB" || gotB[0].Subscription.NotificationURI != subB.NotificationURI {
		t.Errorf("ListByResource(/edev/2) = %+v, want one entry for subB", gotB)
	}

	// Direct Get round-trip.
	got, err := store.Get(ctx, "subA")
	if err != nil {
		t.Fatalf("Get(subA): %v", err)
	}
	if got.NotificationURI != subA.NotificationURI {
		t.Errorf("Get(subA).NotificationURI = %q, want %q",
			got.NotificationURI, subA.NotificationURI)
	}
}

func TestRestoreForTestingReplacesPriorState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	// Seed with one subscription, then restore a different snapshot.
	if err := store.Create(ctx, "old", newSub("/edev/9/sub/1", "/edev/9", "http://old.test")); err != nil {
		t.Fatalf("Create(old): %v", err)
	}

	store.RestoreForTesting([]memory.SubscriptionRecord{
		{ID: "new", Subscription: newSub("/edev/1/sub/1", "/edev/1", "http://new.test")},
	})

	if _, err := store.Get(ctx, "old"); err == nil {
		t.Error("old subscription survived RestoreForTesting; want gone")
	}
	if _, err := store.Get(ctx, "new"); err != nil {
		t.Errorf("Get(new) after Restore: %v", err)
	}

	// Old resource index entry must also be cleared.
	gotOld, err := store.ListByResource(ctx, "/edev/9")
	if err != nil {
		t.Fatalf("ListByResource(/edev/9): %v", err)
	}
	if len(gotOld) != 0 {
		t.Errorf("ListByResource(/edev/9) = %d entries after Restore, want 0", len(gotOld))
	}
}

func TestSnapshotRestoreRoundTripAcrossNewStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Populate the first store.
	store1 := memory.NewSubscriptionStore()
	subA := newSub("/edev/1/sub/1", "/edev/1", "http://a.test/notify")
	subB := newSub("/edev/2/sub/1", "/edev/2", "http://b.test/notify")
	if err := store1.Create(ctx, "subA", subA); err != nil {
		t.Fatalf("Create(subA): %v", err)
	}
	if err := store1.Create(ctx, "subB", subB); err != nil {
		t.Fatalf("Create(subB): %v", err)
	}

	// Snapshot from store1, restore into a brand-new store2 (simulates
	// "server restart" — old in-memory instance is gone, new instance
	// rebuilds from the snapshot).
	snap := store1.SnapshotForTesting()
	store2 := memory.NewSubscriptionStore()
	store2.RestoreForTesting(snap)

	got := store2.SnapshotForTesting()
	if len(got) != len(snap) {
		t.Fatalf("round-trip len = %d, want %d", len(got), len(snap))
	}

	sortRecords(got)
	sortRecords(snap)
	for i := range snap {
		if got[i].ID != snap[i].ID {
			t.Errorf("record %d: ID = %q, want %q", i, got[i].ID, snap[i].ID)
		}
		if got[i].Subscription.NotificationURI != snap[i].Subscription.NotificationURI {
			t.Errorf("record %d: NotificationURI = %q, want %q",
				i, got[i].Subscription.NotificationURI,
				snap[i].Subscription.NotificationURI)
		}
		if got[i].Subscription.SubscribedResource != snap[i].Subscription.SubscribedResource {
			t.Errorf("record %d: SubscribedResource = %q, want %q",
				i, got[i].Subscription.SubscribedResource,
				snap[i].Subscription.SubscribedResource)
		}
	}
}
