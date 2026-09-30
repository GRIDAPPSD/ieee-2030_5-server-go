package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func newPersistedDERControlStore(t *testing.T) (*memory.DERControlStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dercontrols.json")
	s, err := memory.NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}
	return s, path
}

func mkControl(mrid string, start int64) sep2.DERControl {
	dc := sep2.DERControl{}
	dc.MRID = mrid
	dc.CreationTime = start
	dc.Interval = &sep2.DateTimeInterval{Start: start, Duration: 3600}
	return dc
}

func TestDERControlPersistence_EmptyPathIsInMemory(t *testing.T) {
	s, err := memory.NewDERControlStoreWithPersistence("")
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence(\"\") error = %v", err)
	}
	if s.Persists() {
		t.Fatalf("Persists() = true, want false for an empty path")
	}
	ctx := context.Background()
	if err := s.Create(ctx, "edev/0/derp/0", "c1", mkControl("AA", 100)); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestDERControlPersistence_ColdBootMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	s, err := memory.NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("cold boot on missing file: %v", err)
	}
	if !s.Persists() {
		t.Fatalf("Persists() = false, want true once a path is configured")
	}
}

// TestDERControlPersistence_CreateThenReload covers acceptance criterion 1's
// disk half directly: create, reload from the same path into a fresh store,
// and assert the reloaded record's field values, not merely that reload
// succeeded (per data-invariants.md).
func TestDERControlPersistence_CreateThenReload(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	want := mkControl("AABBCCDD", 500)
	if err := s.Create(ctx, "0/0/0", "c1", want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revived, err := memory.NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := revived.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.MRID != want.MRID {
		t.Errorf("reloaded MRID = %q, want %q", got.MRID, want.MRID)
	}
	if got.Interval == nil || got.Interval.Start != want.Interval.Start || got.Interval.Duration != want.Interval.Duration {
		t.Errorf("reloaded Interval = %+v, want %+v", got.Interval, want.Interval)
	}
}

// TestDERControlPersistence_ByMRIDAfterReload is acceptance criterion 5: the
// mRID-to-scope index answers correctly for a control found only after a
// fresh store rebuilds it from the reloaded records.
func TestDERControlPersistence_ByMRIDAfterReload(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	c1 := mkControl("MRID-ONE", 100)
	c2 := mkControl("MRID-TWO", 200)
	if err := s.Create(ctx, "0/0/0", "c1", c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := s.Create(ctx, "0/0/1", "c2", c2); err != nil {
		t.Fatalf("Create c2: %v", err)
	}

	revived, err := memory.NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	parentID, id, got, err := revived.ByMRID(ctx, "MRID-TWO")
	if err != nil {
		t.Fatalf("ByMRID after reload: %v", err)
	}
	if parentID != "0/0/1" || id != "c2" {
		t.Errorf("ByMRID = (%q, %q), want (\"0/0/1\", \"c2\")", parentID, id)
	}
	if got.MRID != "MRID-TWO" {
		t.Errorf("ByMRID control.MRID = %q, want MRID-TWO", got.MRID)
	}
	if _, _, _, err := revived.ByMRID(ctx, "NOPE"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ByMRID(unknown) error = %v, want ErrNotFound", err)
	}
}

// blockPersist creates a non-empty directory at <path>.tmp so
// atomicfile.Write's os.OpenFile fails with EISDIR regardless of which user
// runs the test: chmod-based unwritability is bypassed by root, but no user
// can open a directory as a regular file for writing. Mirrors
// atomicfile_test.go's own TestWriteFailureLeavesCommittedFile.
func blockPersist(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
}

// TestDERControlPersistence_CreateRollsBackOnPersistFailure is acceptance
// criterion 4: a failed snapshot write is returned as an error, and the
// in-memory state does not report a write that did not persist.
func TestDERControlPersistence_CreateRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	blockPersist(t, path)

	ctrl := mkControl("BLOCKED", 100)
	if err := s.Create(ctx, "0/0/0", "c1", ctrl); err == nil {
		t.Fatal("Create with a blocked snapshot path returned nil error, want the persist failure")
	}
	if _, err := s.Get(ctx, "0/0/0", "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after a failed Create = %v, want ErrNotFound (the write must not be reported as having persisted)", err)
	}
	if _, _, _, err := s.ByMRID(ctx, "BLOCKED"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ByMRID after a failed Create = %v, want ErrNotFound", err)
	}
	if n, err := s.Count(ctx, "0/0/0"); err != nil || n != 0 {
		t.Errorf("Count after a failed Create = (%d, %v), want (0, nil)", n, err)
	}
}

// TestDERControlPersistence_UpdateRollsBackOnPersistFailure is
// GRIDAPPSD/ieee-2030_5-server-go#565 fix round 1, item 2: the sibling
// Create and Delete rollback tests above had no Update counterpart, so a
// mutant that dropped Update's rollback (dercontrol_persistence.go's
// restore of `before` and of the mRID index) went uncaught. This asserts
// both the field value and the mRID index revert to the pre-Update state.
func TestDERControlPersistence_UpdateRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	original := mkControl("ORIGINAL", 100)
	if err := s.Create(ctx, "0/0/0", "c1", original); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	blockPersist(t, path)
	changed := mkControl("CHANGED", 200)
	if err := s.Update(ctx, "0/0/0", "c1", changed); err == nil {
		t.Fatal("Update with a blocked snapshot path returned nil error, want the persist failure")
	}
	got, err := s.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after a failed Update: %v, want the prior value restored", err)
	}
	if got.MRID != "ORIGINAL" {
		t.Errorf("control after a failed Update has MRID = %q, want the restored ORIGINAL", got.MRID)
	}
	if _, _, _, err := s.ByMRID(ctx, "ORIGINAL"); err != nil {
		t.Errorf("ByMRID(ORIGINAL) after a failed Update: %v, want the index restored to the prior mRID", err)
	}
	if _, _, _, err := s.ByMRID(ctx, "CHANGED"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ByMRID(CHANGED) after a failed Update = %v, want ErrNotFound: the rejected mRID must not be indexed", err)
	}
}

func TestDERControlPersistence_DeleteRollsBackOnPersistFailure(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	ctrl := mkControl("KEEP", 100)
	if err := s.Create(ctx, "0/0/0", "c1", ctrl); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	blockPersist(t, path)
	if err := s.Delete(ctx, "0/0/0", "c1"); err == nil {
		t.Fatal("Delete with a blocked snapshot path returned nil error, want the persist failure")
	}
	got, err := s.Get(ctx, "0/0/0", "c1")
	if err != nil {
		t.Fatalf("Get after a failed Delete: %v, want the record restored", err)
	}
	if got.MRID != "KEEP" {
		t.Errorf("restored control MRID = %q, want KEEP", got.MRID)
	}
	if _, _, _, err := s.ByMRID(ctx, "KEEP"); err != nil {
		t.Errorf("ByMRID after a failed Delete: %v, want the index restored", err)
	}
}

// TestDERControlPersistence_Persists is acceptance criterion 2: the caller
// can ask whether the store persists.
func TestDERControlPersistence_Persists(t *testing.T) {
	mem := memory.NewDERControlStore()
	if mem.Persists() {
		t.Errorf("in-memory store Persists() = true, want false")
	}
	persisted, _ := newPersistedDERControlStore(t)
	if !persisted.Persists() {
		t.Errorf("persisted store Persists() = false, want true")
	}
}
