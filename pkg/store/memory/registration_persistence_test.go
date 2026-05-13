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

// IEEE-097: RegistrationStore persistence pins the same contract as the
// EndDeviceStore wrapper. The underlying Store[sep2.Registration] keeps
// its existing interface — RegistrationStore is a thin wrapper that adds
// on-disk snapshots without changing how the rest of the server interacts
// with it.

func newPersistedRegistrationStore(t *testing.T) (*memory.RegistrationStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registrations.json")
	store, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewRegistrationStoreWithPersistence: %v", err)
	}
	return store, path
}

func mkRegistration(pin uint32, registered int64) sep2.Registration {
	return sep2.Registration{
		DateTimeRegistered: registered,
		PIN:                pin,
	}
}

func TestRegistrationPersistence_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	store, err := memory.NewRegistrationStoreWithPersistence("")
	if err != nil {
		t.Fatalf("empty path: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store for empty path")
	}
	if err := store.Create(context.Background(), "rg-1", mkRegistration(123456, 0)); err != nil {
		t.Fatalf("Create on in-memory: %v", err)
	}
}

func TestRegistrationPersistence_ColdBootMissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "subdir", "registrations.json")
	if _, err := memory.NewRegistrationStoreWithPersistence(missing); err != nil {
		t.Fatalf("cold boot must tolerate missing file: %v", err)
	}
}

func TestRegistrationPersistence_CreateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedRegistrationStore(t)

	reg := mkRegistration(987654, 1234567890)
	if err := store.Create(ctx, "rg-1", reg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot file empty")
	}

	revived, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "rg-1")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.PIN != reg.PIN || got.DateTimeRegistered != reg.DateTimeRegistered {
		t.Errorf("revived = %+v, want %+v", got, reg)
	}
}

func TestRegistrationPersistence_DeleteThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedRegistrationStore(t)

	_ = store.Create(ctx, "rg-A", mkRegistration(111111, 1))
	_ = store.Create(ctx, "rg-B", mkRegistration(222222, 2))
	if err := store.Delete(ctx, "rg-A"); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	revived, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := revived.Get(ctx, "rg-A"); err == nil {
		t.Error("rg-A should be absent")
	}
	if _, err := revived.Get(ctx, "rg-B"); err != nil {
		t.Errorf("rg-B should be present: %v", err)
	}
}

func TestRegistrationPersistence_UpdateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedRegistrationStore(t)

	if err := store.Create(ctx, "rg-1", mkRegistration(111111, 100)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(ctx, "rg-1", mkRegistration(999999, 200)); err != nil {
		t.Fatalf("Update: %v", err)
	}

	revived, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "rg-1")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.PIN != 999999 || got.DateTimeRegistered != 200 {
		t.Errorf("revived after Update = %+v, want PIN=999999/dt=200", got)
	}
}

func TestRegistrationPersistence_CorruptJSONRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registrations.json")
	if err := os.WriteFile(path, []byte("{not"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := memory.NewRegistrationStoreWithPersistence(path); err == nil {
		t.Fatal("expected error on corrupt JSON")
	}
}

func TestRegistrationPersistence_ConcurrentMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedRegistrationStore(t)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("rg-%02d", i)
			_ = store.Create(ctx, id, mkRegistration(uint32(i), int64(i)))
		}(i)
	}
	wg.Wait()

	revived, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("rg-%02d", i)
		if _, err := revived.Get(ctx, id); err != nil {
			t.Errorf("%s missing: %v", id, err)
		}
	}
}
