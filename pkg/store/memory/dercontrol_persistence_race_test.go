package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// TestDERControlStore_ConcurrentWriterCannotObserveInFlightMutation is
// GRIDAPPSD/ieee-2030_5-server-go#565 fix round 1, item 1: a deterministic
// reproduction of the race writeMu's doc comment describes, using the
// afterMutateBeforePersist test hook to force the exact interleaving
// rather than hoping for it under real scheduling.
//
// Writer A (Create X) is paused by the hook after mutating X in memory but
// before its own persist attempt. While paused, writer B (Create Y) is
// launched. Under the pre-fix code (a lock held only around the snapshot
// write, not the whole call), B runs to completion immediately: its
// snapshot covers the WHOLE store, including A's in-flight, not-yet-
// decided X, and writes both to disk. The path is then sabotaged so A's
// own persist fails and A rolls X back. Reopening the store from disk
// then finds X present even though Create(X) reported failure to its
// caller: a control an admin route was told was never stored is served
// after a restart, and it has no lifecycle record, so Cancel refuses it.
//
// Under the fixed code B blocks on writeMu until A's whole call (mutate,
// persist, rollback) has returned, so B's own persist never runs while X
// is in-flight; by the time B runs, A has already rolled X back, and B's
// persist (against the same now-sabotaged path) fails too. Reopening then
// finds X absent, matching what Create(X)'s error promised.
func TestDERControlStore_ConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrols.json")
	s, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}

	mutated := make(chan struct{})
	proceed := make(chan struct{})
	// The hook fires for every writer, so a CAS flag confines the pause to
	// A's call: B must run the hook too, once it gets writeMu (or, on the
	// pre-fix code, concurrently with A), and must pass straight through
	// without blocking. sync.Once cannot be used for this: Once.Do blocks
	// a second, concurrent caller until the first caller's function
	// returns, which would serialize A and B all by itself and make the
	// test unable to tell the fixed code from the pre-fix one.
	var paused atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}

	ctrlX := sep2.DERControl{}
	ctrlX.MRID = "RACE-X"
	errA := make(chan error, 1)
	go func() { errA <- s.Create(context.Background(), "0/0/0", "X", ctrlX) }()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	ctrlY := sep2.DERControl{}
	ctrlY.MRID = "RACE-Y"
	errB := make(chan error, 1)
	go func() { errB <- s.Create(context.Background(), "0/0/1", "Y", ctrlY) }()

	// Give B a bounded window to run to completion while A is still
	// paused. On the pre-fix code B is unblocked and finishes well within
	// this window; on the fixed code B is genuinely blocked on writeMu and
	// this always times out (not a timing coincidence: nothing releases
	// writeMu until proceed is closed below).
	var bErr error
	bDone := false
	select {
	case bErr = <-errB:
		bDone = true
		if bErr != nil {
			t.Fatalf("writer B failed before the path was sabotaged: %v", bErr)
		}
		t.Log("writer B completed while A was still paused (the pre-fix interleaving)")
	case <-time.After(200 * time.Millisecond):
		t.Log("writer B still blocked after 200ms (the fixed interleaving)")
	}

	// Sabotage the path so a persist attempted from here on fails: a
	// non-empty directory at <path>.tmp makes atomicfile.Write's
	// os.OpenFile fail regardless of which user runs the test (chmod-based
	// unwritability is bypassed by root; a directory cannot be opened as a
	// regular file for writing by anyone). Mirrors atomicfile_test.go's own
	// TestWriteFailureLeavesCommittedFile.
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
	close(proceed)

	if err := <-errA; err == nil {
		t.Fatal("writer A's Create succeeded despite the sabotaged path, want an error")
	}
	if !bDone {
		select {
		case bErr = <-errB:
		case <-time.After(5 * time.Second):
			t.Fatal("writer B never completed after A released writeMu")
		}
	}
	t.Logf("writer B's Create result: %v", bErr)

	// The decisive assertion: reopen the store from disk, simulating a
	// restart, and check whether X, reported as a failed Create, is
	// nonetheless durably stored.
	reopened, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := reopened.Get(context.Background(), "0/0/0", "X"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("control X present on disk after a restart, though Create(X) reported failure: %v", err)
	}
}

// TestDERControlStore_UpdateConcurrentWriterCannotObserveInFlightMutation
// is GRIDAPPSD/ieee-2030_5-server-go#565 round 3 item 3: the Create test
// above proves writeMu covers Create; this proves it covers Update the
// same way. X and Y are seeded before the hook is installed, so the seed
// writes are not paused. Writer A updates X's MRID; writer B updates Y
// concurrently. The decisive check is X's MRID after a restart: it must
// still be the pre-Update value, since Update(X) reported failure.
func TestDERControlStore_UpdateConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrols.json")
	s, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}
	ctx := context.Background()
	xBefore := sep2.DERControl{}
	xBefore.MRID = "X-BEFORE"
	if err := s.Create(ctx, "0/0/0", "X", xBefore); err != nil {
		t.Fatalf("seed X: %v", err)
	}
	yBefore := sep2.DERControl{}
	yBefore.MRID = "Y-BEFORE"
	if err := s.Create(ctx, "0/0/1", "Y", yBefore); err != nil {
		t.Fatalf("seed Y: %v", err)
	}

	mutated := make(chan struct{})
	proceed := make(chan struct{})
	var paused atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}

	xAfter := sep2.DERControl{}
	xAfter.MRID = "X-AFTER"
	errA := make(chan error, 1)
	go func() { errA <- s.Update(context.Background(), "0/0/0", "X", xAfter) }()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	yAfter := sep2.DERControl{}
	yAfter.MRID = "Y-AFTER"
	errB := make(chan error, 1)
	go func() { errB <- s.Update(context.Background(), "0/0/1", "Y", yAfter) }()

	var bErr error
	bDone := false
	select {
	case bErr = <-errB:
		bDone = true
		if bErr != nil {
			t.Fatalf("writer B failed before the path was sabotaged: %v", bErr)
		}
		t.Log("writer B completed while A was still paused (the pre-fix interleaving)")
	case <-time.After(200 * time.Millisecond):
		t.Log("writer B still blocked after 200ms (the fixed interleaving)")
	}

	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
	close(proceed)

	if err := <-errA; err == nil {
		t.Fatal("writer A's Update succeeded despite the sabotaged path, want an error")
	}
	if !bDone {
		select {
		case bErr = <-errB:
		case <-time.After(5 * time.Second):
			t.Fatal("writer B never completed after A released writeMu")
		}
	}
	t.Logf("writer B's Update result: %v", bErr)

	reopened, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(context.Background(), "0/0/0", "X")
	if err != nil {
		t.Fatalf("reopened X: %v, want it present with its pre-Update value", err)
	}
	if got.MRID != "X-BEFORE" {
		t.Fatalf("X.MRID after a restart = %q, want X-BEFORE: Update(X) reported failure, so the in-flight X-AFTER must never have persisted", got.MRID)
	}
}

// TestDERControlStore_DeleteConcurrentWriterCannotObserveInFlightMutation
// is round 3 item 3: the same reproduction against Delete's writeMu. X
// and Y are seeded before the hook is installed. Writer A deletes X;
// writer B writes a new record Z concurrently. The decisive check is X's
// presence on disk after a restart: it must still be there, since
// Delete(X) reported failure.
func TestDERControlStore_DeleteConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrols.json")
	s, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewDERControlStoreWithPersistence: %v", err)
	}
	ctx := context.Background()
	x := sep2.DERControl{}
	x.MRID = "X"
	if err := s.Create(ctx, "0/0/0", "X", x); err != nil {
		t.Fatalf("seed X: %v", err)
	}
	y := sep2.DERControl{}
	y.MRID = "Y"
	if err := s.Create(ctx, "0/0/1", "Y", y); err != nil {
		t.Fatalf("seed Y: %v", err)
	}

	mutated := make(chan struct{})
	proceed := make(chan struct{})
	var paused atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}

	errA := make(chan error, 1)
	go func() { errA <- s.Delete(context.Background(), "0/0/0", "X") }()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	zNew := sep2.DERControl{}
	zNew.MRID = "Z"
	errB := make(chan error, 1)
	go func() { errB <- s.Create(context.Background(), "0/0/2", "Z", zNew) }()

	var bErr error
	bDone := false
	select {
	case bErr = <-errB:
		bDone = true
		if bErr != nil {
			t.Fatalf("writer B failed before the path was sabotaged: %v", bErr)
		}
		t.Log("writer B completed while A was still paused (the pre-fix interleaving)")
	case <-time.After(200 * time.Millisecond):
		t.Log("writer B still blocked after 200ms (the fixed interleaving)")
	}

	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
	close(proceed)

	if err := <-errA; err == nil {
		t.Fatal("writer A's Delete succeeded despite the sabotaged path, want an error")
	}
	if !bDone {
		select {
		case bErr = <-errB:
		case <-time.After(5 * time.Second):
			t.Fatal("writer B never completed after A released writeMu")
		}
	}
	t.Logf("writer B's Create result: %v", bErr)

	reopened, err := NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(context.Background(), "0/0/0", "X")
	if err != nil {
		t.Fatalf("reopened X: %v, want it still present: Delete(X) reported failure", err)
	}
	if got.MRID != "X" {
		t.Errorf("X.MRID after a restart = %q, want X", got.MRID)
	}
}
