package memory_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	corestore "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// EndDeviceStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165): pin
// the same contract SubscriptionStore already has
// (GRIDAPPSD/ieee-2030_5-server-go#224). Each test creates a fresh store
// with
// the persistence path wired to a file inside t.TempDir(), exercises the
// mutation, and verifies a fresh store rehydrated from disk sees the same
// state.

func newPersistedEndDeviceStore(t *testing.T) (*memory.EndDeviceStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enddevices.json")
	store, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceStoreWithPersistence: %v", err)
	}
	return store, path
}

func mkDevice(id, sfdi, lfdi string) sep2.EndDevice {
	return sep2.EndDevice{
		SFDI: sfdi,
		LFDI: lfdi,
	}
}

func TestEndDevicePersistence_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	store, err := memory.NewEndDeviceStoreWithPersistence("")
	if err != nil {
		t.Fatalf("empty path should be no-op: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store for empty path (in-memory)")
	}
	// Create works without any disk I/O.
	if err := store.Create(context.Background(), "dev-1", mkDevice("dev-1", "111", "lfdi-111")); err != nil {
		t.Fatalf("Create on in-memory: %v", err)
	}
}

func TestEndDevicePersistence_ColdBootMissingFile(t *testing.T) {
	t.Parallel()
	// Pointing at a path that does not exist must NOT error: that is
	// the normal first-boot shape.
	missing := filepath.Join(t.TempDir(), "subdir", "enddevices.json")
	// Parent dir doesn't exist yet: cold-boot reads do not create it.
	_, err := memory.NewEndDeviceStoreWithPersistence(missing)
	if err != nil {
		t.Fatalf("cold boot must tolerate missing file: %v", err)
	}
}

func TestEndDevicePersistence_CreateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedEndDeviceStore(t)

	dev := mkDevice("dev-A", "1234567890", "lfdi-A")
	if err := store.Create(ctx, "dev-A", dev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot file is empty after Create")
	}

	// Rehydrate a fresh store from disk.
	revived, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "dev-A")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.SFDI != dev.SFDI || got.LFDI != dev.LFDI {
		t.Errorf("revived device = %+v, want SFDI=%q LFDI=%q", got, dev.SFDI, dev.LFDI)
	}
	// Secondary indexes rebuilt.
	bySFDI, err := revived.GetBySFDI(ctx, dev.SFDI)
	if err != nil {
		t.Fatalf("GetBySFDI after revive: %v", err)
	}
	if bySFDI.LFDI != dev.LFDI {
		t.Errorf("GetBySFDI returned wrong device: %+v", bySFDI)
	}
}

func TestEndDevicePersistence_DeleteThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedEndDeviceStore(t)

	if err := store.Create(ctx, "dev-A", mkDevice("dev-A", "sA", "lA")); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if err := store.Create(ctx, "dev-B", mkDevice("dev-B", "sB", "lB")); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if err := store.Delete(ctx, "dev-A"); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	revived, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := revived.Get(ctx, "dev-A"); err == nil {
		t.Error("dev-A should be absent after Delete+revive")
	}
	if _, err := revived.Get(ctx, "dev-B"); err != nil {
		t.Errorf("dev-B should be present after revive: %v", err)
	}
}

// TestEndDevicePersistence_CreateRollsBackWhenTheSnapshotFailsToFlush pins
// GRIDAPPSD/ieee-2030_5-server-go#721: Create inserts the device and its
// SFDI/LFDI indexes before it flushes a snapshot, and a failed flush must
// not leave that insert readable. RegisteredEndDeviceStore.Create trusts a
// non-nil error here to mean nothing happened before it decides whether the
// Registration half needs touching; a half-committed device would let it
// skip that decision while the device stayed servable.
//
// The path's parent directory is never created. That makes the cold-boot
// read at construction (os.ReadFile, ENOENT, no error per readSnapshotEnvelope)
// succeed while the write at Create time (open a sibling .tmp file for
// create, also ENOENT, a real error since a write has no cold-boot reading)
// still fails, which isolates the flush failure this test is about from a
// construction-time one.
func TestEndDevicePersistence_CreateRollsBackWhenTheSnapshotFailsToFlush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "missing-subdir", "snapshot.json")

	s, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceStoreWithPersistence: %v", err)
	}

	if err := s.Create(ctx, "1", mkDevice("1", "1111111111", "AAAA")); err == nil {
		t.Fatal("Create succeeded while the snapshot path could not be written; want an error and nothing stored")
	}

	if _, err := s.Get(ctx, "1"); !errors.Is(err, corestore.ErrNotFound) {
		t.Errorf("Get(1) after the failed flush = %v, want ErrNotFound: the insert must be rolled back", err)
	}
	if _, err := s.GetBySFDI(ctx, "1111111111"); !errors.Is(err, corestore.ErrNotFound) {
		t.Errorf("GetBySFDI after the failed flush = %v, want ErrNotFound: the index must be rolled back too", err)
	}
	if _, err := s.GetByLFDI(ctx, "AAAA"); !errors.Is(err, corestore.ErrNotFound) {
		t.Errorf("GetByLFDI after the failed flush = %v, want ErrNotFound: the index must be rolled back too", err)
	}
}

// TestEndDevicePersistence_CreateRollbackDoesNotLeaveADanglingLFDIIndexEntry
// is a sharper reproduction of the same rollback than the test above: that
// one's GetByLFDI("AAAA") = ErrNotFound assertion also passes if only the
// rollback's Store.Delete ran and the index entry itself leaked, because
// GetByLFDI's second step (Store.Get on the resolved id) fails on its own
// once the record is gone. Reusing the key for a second, successful Create
// under a DIFFERENT LFDI is what actually distinguishes the two cases: if
// the first LFDI's index entry survived, it now resolves straight through
// to the second device (GRIDAPPSD/ieee-2030_5-server-go#721).
func TestEndDevicePersistence_CreateRollbackDoesNotLeaveADanglingLFDIIndexEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	s, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceStoreWithPersistence: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod the snapshot directory read-only: %v", err)
	}
	if err := s.Create(ctx, "1", mkDevice("1", "1111111111", "AAAA")); err == nil {
		t.Fatal("Create succeeded while the snapshot directory was read-only; want an error")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore write access: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.Create(ctx, "1", mkDevice("1", "2222222222", "BBBB")); err != nil {
		t.Fatalf("Create id 1 with LFDI BBBB after the rollback: %v", err)
	}

	if _, err := s.GetByLFDI(ctx, "AAAA"); !errors.Is(err, corestore.ErrNotFound) {
		t.Errorf("GetByLFDI(AAAA) after the rollback and a second Create at the same id = %v, want ErrNotFound: "+
			"a dangling index entry would resolve straight through to the second device", err)
	}
}

// TestEndDeviceStore_DeleteDoesNotEraseAnotherDevicesReclaimedIdentity pins
// GRIDAPPSD/ieee-2030_5-server-go#721: removeIndex used to delete an SFDI
// or LFDI key by value alone, with no check that the key still pointed at
// the id being removed. Store.Create and Store.Update enforce uniqueness on
// id only, not on either identity field, so one device (B) can legally
// reclaim another's (A's) SFDI and LFDI; deleting A afterward must not
// erase B's now-current claim on identities A no longer holds.
func TestEndDeviceStore_DeleteDoesNotEraseAnotherDevicesReclaimedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewEndDeviceStore()

	if err := s.Create(ctx, "1", mkDevice("1", "1111111111", "AAAA")); err != nil {
		t.Fatalf("create device A: %v", err)
	}
	if err := s.Create(ctx, "2", mkDevice("2", "2222222222", "BBBB")); err != nil {
		t.Fatalf("create device B: %v", err)
	}
	if err := s.Update(ctx, "2", mkDevice("2", "1111111111", "AAAA")); err != nil {
		t.Fatalf("update device B to reclaim device A's SFDI and LFDI: %v", err)
	}
	if got, err := s.GetByLFDI(ctx, "AAAA"); err != nil || got.SFDI != "1111111111" {
		t.Fatalf("control: GetByLFDI(AAAA) = %+v, %v, want device B holding the reclaimed identity", got, err)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("delete device A: %v", err)
	}

	if _, err := s.GetBySFDI(ctx, "1111111111"); err != nil {
		t.Errorf("GetBySFDI(1111111111) after deleting device A = %v, want device B still resolvable by the identity it reclaimed", err)
	}
	if _, err := s.GetByLFDI(ctx, "AAAA"); err != nil {
		t.Errorf("GetByLFDI(AAAA) after deleting device A = %v, want device B still resolvable by the identity it reclaimed", err)
	}
}

func TestEndDevicePersistence_UpdateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedEndDeviceStore(t)

	if err := store.Create(ctx, "dev-A", mkDevice("dev-A", "sfdi-old", "lfdi-old")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(ctx, "dev-A", mkDevice("dev-A", "sfdi-new", "lfdi-new")); err != nil {
		t.Fatalf("Update: %v", err)
	}

	revived, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "dev-A")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.SFDI != "sfdi-new" || got.LFDI != "lfdi-new" {
		t.Errorf("revived device after Update = %+v, want sfdi-new/lfdi-new", got)
	}
	// Stale SFDI must NOT resolve.
	if _, err := revived.GetBySFDI(ctx, "sfdi-old"); err == nil {
		t.Error("stale SFDI still indexed after Update + revive")
	}
}

func TestEndDevicePersistence_CorruptJSONRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "enddevices.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := memory.NewEndDeviceStoreWithPersistence(path); err == nil {
		t.Fatal("expected error loading corrupt JSON, got nil")
	}
}

func TestEndDevicePersistence_UnsupportedVersionRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "enddevices.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"records":[]}`), 0o600); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if _, err := memory.NewEndDeviceStoreWithPersistence(path); err == nil {
		t.Fatal("expected error on unsupported version, got nil")
	}
}

func TestEndDevicePersistence_ConcurrentMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedEndDeviceStore(t)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("dev-%02d", i)
			_ = store.Create(ctx, id, mkDevice(id, fmt.Sprintf("sfdi-%02d", i), fmt.Sprintf("lfdi-%02d", i)))
		}(i)
	}
	wg.Wait()

	// All N must survive a revive.
	revived, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("dev-%02d", i)
		if _, err := revived.Get(ctx, id); err != nil {
			t.Errorf("dev %s missing after revive: %v", id, err)
		}
	}
}
