package dercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// TestLifecycleStore_ConcurrentWriterCannotObserveInFlightMutation is
// GRIDAPPSD/ieee-2030_5-server-go#565 fix round 1, item 1's LifecycleStore
// half. Same shape as
// pkg/store/memory/dercontrol_persistence_race_test.go's
// TestDERControlStore_ConcurrentWriterCannotObserveInFlightMutation: writer
// A (Create X) is paused, via the afterMutateBeforePersist hook, after
// mutating X but before its own persist; writer B (Create Y) is launched
// while A is paused. On the pre-fix code (a lock held only around the
// snapshot write) B completes immediately and its snapshot durably writes
// A's in-flight X to disk; the path is then sabotaged so A's own persist
// fails and rolls X back, leaving a lifecycle record on disk for a write
// the caller was told failed. On the fixed code B blocks on writeMu until
// A's whole call has returned, so it never snapshots X while X's fate is
// undecided.
func TestLifecycleStore_ConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}

	mutated := make(chan struct{})
	proceed := make(chan struct{})
	// A CAS flag, not sync.Once: Once.Do blocks a second concurrent caller
	// until the first caller's function returns, which would serialize A
	// and B by itself and defeat the test (see the sibling DERControlStore
	// test for the same note).
	var paused atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}

	errA := make(chan error, 1)
	go func() { errA <- s.Create(context.Background(), "0/0/0", "X", LifecycleRecord{}) }()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	errB := make(chan error, 1)
	go func() { errB <- s.Create(context.Background(), "0/0/1", "Y", LifecycleRecord{}) }()

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

	reopened, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := reopened.Get(context.Background(), "0/0/0", "X"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lifecycle record X present on disk after a restart, though Create(X) reported failure: %v", err)
	}
}

// TestLifecycleStore_UpdateConcurrentWriterCannotObserveInFlightMutation is
// GRIDAPPSD/ieee-2030_5-server-go#565 round 3 item 3: the Create test above
// proves writeMu covers Create; this proves it covers Update the same way.
// X and Y are seeded before the hook is installed. Writer A updates X's
// CancelReason; writer B updates Y concurrently. The decisive check is X's
// CancelReason after a restart: it must still be the pre-Update value.
func TestLifecycleStore_UpdateConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}
	ctx := context.Background()
	if err := s.Create(ctx, "0/0/0", "X", LifecycleRecord{CancelReason: "X-BEFORE"}); err != nil {
		t.Fatalf("seed X: %v", err)
	}
	if err := s.Create(ctx, "0/0/1", "Y", LifecycleRecord{CancelReason: "Y-BEFORE"}); err != nil {
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
	go func() {
		errA <- s.Update(context.Background(), "0/0/0", "X", LifecycleRecord{CancelReason: "X-AFTER"})
	}()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	errB := make(chan error, 1)
	go func() {
		errB <- s.Update(context.Background(), "0/0/1", "Y", LifecycleRecord{CancelReason: "Y-AFTER"})
	}()

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

	reopened, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(context.Background(), "0/0/0", "X")
	if err != nil {
		t.Fatalf("reopened X: %v, want it present with its pre-Update value", err)
	}
	if got.CancelReason != "X-BEFORE" {
		t.Fatalf("X.CancelReason after a restart = %q, want X-BEFORE: Update(X) reported failure, so the in-flight X-AFTER must never have persisted", got.CancelReason)
	}
}

// TestLifecycleStore_DeleteConcurrentWriterCannotObserveInFlightMutation is
// round 3 item 3: the same reproduction against Delete's writeMu. X and Y
// are seeded before the hook is installed. Writer A deletes X; writer B
// creates a new record Z concurrently. The decisive check is X's presence
// on disk after a restart: it must still be there, since Delete(X)
// reported failure.
func TestLifecycleStore_DeleteConcurrentWriterCannotObserveInFlightMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence: %v", err)
	}
	ctx := context.Background()
	if err := s.Create(ctx, "0/0/0", "X", LifecycleRecord{CancelReason: "X"}); err != nil {
		t.Fatalf("seed X: %v", err)
	}
	if err := s.Create(ctx, "0/0/1", "Y", LifecycleRecord{CancelReason: "Y"}); err != nil {
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

	errB := make(chan error, 1)
	go func() { errB <- s.Create(context.Background(), "0/0/2", "Z", LifecycleRecord{CancelReason: "Z"}) }()

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

	reopened, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(context.Background(), "0/0/0", "X")
	if err != nil {
		t.Fatalf("reopened X: %v, want it still present: Delete(X) reported failure", err)
	}
	if got.CancelReason != "X" {
		t.Errorf("X.CancelReason after a restart = %q, want X", got.CancelReason)
	}
}
