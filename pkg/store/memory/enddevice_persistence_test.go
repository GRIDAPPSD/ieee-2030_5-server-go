package memory_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-097: EndDeviceStore persistence — pin the same contract IEEE-077
// introduced for SubscriptionStore. Each test creates a fresh store with
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
	// Pointing at a path that does not exist must NOT error — that is
	// the normal first-boot shape.
	missing := filepath.Join(t.TempDir(), "subdir", "enddevices.json")
	// Parent dir doesn't exist yet — cold-boot reads do not create it.
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
