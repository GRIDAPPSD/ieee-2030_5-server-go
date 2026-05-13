package memory_test

import (
	"context"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-099: SubscriptionStore must surface subscriptions scoped to the
// EndDevice they were POSTed under. Before this ticket landed, the
// secondary deviceIndex existed but was never populated, so
// ListByDeviceWithIDs (and ListByDevice) returned empty regardless of
// state. The handler at GET /edev/{id}/sub was therefore wired to the
// underlying union store and leaked subscriptions across EndDevices.
//
// These tests pin the scoped behavior on the store layer. The handler
// integration tests live next to the router (see
// internal/server/subscription_scope_test.go).

func TestListByDeviceWithIDs_EmptyStore(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()

	got, err := store.ListByDeviceWithIDs(context.Background(), "1")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs on fresh store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByDeviceWithIDs on fresh store = %d records, want 0", len(got))
	}
}

func TestListByDeviceWithIDs_ScopesByEndDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	// Two EndDevices each get two subscriptions. The store must surface
	// exactly the subscriptions owned by the queried EndDevice — no
	// cross-EndDevice leakage.
	mustCreate := func(id, edevID, resource string) {
		t.Helper()
		sub := sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/" + edevID + "/sub/" + id},
			},
			SubscribedResource: resource,
			NotificationURI:    "https://example.test/notify/" + id,
		}
		if err := store.Create(ctx, id, sub); err != nil {
			t.Fatalf("Create %q: %v", id, err)
		}
	}
	mustCreate("a1", "1", "/edev/1")
	mustCreate("a2", "1", "/edev/1/fsa")
	mustCreate("b1", "2", "/edev/2")
	mustCreate("b2", "2", "/edev/2/fsa")

	gotA, err := store.ListByDeviceWithIDs(ctx, "1")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs(1): %v", err)
	}
	if got, want := len(gotA), 2; got != want {
		t.Fatalf("ListByDeviceWithIDs(1) len = %d, want %d (records=%+v)", got, want, gotA)
	}
	gotIDsA := []string{gotA[0].ID, gotA[1].ID}
	sort.Strings(gotIDsA)
	if gotIDsA[0] != "a1" || gotIDsA[1] != "a2" {
		t.Errorf("ListByDeviceWithIDs(1) IDs = %v, want [a1 a2]", gotIDsA)
	}

	gotB, err := store.ListByDeviceWithIDs(ctx, "2")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs(2): %v", err)
	}
	if got, want := len(gotB), 2; got != want {
		t.Fatalf("ListByDeviceWithIDs(2) len = %d, want %d (records=%+v)", got, want, gotB)
	}
	gotIDsB := []string{gotB[0].ID, gotB[1].ID}
	sort.Strings(gotIDsB)
	if gotIDsB[0] != "b1" || gotIDsB[1] != "b2" {
		t.Errorf("ListByDeviceWithIDs(2) IDs = %v, want [b1 b2]", gotIDsB)
	}

	gotMissing, err := store.ListByDeviceWithIDs(ctx, "ghost")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs(ghost): %v", err)
	}
	if len(gotMissing) != 0 {
		t.Errorf("ListByDeviceWithIDs(ghost) = %d, want 0", len(gotMissing))
	}
}

func TestListByDeviceWithIDs_AfterDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/a1"},
		},
		SubscribedResource: "/edev/1",
	}
	if err := store.Create(ctx, "a1", sub); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Delete(ctx, "a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := store.ListByDeviceWithIDs(ctx, "1")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByDeviceWithIDs after Delete = %d, want 0", len(got))
	}
}

// RestoreForTesting must rebuild the deviceIndex from the snapshot — the
// persistence reload path (LoadFromFile → RestoreForTesting) goes through
// here, so a broken rebuild would silently zero out the per-EndDevice
// view after a server restart.
func TestListByDeviceWithIDs_AfterRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	if err := store.Create(ctx, "a1", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/a1"},
		},
		SubscribedResource: "/edev/1",
	}); err != nil {
		t.Fatalf("Create a1: %v", err)
	}
	if err := store.Create(ctx, "b1", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/2/sub/b1"},
		},
		SubscribedResource: "/edev/2",
	}); err != nil {
		t.Fatalf("Create b1: %v", err)
	}

	snap := store.SnapshotForTesting()

	// New store, restore the snapshot, query.
	store2 := memory.NewSubscriptionStore()
	store2.RestoreForTesting(snap)

	got1, err := store2.ListByDeviceWithIDs(ctx, "1")
	if err != nil {
		t.Fatalf("post-restore ListByDeviceWithIDs(1): %v", err)
	}
	if len(got1) != 1 || got1[0].ID != "a1" {
		t.Errorf("post-restore ListByDeviceWithIDs(1) = %+v, want one entry for a1", got1)
	}
	got2, err := store2.ListByDeviceWithIDs(ctx, "2")
	if err != nil {
		t.Fatalf("post-restore ListByDeviceWithIDs(2): %v", err)
	}
	if len(got2) != 1 || got2[0].ID != "b1" {
		t.Errorf("post-restore ListByDeviceWithIDs(2) = %+v, want one entry for b1", got2)
	}
}

// ListByDevice is the legacy []Subscription accessor (no ID). The handler
// uses ListByDeviceWithIDs to keep the storage ID for paging, but the
// bare-Subscription view is still exported. Pin it here so it does not
// regress to the pre-IEEE-099 always-empty behavior.
func TestListByDevice_ReturnsScopedSubscriptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	if err := store.Create(ctx, "a1", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/a1"},
		},
		SubscribedResource: "/edev/1",
	}); err != nil {
		t.Fatalf("Create a1: %v", err)
	}
	if err := store.Create(ctx, "b1", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/2/sub/b1"},
		},
		SubscribedResource: "/edev/2",
	}); err != nil {
		t.Fatalf("Create b1: %v", err)
	}

	got, err := store.ListByDevice(ctx, "1")
	if err != nil {
		t.Fatalf("ListByDevice(1): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByDevice(1) len = %d, want 1", len(got))
	}
	if got[0].Href != "/edev/1/sub/a1" {
		t.Errorf("ListByDevice(1)[0].Href = %q, want %q", got[0].Href, "/edev/1/sub/a1")
	}
}

// Subscriptions whose Href does not resolve to an /edev/{id}/... prefix
// are not indexed by EndDevice. They remain reachable via the primary
// Store but absent from the per-EndDevice view — this matches the
// handler contract (GET /edev/{id}/sub returns only edev-scoped entries).
func TestListByDeviceWithIDs_IgnoresNonEdevHrefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.NewSubscriptionStore()

	if err := store.Create(ctx, "x1", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/some/other/path/x1"},
		},
		SubscribedResource: "/dcap",
	}); err != nil {
		t.Fatalf("Create x1: %v", err)
	}

	got, err := store.ListByDeviceWithIDs(ctx, "1")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByDeviceWithIDs(1) = %d entries, want 0 (non-edev href must not index)", len(got))
	}
}
