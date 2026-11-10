package memory_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// RegistrationStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165)
// pins the same contract as the EndDeviceStore wrapper. The underlying
// Store[sep2.Registration] keeps
// its existing interface: RegistrationStore is a thin wrapper that adds
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

// blockRegistrationPersist makes every snapshot write fail: a non-empty
// directory at <path>.tmp cannot be opened as a file, whichever user runs the
// test.
func blockRegistrationPersist(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
}

func unblockRegistrationPersist(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatalf("unblock persist path: %v", err)
	}
}

func TestRegistrationPersistence_FailedFlushRollsBackEveryWrite(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedRegistrationStore(t)
	if err := s.Create(ctx, "rg-1", mkRegistration(111111, 10)); err != nil {
		t.Fatal(err)
	}
	blockRegistrationPersist(t, path)

	if err := s.Create(ctx, "rg-2", mkRegistration(222222, 20)); err == nil {
		t.Error("Create with a blocked snapshot returned nil")
	}
	if _, err := s.Get(ctx, "rg-2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after a failed Create = %v, want ErrNotFound", err)
	}

	if err := s.Update(ctx, "rg-1", mkRegistration(999999, 99)); err == nil {
		t.Error("Update with a blocked snapshot returned nil")
	}
	if got, _ := s.Get(ctx, "rg-1"); got.PIN != 111111 || got.DateTimeRegistered != 10 {
		t.Errorf("after a failed Update rg-1 = %+v, want pin 111111 registered 10", got)
	}

	if _, err := s.Upsert(ctx, "rg-1", func(cur sep2.Registration, _ bool) sep2.Registration {
		cur.PIN = 5
		return cur
	}); err == nil {
		t.Error("Upsert (replace) with a blocked snapshot returned nil")
	}
	if got, _ := s.Get(ctx, "rg-1"); got.PIN != 111111 {
		t.Errorf("after a failed Upsert rg-1 pin = %d, want 111111", got.PIN)
	}
	if _, err := s.Upsert(ctx, "rg-3", func(sep2.Registration, bool) sep2.Registration {
		return mkRegistration(333333, 30)
	}); err == nil {
		t.Error("Upsert (create) with a blocked snapshot returned nil")
	}
	if _, err := s.Get(ctx, "rg-3"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after a failed Upsert create = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "rg-1"); err == nil {
		t.Error("Delete with a blocked snapshot returned nil")
	}
	if got, err := s.Get(ctx, "rg-1"); err != nil || got.PIN != 111111 || got.DateTimeRegistered != 10 {
		t.Errorf("after a failed Delete rg-1 = (%+v, %v), want the record back unchanged", got, err)
	}
	if n, _ := s.Count(ctx); n != 1 {
		t.Errorf("Count = %d, want 1", n)
	}

	// The rolled-back state is what the next successful write persists.
	unblockRegistrationPersist(t, path)
	if err := s.Create(ctx, "rg-4", mkRegistration(444444, 40)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := reloaded.Count(ctx); n != 2 {
		t.Fatalf("reloaded Count = %d, want 2 (rg-1, rg-4)", n)
	}
	for id, pin := range map[string]uint32{"rg-1": 111111, "rg-4": 444444} {
		got, err := reloaded.Get(ctx, id)
		if err != nil || got.PIN != pin {
			t.Errorf("reloaded %s = (%+v, %v), want pin %d", id, got, err, pin)
		}
	}
}

func TestRegistrationUpsert_ReportsCreatedAndKeepsServerFields(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedRegistrationStore(t)
	build := func(pin uint32) func(sep2.Registration, bool) sep2.Registration {
		return func(cur sep2.Registration, found bool) sep2.Registration {
			if !found {
				return mkRegistration(pin, 77)
			}
			cur.PIN = pin
			return cur
		}
	}
	created, err := s.Upsert(ctx, "rg-1", build(1))
	if err != nil || !created {
		t.Fatalf("first Upsert = (%v, %v), want created", created, err)
	}
	created, err = s.Upsert(ctx, "rg-1", build(2))
	if err != nil || created {
		t.Fatalf("second Upsert = (%v, %v), want replaced", created, err)
	}
	got, _ := s.Get(ctx, "rg-1")
	if got.PIN != 2 || got.DateTimeRegistered != 77 {
		t.Errorf("stored = %+v, want pin 2 and the original registration time 77", got)
	}
	reloaded, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get(ctx, "rg-1"); got.PIN != 2 || got.DateTimeRegistered != 77 {
		t.Errorf("reloaded = %+v, want pin 2 registered 77", got)
	}
}

func TestRegistrationUpsert_ConcurrentWritersAgree(t *testing.T) {
	ctx := context.Background()
	s, _ := newPersistedRegistrationStore(t)
	const writers = 16
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.Upsert(ctx, "rg-1", func(cur sep2.Registration, found bool) sep2.Registration {
				cur.PIN = uint32(i + 1)
				return cur
			})
			if err != nil {
				t.Errorf("Upsert: %v", err)
			}
			if c {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Errorf("%d writers saw created, want exactly 1", created.Load())
	}
}
