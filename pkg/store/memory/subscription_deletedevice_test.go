package memory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func mkSub(href, resource string) sep2.Subscription {
	sub := sep2.Subscription{SubscribedResource: resource}
	sub.Href = href
	return sub
}

func countAll(t *testing.T, s *memory.SubscriptionStore, resources ...string) int {
	t.Helper()
	n := 0
	for _, r := range resources {
		recs, err := s.ListByResource(context.Background(), r)
		if err != nil {
			t.Fatalf("ListByResource(%q): %v", r, err)
		}
		n += len(recs)
	}
	return n
}

// DeleteDevice removes the device's own subscriptions and those any device
// holds on the device or beneath it, and nothing else. e10 shares the string
// prefix "/edev/e1" and must keep its own.
func TestSubscriptionStore_DeleteDevice_ScopeByValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewSubscriptionStore()
	seed := map[string]sep2.Subscription{
		"own":      mkSub("/edev/e1/sub/a", "/edev"),
		"onDevice": mkSub("/edev/e5/sub/b", "/edev/e1"),
		"onDER":    mkSub("/edev/e5/sub/c", "/edev/e1/der/1/ders"),
		"neighbor": mkSub("/edev/e10/sub/d", "/edev/e10/der/1/ders"),
		"watchesN": mkSub("/edev/e5/sub/e", "/edev/e10"),
	}
	for id, sub := range seed {
		if err := s.Create(ctx, id, sub); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	if err := s.DeleteDevice(ctx, "e1"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}

	for _, id := range []string{"own", "onDevice", "onDER"} {
		if _, err := s.Get(ctx, id); err == nil {
			t.Errorf("subscription %s survived DeleteDevice(e1)", id)
		}
	}
	for _, id := range []string{"neighbor", "watchesN"} {
		if _, err := s.Get(ctx, id); err != nil {
			t.Errorf("subscription %s was removed by DeleteDevice(e1): %v", id, err)
		}
	}
	if n := countAll(t, s, "/edev", "/edev/e1", "/edev/e1/der/1/ders"); n != 0 {
		t.Errorf("resource index still lists %d subscriptions for e1's resources", n)
	}
	if recs, _ := s.ListByDeviceWithIDs(ctx, "e1"); len(recs) != 0 {
		t.Errorf("device index still lists %d subscriptions for e1", len(recs))
	}
}

// The snapshot DeleteDevice writes must survive a reload.
func TestSubscriptionStore_DeleteDevice_PersistsAcrossReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "subs.json")
	s, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Create(ctx, "a", mkSub("/edev/e1/sub/a", "/edev")); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := s.Create(ctx, "b", mkSub("/edev/e2/sub/b", "/edev")); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	if err := s.DeleteDevice(ctx, "e1"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}

	reloaded, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := reloaded.Get(ctx, "a"); err == nil {
		t.Error("e1's subscription came back after reload: DeleteDevice did not persist")
	}
	if _, err := reloaded.Get(ctx, "b"); err != nil {
		t.Errorf("e2's subscription missing after reload: %v", err)
	}
}

// Nothing removed means no snapshot is written, so a device with no
// subscriptions never touches the disk on DELETE.
func TestSubscriptionStore_DeleteDevice_NothingRemovedWritesNoSnapshot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "subs.json")
	s, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.DeleteDevice(context.Background(), "e1"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("snapshot file after a no-op DeleteDevice: stat err = %v, want not-exist", err)
	}
}
