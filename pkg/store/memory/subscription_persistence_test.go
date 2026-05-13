package memory_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-077: durable subscription persistence — these tests pin the spec's
// minimum contract for the Path B JSON-file backend:
//   1. Create writes a recoverable snapshot to the configured path.
//   2. Delete updates the on-disk snapshot.
//   3. LoadFromFile rehydrates a fresh store from disk (cold start).
//   4. Snapshot/restore parity: after a write/load cycle the in-memory
//      view matches the SnapshotForTesting() shape from IEEE-022.
//   5. Crash-shape: a half-written temp file does not corrupt the live
//      snapshot (atomic-rename).
//   6. Concurrency: parallel Create/Delete/Get is race-clean and the
//      persisted file converges on a consistent state.

// newPersistedStore returns a fresh in-memory SubscriptionStore with the
// persistence path wired to a file inside t.TempDir(), plus the path
// itself so callers can inspect / corrupt the file.
func newPersistedStore(t *testing.T) (*memory.SubscriptionStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subs.json")
	store, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence: %v", err)
	}
	return store, path
}

func TestPersistence_CreateWritesSnapshotToDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedStore(t)

	sub := newSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")
	if err := store.Create(ctx, "sub1", sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot file is empty after Create")
	}
}

func TestPersistence_DeleteUpdatesSnapshotOnDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedStore(t)

	if err := store.Create(ctx, "subA", newSub("/edev/1/sub/1", "/edev/1", "http://a.test")); err != nil {
		t.Fatalf("Create(subA): %v", err)
	}
	if err := store.Create(ctx, "subB", newSub("/edev/2/sub/1", "/edev/2", "http://b.test")); err != nil {
		t.Fatalf("Create(subB): %v", err)
	}
	if err := store.Delete(ctx, "subA"); err != nil {
		t.Fatalf("Delete(subA): %v", err)
	}

	// Rehydrate a fresh store from disk — only subB must survive.
	revived := memory.NewSubscriptionStore()
	if err := revived.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if _, err := revived.Get(ctx, "subA"); err == nil {
		t.Error("subA still present in rehydrated store after Delete")
	}
	got, err := revived.Get(ctx, "subB")
	if err != nil {
		t.Fatalf("Get(subB) after revive: %v", err)
	}
	if got.NotificationURI != "http://b.test" {
		t.Errorf("Get(subB).NotificationURI = %q, want %q", got.NotificationURI, "http://b.test")
	}
}

func TestPersistence_LoadFromFile_EmptyPath_IsNoOp(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()
	// A path that does not exist must NOT error — it means "cold boot,
	// nothing to restore." The server uses this on startup.
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	if err := store.LoadFromFile(missing); err != nil {
		t.Fatalf("LoadFromFile(missing) = %v, want nil for cold boot", err)
	}
}

func TestPersistence_LoadFromFile_RejectsCorruptJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "subs.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	store := memory.NewSubscriptionStore()
	if err := store.LoadFromFile(path); err == nil {
		t.Fatal("LoadFromFile on corrupt JSON returned nil; want error")
	}
}

func TestPersistence_RoundTripMatchesSnapshotForTesting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Populate a persisted store.
	store1, path := newPersistedStore(t)
	subA := newSub("/edev/1/sub/1", "/edev/1", "http://a.test/notify")
	subB := newSub("/edev/2/sub/1", "/edev/2", "http://b.test/notify")
	if err := store1.Create(ctx, "subA", subA); err != nil {
		t.Fatalf("Create(subA): %v", err)
	}
	if err := store1.Create(ctx, "subB", subB); err != nil {
		t.Fatalf("Create(subB): %v", err)
	}

	want := store1.SnapshotForTesting()
	sortRecords(want)

	// Rehydrate from the on-disk file into a brand-new store.
	store2 := memory.NewSubscriptionStore()
	if err := store2.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	got := store2.SnapshotForTesting()
	sortRecords(got)

	if len(got) != len(want) {
		t.Fatalf("round-trip len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Errorf("rec %d: ID = %q, want %q", i, got[i].ID, want[i].ID)
		}
		if got[i].Subscription.Href != want[i].Subscription.Href {
			t.Errorf("rec %d: Href = %q, want %q", i, got[i].Subscription.Href, want[i].Subscription.Href)
		}
		if got[i].Subscription.SubscribedResource != want[i].Subscription.SubscribedResource {
			t.Errorf("rec %d: SubscribedResource = %q, want %q",
				i, got[i].Subscription.SubscribedResource, want[i].Subscription.SubscribedResource)
		}
		if got[i].Subscription.NotificationURI != want[i].Subscription.NotificationURI {
			t.Errorf("rec %d: NotificationURI = %q, want %q",
				i, got[i].Subscription.NotificationURI, want[i].Subscription.NotificationURI)
		}
		if got[i].Subscription.Encoding != want[i].Subscription.Encoding {
			t.Errorf("rec %d: Encoding = %d, want %d",
				i, got[i].Subscription.Encoding, want[i].Subscription.Encoding)
		}
		if got[i].Subscription.Limit != want[i].Subscription.Limit {
			t.Errorf("rec %d: Limit = %d, want %d",
				i, got[i].Subscription.Limit, want[i].Subscription.Limit)
		}
		if (got[i].Subscription.Condition == nil) != (want[i].Subscription.Condition == nil) {
			t.Errorf("rec %d: Condition nilness diverged", i)
			continue
		}
		if got[i].Subscription.Condition != nil &&
			*got[i].Subscription.Condition != *want[i].Subscription.Condition {
			t.Errorf("rec %d: Condition = %+v, want %+v",
				i, *got[i].Subscription.Condition, *want[i].Subscription.Condition)
		}
	}
}

// TestPersistence_LoadRebuildsSecondaryIndexes proves that LoadFromFile
// not only writes data back into the primary store but also rebuilds the
// resource and device indexes — i.e. the loaded state is fully
// equivalent to a freshly-Created state (parity with RestoreForTesting).
func TestPersistence_LoadRebuildsSecondaryIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store1, path := newPersistedStore(t)

	subA := newSub("/edev/1/sub/1", "/edev/1", "http://a.test/notify")
	if err := store1.Create(ctx, "subA", subA); err != nil {
		t.Fatalf("Create(subA): %v", err)
	}

	store2 := memory.NewSubscriptionStore()
	if err := store2.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	got, err := store2.ListByResource(ctx, "/edev/1")
	if err != nil {
		t.Fatalf("ListByResource: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByResource(/edev/1) = %d entries, want 1 (secondary index not rebuilt)", len(got))
	}
	if got[0].Subscription.NotificationURI != subA.NotificationURI {
		t.Errorf("ListByResource entry NotificationURI = %q, want %q",
			got[0].Subscription.NotificationURI, subA.NotificationURI)
	}
	if got[0].ID != "subA" {
		t.Errorf("ListByResource entry ID = %q, want %q", got[0].ID, "subA")
	}
}

// TestPersistence_AtomicWrite_NoCorruptionOnStaleTemp simulates the
// "crash mid-write" shape: a stale .tmp file from a prior crashed write
// is left in the directory. The next successful Create must replace the
// committed file via atomic rename — the stale .tmp must NOT poison the
// load path.
func TestPersistence_AtomicWrite_NoCorruptionOnStaleTemp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "subs.json")

	// Seed a half-written temp file BEFORE the store is created.
	stale := path + ".tmp"
	if err := os.WriteFile(stale, []byte("{partial garbage"), 0o644); err != nil {
		t.Fatalf("seed stale tmp: %v", err)
	}

	store, err := memory.NewSubscriptionStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence: %v", err)
	}
	if err := store.Create(ctx, "sub1", newSub("/edev/1/sub/1", "/edev/1", "http://a.test")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The committed file must parse cleanly.
	revived := memory.NewSubscriptionStore()
	if err := revived.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile after stale tmp: %v", err)
	}
	if _, err := revived.Get(ctx, "sub1"); err != nil {
		t.Errorf("sub1 missing after crash-shape recovery: %v", err)
	}
}

// TestPersistence_ConcurrentCreatesAreRaceClean exercises N parallel
// Create / Delete / Get goroutines against the persisted store. We
// don't care about exact final cardinality — only that the operations
// are race-clean (run under -race) and the persisted file is valid
// JSON at the end.
func TestPersistence_ConcurrentCreatesAreRaceClean(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedStore(t)

	const workers = 8
	const opsPerWorker = 25

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				id := fmt.Sprintf("w%d-sub%d", workerID, i)
				sub := newSub(
					fmt.Sprintf("/edev/%d/sub/%d", workerID, i),
					fmt.Sprintf("/edev/%d", workerID),
					fmt.Sprintf("http://worker%d.test/notify", workerID),
				)
				if err := store.Create(ctx, id, sub); err != nil {
					t.Errorf("worker %d Create %q: %v", workerID, id, err)
					return
				}
				if _, err := store.Get(ctx, id); err != nil {
					t.Errorf("worker %d Get %q after Create: %v", workerID, id, err)
					return
				}
				// Delete half so we exercise the delete-then-persist path too.
				if i%2 == 0 {
					if err := store.Delete(ctx, id); err != nil {
						t.Errorf("worker %d Delete %q: %v", workerID, id, err)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()

	// File must be valid JSON the load path accepts.
	revived := memory.NewSubscriptionStore()
	if err := revived.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile after concurrent run: %v", err)
	}

	// Surviving subscriptions on disk must equal the in-memory state.
	want := store.SnapshotForTesting()
	got := revived.SnapshotForTesting()
	sortRecords(want)
	sortRecords(got)
	if len(want) != len(got) {
		t.Fatalf("on-disk record count = %d, in-memory = %d (drift after concurrent ops)",
			len(got), len(want))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Errorf("rec %d ID = %q, want %q", i, got[i].ID, want[i].ID)
		}
	}
}

// TestPersistence_DeleteNotFoundDoesNotWriteSnapshot pins the contract
// that a no-op Delete (id missing) must NOT trigger a write — otherwise
// every spurious DELETE request becomes a disk syscall.
func TestPersistence_DeleteNotFoundDoesNotWriteSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedStore(t)

	// Create one record so the snapshot file exists with known mtime.
	if err := store.Create(ctx, "sub1", newSub("/edev/1/sub/1", "/edev/1", "http://a.test")); err != nil {
		t.Fatalf("Create(sub1): %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat path: %v", err)
	}

	// Delete a non-existent ID — should return ErrNotFound and NOT
	// rewrite the file. We don't compare mtime (filesystem resolution
	// can be coarse); instead we assert the file is byte-identical.
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read path: %v", err)
	}

	if err := store.Delete(ctx, "ghost"); err == nil {
		t.Error("Delete(ghost) returned nil, want ErrNotFound")
	}

	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read path: %v", err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("snapshot file changed on no-op Delete (before size %d, after size %d)",
			before.Size(), len(afterBytes))
	}
	// Confirm sub1 still present in-memory and on disk.
	revived := memory.NewSubscriptionStore()
	if err := revived.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile after no-op Delete: %v", err)
	}
	if _, err := revived.Get(ctx, "sub1"); err != nil {
		t.Errorf("sub1 missing after no-op Delete: %v", err)
	}
}

// sortRecords helper and newSub helper are defined in
// subscription_testhooks_test.go (same package _test); no redeclaration
// needed here.
