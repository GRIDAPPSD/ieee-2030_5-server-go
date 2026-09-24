package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
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

// TestManagementPersistence_LoadRefusesNonHex40LFDI is item 5's decisive
// case: checkCanonicalLFDI checked case and trimming but not length, so a
// short value that is already upper case and trimmed passed it. normalizeLFDI
// on the API path refuses the same value with validLFDI's 40-hex-digit rule,
// so the two paths must agree or a seeded short value loads, is listed, and
// answers 400 to both delete and re-key: visible and unremovable.
func TestManagementPersistence_LoadRefusesNonHex40LFDI(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "management.json")
	seedRecords(t, path, `[{"managerLFDI":"`+managerA+`","managedLFDI":"ZZZZ"}]`)

	if _, err := memory.NewEndDeviceManagementStoreWithPersistence(path); !errors.Is(err, store.ErrInvalidManagementPair) {
		t.Fatalf("load with a 4-character managed LFDI = %v, want ErrInvalidManagementPair", err)
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

	// Item 2: the rekey pair persists before mutating too, the same as
	// create and remove above. A failed write after the in-memory mutation
	// would leave the rotated fleet live in memory while disk still names
	// the retired manager, so the next boot hands the fleet back to it.
	t.Run("rekey manager reported as failed does not take effect", func(t *testing.T) {
		err := s.RekeyManager(ctx, managerA, managerB)
		if err == nil {
			t.Fatal("RekeyManager into an unwritable data directory: expected an error, got nil")
		}
		if manager, mErr := s.ManagerOf(ctx, childA); mErr != nil || manager != managerA {
			t.Errorf("ManagerOf(childA) after a failed rekey manager = %q, %v; want %q still present: the rotation must not have taken effect", manager, mErr, managerA)
		}
	})

	t.Run("rekey managed reported as failed does not take effect", func(t *testing.T) {
		err := s.RekeyManaged(ctx, childA, childB)
		if err == nil {
			t.Fatal("RekeyManaged into an unwritable data directory: expected an error, got nil")
		}
		if manager, mErr := s.ManagerOf(ctx, childA); mErr != nil || manager != managerA {
			t.Errorf("ManagerOf(childA) after a failed rekey managed = %q, %v; want %q still present: the rotation must not have taken effect", manager, mErr, managerA)
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

// onDiskRecords decodes the snapshot envelope at path into the raw
// (manager, managed) pairs as written, independent of any store type, so the
// test can assert on-disk byte order rather than trusting a decoder that
// might re-sort.
func onDiskRecords(t *testing.T, path string) []struct {
	ManagerLFDI string `json:"managerLFDI"`
	ManagedLFDI string `json:"managedLFDI"`
} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var env struct {
		Records []struct {
			ManagerLFDI string `json:"managerLFDI"`
			ManagedLFDI string `json:"managedLFDI"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return env.Records
}

// TestManagementPersistence_OnDiskOrderIsSorted is item 6's second small
// truth: recordsLocked's comment promises a sorted-by-managed-LFDI on-disk
// order, but Assign appends its new record after the sort and RekeyManaged
// rewrites a record's sort key in place, so the file was unsorted after
// either. childC sorts before childA and childB, and the rekey below moves
// childB's sort key past childC, exercising both cases the comment claimed
// were covered.
func TestManagementPersistence_OnDiskOrderIsSorted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newPersistedManagementStore(t)

	mustAssignT(t, s, managerA, childA)
	mustAssignT(t, s, managerA, childB)
	// childC sorts before both: an append-after-sort would leave it last.
	mustAssignT(t, s, managerA, childC)

	assertOnDiskSorted(t, path)

	// Rekey childB to childD, which sorts after childC: rewriting the sort
	// key in place without re-sorting would leave the record out of order.
	if err := s.RekeyManaged(ctx, childB, childD); err != nil {
		t.Fatalf("RekeyManaged: %v", err)
	}
	assertOnDiskSorted(t, path)
}

func assertOnDiskSorted(t *testing.T, path string) {
	t.Helper()
	records := onDiskRecords(t, path)
	got := make([]string, len(records))
	for i, r := range records {
		got[i] = r.ManagedLFDI
	}
	want := slices.Clone(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("on-disk managed-LFDI order = %v, want sorted %v", got, want)
	}
}

// TestManagementPersistence_ConcurrentAssignsAgreeWithDisk is risk area 2:
// item 1's persistMu serializes every mutating call for its whole
// operation, so two concurrent writers must never interleave and leave the
// file disagreeing with memory. N goroutines each assign a distinct managed
// LFDI to the same manager concurrently; every one must succeed (no writer
// can have observed a stale precondition from another writer's half-applied
// change), and the revived store's pairs must match the live store's
// exactly, proving the file and memory agree after concurrent writers.
func TestManagementPersistence_ConcurrentAssignsAgreeWithDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newPersistedManagementStore(t)

	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			managed := fmt.Sprintf("C0C0%036X", i)
			errs[i] = s.Assign(ctx, managerA, managed)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Assign %d: %v", i, err)
		}
	}

	liveManaged, err := s.ManagedBy(ctx, managerA)
	if err != nil {
		t.Fatalf("ManagedBy after concurrent assigns: %v", err)
	}
	if len(liveManaged) != n {
		t.Fatalf("ManagedBy after concurrent assigns = %d entries, want %d", len(liveManaged), n)
	}

	revived, err := memory.NewEndDeviceManagementStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive after concurrent assigns: %v", err)
	}
	revivedManaged, err := revived.ManagedBy(ctx, managerA)
	if err != nil {
		t.Fatalf("revived ManagedBy: %v", err)
	}
	sort.Strings(liveManaged)
	sort.Strings(revivedManaged)
	if !slices.Equal(liveManaged, revivedManaged) {
		t.Errorf("revived ManagedBy = %v, want it to match the live store %v: disk and memory disagree after concurrent writers", revivedManaged, liveManaged)
	}
}

const (
	managerA = "AAAA000000000000000000000000000000000010"
	managerB = "AAAA000000000000000000000000000000000020"
	childA   = "C0A0000000000000000000000000000000000001"
	childB   = "C0B0000000000000000000000000000000000002"
	// childC and childD sort before childA (an append-after-sort in Assign
	// would leave childC last) and between childC and childA (an in-place
	// sort-key rewrite in RekeyManaged would leave childD wherever childB
	// used to sit, not where childD belongs).
	childC = "C000000000000000000000000000000000000003"
	childD = "C005000000000000000000000000000000000004"
)
