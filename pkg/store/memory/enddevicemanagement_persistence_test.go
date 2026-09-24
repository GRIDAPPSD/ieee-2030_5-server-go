package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// EndDeviceManagementStore persistence (GRIDAPPSD/ieee-2030_5-server-go#440):
// same contract as the other five admin-mutated stores (#165). Each test
// builds a fresh store with the persistence path wired to a file inside
// t.TempDir(), mutates it, and verifies a store revived from disk sees the
// same pairs.

func newPersistedManagementStore(t *testing.T) (*memory.EndDeviceManagementStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "management.json")
	s, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence: %v", err)
	}
	return s, path
}

func TestManagementPersistence_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	s, err := memory.NewEndDeviceManagementStoreWithPersistence("")
	if err != nil {
		t.Fatalf("empty path should be no-op: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store for empty path (in-memory)")
	}
	if err := s.Assign(context.Background(), managerA, childA); err != nil {
		t.Fatalf("Assign on in-memory: %v", err)
	}
}

func TestManagementPersistence_ColdBootMissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "subdir", "management.json")
	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(missing); err != nil {
		t.Fatalf("cold boot must tolerate missing file: %v", err)
	}
}

// TestManagementPersistence_AssignThenReload is the data-invariant case:
// the revived store's fields (both directions of the pair) must match the
// values written, not merely fail to crash.
func TestManagementPersistence_AssignThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newPersistedManagementStore(t)

	if err := s.Assign(ctx, managerA, childA); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := s.Assign(ctx, managerA, childB); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot file is empty after Assign")
	}

	revived, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if manager, err := revived.ManagerOf(ctx, childA); err != nil || manager != managerA {
		t.Errorf("revived ManagerOf(childA) = %q, %v; want %q", manager, err, managerA)
	}
	if manager, err := revived.ManagerOf(ctx, childB); err != nil || manager != managerA {
		t.Errorf("revived ManagerOf(childB) = %q, %v; want %q", manager, err, managerA)
	}
	got, err := revived.ManagedBy(ctx, managerA)
	if err != nil || !slices.Equal(got, []string{childA, childB}) {
		t.Errorf("revived ManagedBy(managerA) = %v, %v; want [%s %s]", got, err, childA, childB)
	}
}

func TestManagementPersistence_UnassignThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newPersistedManagementStore(t)

	mustAssignT(t, s, managerA, childA)
	mustAssignT(t, s, managerA, childB)
	if err := s.Unassign(ctx, childA); err != nil {
		t.Fatalf("Unassign: %v", err)
	}

	revived, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := revived.ManagerOf(ctx, childA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revived ManagerOf(unassigned childA) = %v, want ErrNotFound", err)
	}
	if manager, err := revived.ManagerOf(ctx, childB); err != nil || manager != managerA {
		t.Errorf("revived ManagerOf(childB) = %q, %v; want %q still present", manager, err, managerA)
	}
}

func TestManagementPersistence_RekeyThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newPersistedManagementStore(t)

	mustAssignT(t, s, managerA, childA)
	if err := s.RekeyManager(ctx, managerA, managerB); err != nil {
		t.Fatalf("RekeyManager: %v", err)
	}

	revived, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if manager, err := revived.ManagerOf(ctx, childA); err != nil || manager != managerB {
		t.Errorf("revived ManagerOf(childA) after rekey+reload = %q, %v; want %q", manager, err, managerB)
	}
	if got, _ := revived.ManagedBy(ctx, managerA); len(got) != 0 {
		t.Errorf("revived ManagedBy(retired manager) = %v, want empty", got)
	}
}

func TestManagementPersistence_CorruptJSONRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); err == nil {
		t.Fatal("expected error loading corrupt JSON, got nil")
	}
}

func TestManagementPersistence_UnsupportedVersionRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"records":[]}`), 0o600); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); err == nil {
		t.Fatal("expected error on unsupported version, got nil")
	}
}

const (
	managerA = "AAAA000000000000000000000000000000000010"
	managerB = "AAAA000000000000000000000000000000000020"
	childA   = "C0A0000000000000000000000000000000000001"
	childB   = "C0B0000000000000000000000000000000000002"
)
