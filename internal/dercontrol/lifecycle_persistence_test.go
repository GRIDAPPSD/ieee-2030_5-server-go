package dercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

func newPersistedLifecycleStore(t *testing.T) (*LifecycleStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}
	return s, path
}

func TestLifecyclePersistence_EmptyPathIsInMemory(t *testing.T) {
	s, err := NewLifecycleStoreWithPersistence("")
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence(\"\") error = %v", err)
	}
	if s.Persists() {
		t.Errorf("Persists() = true, want false for an empty path")
	}
}

func TestLifecyclePersistence_ColdBootMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("cold boot on missing file: %v", err)
	}
	if !s.Persists() {
		t.Errorf("Persists() = false, want true once a path is configured")
	}
}

// TestLifecyclePersistence_CreateThenReload asserts field values on the
// reloaded record, not just that reload succeeded (data-invariants.md).
func TestLifecyclePersistence_CreateThenReload(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	cancelledAt := int64(1000)
	want := LifecycleRecord{CancelledAt: &cancelledAt, CancelReason: "operator request"}
	if err := s.Create(ctx, "0/0/0", "c1", want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revived, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := revived.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.CancelledAt == nil || *got.CancelledAt != cancelledAt {
		t.Errorf("reloaded CancelledAt = %v, want %d", got.CancelledAt, cancelledAt)
	}
	if got.CancelReason != want.CancelReason {
		t.Errorf("reloaded CancelReason = %q, want %q", got.CancelReason, want.CancelReason)
	}
	if !got.cancelled() {
		t.Errorf("reloaded record's derived status cancelled() = false, want true")
	}
}

func blockLifecyclePersist(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
}

// TestLifecyclePersistence_CreateRollsBackOnPersistFailure is acceptance
// criterion 4 for the lifecycle store.
func TestLifecyclePersistence_CreateRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	blockLifecyclePersist(t, path)

	if err := s.Create(ctx, "0/0/0", "c1", LifecycleRecord{}); err == nil {
		t.Fatal("Create with a blocked snapshot path returned nil error, want the persist failure")
	}
	if _, err := s.Get(ctx, "0/0/0", "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after a failed Create = %v, want ErrNotFound", err)
	}
}

func TestLifecyclePersistence_UpdateRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, "0/0/0", "c1", LifecycleRecord{}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	blockLifecyclePersist(t, path)
	cancelledAt := int64(500)
	if err := s.Update(ctx, "0/0/0", "c1", LifecycleRecord{CancelledAt: &cancelledAt}); err == nil {
		t.Fatal("Update with a blocked snapshot path returned nil error, want the persist failure")
	}
	got, err := s.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after a failed Update: %v", err)
	}
	if got.cancelled() {
		t.Errorf("record after a failed Update reports cancelled(), want the prior (uncancelled) value restored")
	}
}

// TestLifecyclePersistence_DeleteRollsBackOnPersistFailure is
// GRIDAPPSD/ieee-2030_5-server-go#565 fix round 1, item 2: Create and
// Update above had rollback tests; Delete did not, so a mutant that
// dropped Delete's restore-on-persist-failure went uncaught.
func TestLifecyclePersistence_DeleteRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	cancelledAt := int64(700)
	seeded := LifecycleRecord{CancelledAt: &cancelledAt, CancelReason: "keep me"}
	if err := s.Create(ctx, "0/0/0", "c1", seeded); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	blockLifecyclePersist(t, path)
	if err := s.Delete(ctx, "0/0/0", "c1"); err == nil {
		t.Fatal("Delete with a blocked snapshot path returned nil error, want the persist failure")
	}
	got, err := s.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after a failed Delete: %v, want the record restored", err)
	}
	if got.CancelReason != "keep me" || got.CancelledAt == nil || *got.CancelledAt != cancelledAt {
		t.Errorf("record after a failed Delete = %+v, want the seeded value restored", got)
	}
}

func TestLifecyclePersistence_Persists(t *testing.T) {
	mem := NewLifecycleStore()
	if mem.Persists() {
		t.Errorf("in-memory store Persists() = true, want false")
	}
	persisted, _ := newPersistedLifecycleStore(t)
	if !persisted.Persists() {
		t.Errorf("persisted store Persists() = false, want true")
	}
}
