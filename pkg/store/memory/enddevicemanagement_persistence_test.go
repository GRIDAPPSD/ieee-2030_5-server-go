package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// seedRecords writes a snapshot envelope holding records verbatim, bypassing
// every check the store's own write path enforces, the way a hand-edited or
// corrupted file would arrive on disk.
func seedRecords(t *testing.T, path string, records string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"version":1,"records":`+records+`}`), 0o600); err != nil {
		t.Fatalf("seed records: %v", err)
	}
}

// TestManagementPersistence_LoadRefusesNonCanonicalLFDI is item 2's decisive
// case for H3: the write path refuses a non-canonical LFDI with
// checkCanonicalLFDI, and the load path must refuse the same file rather
// than seat a pair that a client GETs but the gate can never match and the
// API can never remove (H3's stated symptom).
func TestManagementPersistence_LoadRefusesNonCanonicalLFDI(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	seedRecords(t, path, `[{"managerLFDI":"`+strings.ToLower(managerA)+`","managedLFDI":"`+childA+`"}]`)

	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); !errors.Is(err, store.ErrInvalidManagementPair) {
		t.Fatalf("load with a lowercase manager LFDI = %v, want ErrInvalidManagementPair", err)
	}
}

// TestManagementPersistence_LoadRefusesSelfManagement covers the other H3
// state the write path never allows: a record naming a device as its own
// manager.
func TestManagementPersistence_LoadRefusesSelfManagement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	seedRecords(t, path, `[{"managerLFDI":"`+managerA+`","managedLFDI":"`+managerA+`"}]`)

	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); err == nil {
		t.Fatal("load with a self-managing record: expected an error, got nil")
	}
}

// TestManagementPersistence_LoadRefusesDuplicateManagedDevice covers H3's
// two-managers state: two records naming the same managed device is exactly
// what Assign's one-manager-per-device rule (store.ErrAlreadyExists) refuses
// on the write path, so a file holding it is refused too rather than
// silently keeping one manager in managerOf while both survive in
// managedBy.
func TestManagementPersistence_LoadRefusesDuplicateManagedDevice(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	seedRecords(t, path,
		`[{"managerLFDI":"`+managerA+`","managedLFDI":"`+childA+`"},`+
			`{"managerLFDI":"`+managerB+`","managedLFDI":"`+childA+`"}]`)

	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); err == nil {
		t.Fatal("load with two managers for one device: expected an error, got nil")
	}
}

// TestManagementPersistence_LoadRejectionLeavesNoPartialState asserts the
// "refuse wholesale" half of item 2's decision: a file whose second record
// fails validation must not leave the store holding the first record. The
// loader is exercised directly through NewEndDeviceManagementStoreWithPersistence,
// whose only handle on success is the returned store, so this reaches for
// loadFromFile's atomic-swap contract by checking the error is the only
// thing returned - there is no live store to inspect - and pairs it with
// TestManagementPersistence_LoadRefusesDuplicateManagedDevice: read
// together, a caller that decided to carry on from a load error would
// otherwise not know whether it holds zero records or every record before
// the bad one.
func TestManagementPersistence_LoadRejectionLeavesNoPartialState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	seedRecords(t, path,
		`[{"managerLFDI":"`+managerA+`","managedLFDI":"`+childA+`"},`+
			`{"managerLFDI":"`+managerA+`","managedLFDI":"`+managerA+`"}]`)

	s, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err == nil {
		t.Fatal("load with a bad second record: expected an error, got nil")
	}
	if s != nil {
		t.Fatalf("load with a bad second record: got a non-nil store %+v, want nil", s)
	}
}

// TestManagementPersistence_WriteFailureLeavesMemoryAndDiskUnchanged is
// item 1's decisive test, reproducing H1: with the data directory
// unwritable, a create must not become visible to a reader, and a delete
// must not already have taken effect. Unlike the other five admin-mutated
// stores (#523's open class, e.g.
// TestDERProgramPersistence_UpdateFailedFlushKeepsMemoryAndStaleDisk), this
// store persists before it commits to memory, so the in-memory maps must
// read back exactly as they did before the call, not "as the mutation left
// them".
func TestManagementPersistence_WriteFailureLeavesMemoryAndDiskUnchanged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "management.json")

	s, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence: %v", err)
	}
	mustAssignT(t, s, managerA, childA)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot before the failing write: %v", err)
	}

	// atomicfile.Write opens <path>.tmp for write in dir; a read-only dir
	// makes that open fail without touching the file already committed.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	t.Run("create reported as failed does not become live", func(t *testing.T) {
		err := s.Assign(ctx, managerA, childB)
		if err == nil {
			t.Fatal("Assign into an unwritable data directory: expected an error, got nil")
		}
		if !strings.HasPrefix(err.Error(), "enddevice management persistence:") {
			t.Errorf("Assign error = %q, want the enddevice management persistence prefix", err.Error())
		}
		if _, mErr := s.ManagerOf(ctx, childB); !errors.Is(mErr, store.ErrNotFound) {
			t.Errorf("ManagerOf(childB) after a failed create = %v, want ErrNotFound: the grant must not be live", mErr)
		}
	})

	t.Run("delete reported as failed does not take effect", func(t *testing.T) {
		err := s.Unassign(ctx, childA)
		if err == nil {
			t.Fatal("Unassign from an unwritable data directory: expected an error, got nil")
		}
		if manager, mErr := s.ManagerOf(ctx, childA); mErr != nil || manager != managerA {
			t.Errorf("ManagerOf(childA) after a failed delete = %q, %v; want %q still present: the revocation must not have taken effect", manager, mErr, managerA)
		}
	})

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after the failing writes: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("snapshot changed despite both writes failing: before = %s, after = %s", before, after)
	}

	// The retry case: once the directory is writable again, a repeat of the
	// same create must actually persist rather than answer success from an
	// idempotent early-return over state that was never durable.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod dir writable: %v", err)
	}
	if err := s.Assign(ctx, managerA, childB); err != nil {
		t.Fatalf("retry Assign after the directory is writable again: %v", err)
	}
	revived, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive after retry: %v", err)
	}
	if manager, mErr := revived.ManagerOf(ctx, childB); mErr != nil || manager != managerA {
		t.Errorf("revived ManagerOf(childB) after the retry = %q, %v; want %q: the retry must have reached disk", manager, mErr, managerA)
	}
}

// TestManagementPersistence_WriteFailureBodyHasNoPath is H9: the error
// Assign/Unassign return when persistence fails wraps atomicfile's own
// error, which names the absolute temporary file path; internal/handler's
// admin_management.go must not let that reach an HTTP response body. This
// test pins the store-layer contract the handler test relies on: the error
// DOES still carry the path at this layer (an operator debugging a cold
// disk needs it), so the sanitizing has to happen at the boundary that
// serializes a response, not by scrubbing it out of the Go error here.
func TestManagementPersistence_WriteFailureBodyHasNoPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "management.json")
	s, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = s.Assign(context.Background(), managerA, childA)
	if err == nil {
		t.Fatal("Assign into an unwritable data directory: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), path+".tmp") {
		t.Fatalf("store-layer error = %q, want it to still name the tmp path %q (the handler is what must sanitize this, not the store)", err.Error(), path+".tmp")
	}
}

const (
	managerA = "AAAA000000000000000000000000000000000010"
	managerB = "AAAA000000000000000000000000000000000020"
	childA   = "C0A0000000000000000000000000000000000001"
	childB   = "C0B0000000000000000000000000000000000002"
)
