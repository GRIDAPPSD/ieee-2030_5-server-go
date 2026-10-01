package dercontrol

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestLifecyclePersistence_CorruptFileFailsToLoad is
// GRIDAPPSD/ieee-2030_5-server-go#565 round 2 item 3: readLifecycleEnvelope's
// json.Unmarshal failure branch had no test. Asserts the error names the
// decode step, not just that some error came back, so a mutant that
// swallowed the error or mixed it up with the version-mismatch branch
// would still be caught.
func TestLifecyclePersistence_CorruptFileFailsToLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	_, err := NewLifecycleStoreWithPersistence(path)
	if err == nil {
		t.Fatal("NewLifecycleStoreWithPersistence over a corrupt file returned nil error, want a decode failure")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %q, want it to name the decode step", err)
	}
}

// TestLifecyclePersistence_EmptyFileIsColdBoot is round 2 item 3: an empty
// file (readLifecycleEnvelope's len(data)==0 branch) is a valid "no
// records" state, distinct from a missing file, and had no test of its
// own separate from ColdBootMissingFile.
func TestLifecyclePersistence_EmptyFileIsColdBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence over an empty file: %v", err)
	}
	if !s.Persists() {
		t.Error("Persists() = false, want true: an empty file still configures a path")
	}
	n, err := s.Count(context.Background(), "0/0/0")
	if err != nil || n != 0 {
		t.Errorf("Count after an empty-file boot = (%d, %v), want (0, nil)", n, err)
	}
}

// TestLifecyclePersistence_VersionMismatchFailsToLoad is round 2 item 3:
// readLifecycleEnvelope's version-mismatch branch had no test. Asserts the
// error names the version step, distinguishing it from the decode-failure
// branch above.
func TestLifecyclePersistence_VersionMismatchFailsToLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"records":[]}`), 0o600); err != nil {
		t.Fatalf("write version-mismatch file: %v", err)
	}
	_, err := NewLifecycleStoreWithPersistence(path)
	if err == nil {
		t.Fatal("NewLifecycleStoreWithPersistence over a version-mismatch file returned nil error, want a version failure")
	}
	if !strings.Contains(err.Error(), "unsupported snapshot version") {
		t.Errorf("error = %q, want it to name the version mismatch", err)
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

// TestLifecyclePersistence_DeleteRollbackSurvivesNextPersistAndReload is
// round 3 item 2: the test above reads back through Get, which reads the
// in-memory collection directly and never consults the key index, so it
// cannot tell a restored key index apart from a dropped one.
// snapshotRecords walks the key index, not the collection, so a record
// whose key entry is not restored is silently missing from the NEXT
// successful snapshot and lost after a restart, even though Get would
// still find it in the meantime. This forces a second, unrelated,
// successful write (the moment that actually loses the data) and reloads
// from disk.
func TestLifecyclePersistence_DeleteRollbackSurvivesNextPersistAndReload(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	cancelledAt := int64(700)
	kept := LifecycleRecord{CancelledAt: &cancelledAt, CancelReason: "keep me"}
	if err := s.Create(ctx, "0/0/0", "c1", kept); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	blockLifecyclePersist(t, path)
	if err := s.Delete(ctx, "0/0/0", "c1"); err == nil {
		t.Fatal("Delete with a blocked snapshot path returned nil error, want the persist failure")
	}
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatalf("unblock persist path: %v", err)
	}

	if err := s.Create(ctx, "0/0/1", "c2", LifecycleRecord{}); err != nil {
		t.Fatalf("Create c2 (the write that forces a real snapshot): %v", err)
	}

	revived, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := revived.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("reloaded c1 (whose Delete was rolled back): %v, want it still present", err)
	}
	if got.CancelReason != "keep me" || got.CancelledAt == nil || *got.CancelledAt != cancelledAt {
		t.Errorf("reloaded c1 = %+v, want the seeded value", got)
	}
	if _, err := revived.Get(ctx, "0/0/1", "c2"); err != nil {
		t.Errorf("reloaded c2: %v, want it present too", err)
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

func seedLifecycles(t *testing.T, s *LifecycleStore) map[string]LifecycleRecord {
	t.Helper()
	at := int64(777)
	want := map[string]LifecycleRecord{
		"a": {CancelledAt: &at, CancelReason: "operator cancel"},
		"b": {GrantMRID: "G-B", FleetKey: "fleet", Reach: 3},
	}
	for id, rec := range want {
		if err := s.Create(context.Background(), "dev", id, rec); err != nil {
			t.Fatal(err)
		}
	}
	return want
}

func breakSnapshot(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// DeleteParent removes the whole parent in one snapshot and the removal
// survives a reload.
func TestLifecycleStore_DeleteParentPersists(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedLifecycleStore(t)
	seedLifecycles(t, s)
	if err := s.Create(ctx, "other", "x", LifecycleRecord{Reach: 1}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteParent(ctx, "dev")
	if err != nil || n != 2 {
		t.Fatalf("DeleteParent = %d, %v, want 2, nil", n, err)
	}
	re, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := re.Count(ctx, "dev"); c != 0 {
		t.Errorf("records under dev after reload = %d, want 0", c)
	}
	if got, err := re.Get(ctx, "other", "x"); err != nil || got.Reach != 1 {
		t.Errorf("unrelated parent after reload = %+v, %v, want Reach 1", got, err)
	}
}

// A failed snapshot restores every record and the parent, with each field
// unchanged. The key index is pinned by the test that follows.
func TestLifecycleStore_DeleteParentRollsBackOnAFailedSnapshot(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedLifecycleStore(t)
	want := seedLifecycles(t, s)
	breakSnapshot(t, path)
	if n, err := s.DeleteParent(ctx, "dev"); err == nil || n != 0 {
		t.Fatalf("DeleteParent = %d, %v, want 0 and an error", n, err)
	}
	for id, rec := range want {
		if got, err := s.Get(ctx, "dev", id); err != nil || !reflect.DeepEqual(got, rec) {
			t.Errorf("record %s after rollback = %+v, %v, want %+v", id, got, err, rec)
		}
	}
	if has, _ := s.HasParent(ctx, "dev"); !has {
		t.Error("parent lost after rollback")
	}
}

// TakeParent's undo recreates the records under their store keys, field for
// field, and a later snapshot still includes them.
func TestLifecycleStore_TakeParentUndoRestores(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedLifecycleStore(t)
	want := seedLifecycles(t, s)
	undo, removed, err := s.TakeParent(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("TakeParent removed = %d, want 2", removed)
	}
	if c, _ := s.Count(ctx, "dev"); c != 0 {
		t.Fatalf("TakeParent left %d records", c)
	}
	if err := undo(ctx); err != nil {
		t.Fatalf("undo: %v", err)
	}
	re, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, rec := range want {
		if got, err := re.Get(ctx, "dev", id); err != nil || !reflect.DeepEqual(got, rec) {
			t.Errorf("record %s after undo and reload = %+v, %v, want %+v", id, got, err, rec)
		}
	}
}

func TestLifecycleStore_TakeParentUndoLogsWhatItCouldNotRestore(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedLifecycleStore(t)
	seedLifecycles(t, s)
	undo, _, err := s.TakeParent(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	breakSnapshot(t, path)
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := undo(ctx); err == nil {
		t.Fatal("undo succeeded although the snapshot cannot be written")
	}
	for _, id := range []string{"dev/a", "dev/b"} {
		if !strings.Contains(buf.String(), id) {
			t.Errorf("log %q does not name the lost record %s", buf.String(), id)
		}
	}
}

// Delete, recreate under the same parent and key, delete again: a stale key
// index would make the second delete read an id that is no longer there.
func TestLifecycleStore_DeleteParentThenRecreateThenDeleteAgain(t *testing.T) {
	ctx := context.Background()
	s, _ := newPersistedLifecycleStore(t)
	seedLifecycles(t, s)
	if n, err := s.DeleteParent(ctx, "dev"); err != nil || n != 2 {
		t.Fatalf("first DeleteParent = %d, %v, want 2, nil", n, err)
	}
	if err := s.Create(ctx, "dev", "a", LifecycleRecord{Reach: 9}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteParent(ctx, "dev")
	if err != nil || n != 1 {
		t.Fatalf("second DeleteParent = %d, %v, want 1, nil", n, err)
	}
	if c, _ := s.Count(ctx, "dev"); c != 0 {
		t.Errorf("records left = %d, want 0", c)
	}
}

// The rollback of a failed DeleteParent re-indexes the restored records: a
// later snapshot, written for another parent once the file is writable again,
// is built from that index and must still hold them.
func TestLifecycleStore_DeleteParentRollbackKeepsTheRestoredRecordsInLaterSnapshots(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistedLifecycleStore(t)
	want := seedLifecycles(t, s)
	breakSnapshot(t, path)
	if n, err := s.DeleteParent(ctx, "dev"); err == nil || n != 0 {
		t.Fatalf("DeleteParent = %d, %v, want 0 and an error", n, err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "other", "x", LifecycleRecord{Reach: 1}); err != nil {
		t.Fatalf("write under another parent after the repair: %v", err)
	}

	re, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, rec := range want {
		if got, err := re.Get(ctx, "dev", id); err != nil || !reflect.DeepEqual(got, rec) {
			t.Errorf("record %s after reload = %+v, %v, want %+v: the rollback left it out of the index", id, got, err, rec)
		}
	}
}
