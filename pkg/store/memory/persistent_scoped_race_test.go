package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// TestPersistentScopedStore_SecondWriterNeverPersistsRolledBackRecord is the
// interleaving from GRIDAPPSD/ieee-2030_5-server-go#565 fix round 1: writer A
// is paused after mutating X in memory and before its persist; writer B runs
// meanwhile. With a lock around only the file write, B snapshots X and writes
// it, A's own persist then fails and rolls X back, and a restart serves a
// record whose Create reported failure. With writeMu held across the whole
// call, B waits for A's rollback, so X is absent from disk.
func TestPersistentScopedStore_SecondWriterNeverPersistsRolledBackRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	mutated := make(chan struct{})
	proceed := make(chan struct{})
	// A CAS flag, not sync.Once: Once.Do would block B until A's function
	// returns and serialize the writers by itself.
	var paused atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}

	ctx := context.Background()
	errA := make(chan error, 1)
	go func() { errA <- s.Create(ctx, "p1", "X", storetest.Resource{ID: "X", Body: "rolled-back"}) }()

	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	errB := make(chan error, 1)
	go func() { errB <- s.Create(ctx, "p2", "Y", storetest.Resource{ID: "Y", Body: "second"}) }()

	var bErr error
	bDone := false
	select {
	case bErr = <-errB:
		bDone = true
		if bErr != nil {
			t.Fatalf("writer B failed before the path was blocked: %v", bErr)
		}
	case <-time.After(200 * time.Millisecond):
	}

	// A directory where the temp file goes fails every later persist for any
	// user, root included.
	blockPersistPath(t, path)
	close(proceed)

	if err := <-errA; err == nil {
		t.Fatal("writer A's Create succeeded on a blocked path")
	}
	if !bDone {
		select {
		case bErr = <-errB:
		case <-time.After(5 * time.Second):
			t.Fatal("writer B never completed after A released writeMu")
		}
	}
	if bErr == nil {
		t.Fatal("writer B's Create succeeded on a blocked path: it ran before A's rollback")
	}

	re, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := re.Get(ctx, "p1", "X"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("X on disk after Create(X) reported failure: err = %v, want ErrNotFound", err)
	}
}

func blockPersistPath(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("block persist path: %v", err)
	}
}

// Reads wait for a writer's decision: a Get issued while a Create is paused
// between mutate and persist must not return the record, which the Create
// then rolls back.
func TestPersistentScopedStore_ReadDuringInFlightWriteSeesOnlyDecidedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	mutated := make(chan struct{})
	proceed := make(chan struct{})
	s.afterMutateBeforePersist = func() {
		close(mutated)
		<-proceed
	}
	ctx := context.Background()
	errA := make(chan error, 1)
	go func() { errA <- s.Create(ctx, "p", "X", storetest.Resource{ID: "X", Body: "in-flight"}) }()
	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never reached the hook")
	}

	type getResult struct {
		v   storetest.Resource
		err error
	}
	got := make(chan getResult, 1)
	go func() {
		v, err := s.Get(ctx, "p", "X")
		got <- getResult{v, err}
	}()
	select {
	case r := <-got:
		t.Fatalf("Get returned %+v, %v while the write was undecided", r.v, r.err)
	case <-time.After(200 * time.Millisecond):
	}

	blockPersistPath(t, path)
	close(proceed)
	if err := <-errA; err == nil {
		t.Fatal("Create succeeded on a blocked path")
	}
	select {
	case r := <-got:
		if !errors.Is(r.err, store.ErrNotFound) {
			t.Fatalf("Get after rollback = %+v, %v; want ErrNotFound", r.v, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Get never completed after the writer released the lock")
	}
}

// A drifted key index must not let a failed Update or Delete report
// ErrNotFound, which callers read as "already gone".
func TestPersistentScopedStore_SnapshotReadFailureIsNotNotFound(t *testing.T) {
	for name, op := range map[string]func(s *PersistentScopedStore[storetest.Resource]) error{
		"update": func(s *PersistentScopedStore[storetest.Resource]) error {
			return s.Update(context.Background(), "p", "a", storetest.Resource{ID: "a", Body: "new"})
		},
		"delete": func(s *PersistentScopedStore[storetest.Resource]) error {
			return s.Delete(context.Background(), "p", "a")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewPersistentScopedStore[storetest.Resource](filepath.Join(t.TempDir(), "r.json"), "resources")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Create(context.Background(), "p", "a", storetest.Resource{ID: "a", Body: "old"}); err != nil {
				t.Fatal(err)
			}
			s.addKey("p", "ghost")
			err = op(s)
			if err == nil {
				t.Fatal("write succeeded with a drifted index, want an error")
			}
			if errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed write reported ErrNotFound: %v", err)
			}
		})
	}
}

// The same interleaving with DeleteParent as writer A: B must not snapshot a
// store that already lacks A's parent, or A's rollback leaves p1's records
// gone on disk though DeleteParent reported failure.
func TestPersistentScopedStore_DeleteParentHoldsWriteLockAcrossRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	ctx := context.Background()
	if err := s.Create(ctx, "p1", "a", storetest.Resource{ID: "a", Body: "survivor"}); err != nil {
		t.Fatalf("seed: %v", err)
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
	go func() { _, err := s.DeleteParent(ctx, "p1"); errA <- err }()
	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer A never reached the hook")
	}

	errB := make(chan error, 1)
	go func() { errB <- s.Create(ctx, "p2", "Y", storetest.Resource{ID: "Y", Body: "second"}) }()
	var bErr error
	bDone := false
	select {
	case bErr = <-errB:
		bDone = true
	case <-time.After(200 * time.Millisecond):
	}

	blockPersistPath(t, path)
	close(proceed)
	if err := <-errA; err == nil {
		t.Fatal("DeleteParent succeeded on a blocked path")
	}
	if !bDone {
		select {
		case bErr = <-errB:
		case <-time.After(5 * time.Second):
			t.Fatal("writer B never completed")
		}
	}
	if bErr == nil {
		t.Fatal("writer B's Create succeeded on a blocked path: it ran before A's rollback")
	}

	re, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := re.Get(ctx, "p1", "a")
	if err != nil || got.Body != "survivor" {
		t.Fatalf("p1/a after reload = %+v, %v; want survivor (DeleteParent reported failure)", got, err)
	}
}

// Every reader waits for an undecided write, not only Get.
func TestPersistentScopedStore_EveryReaderWaitsForUndecidedWrite(t *testing.T) {
	readers := map[string]func(ctx context.Context, s *PersistentScopedStore[storetest.Resource]) (any, error){
		"List": func(ctx context.Context, s *PersistentScopedStore[storetest.Resource]) (any, error) {
			r, err := s.List(ctx, "p", store.ListOptions{Unbounded: true})
			return len(r.Items), err
		},
		"Count": func(ctx context.Context, s *PersistentScopedStore[storetest.Resource]) (any, error) {
			return s.Count(ctx, "p")
		},
		"HasParent": func(ctx context.Context, s *PersistentScopedStore[storetest.Resource]) (any, error) {
			return s.HasParent(ctx, "p")
		},
		"Parents": func(ctx context.Context, s *PersistentScopedStore[storetest.Resource]) (any, error) {
			ps, err := s.Parents(ctx)
			return len(ps), err
		},
	}
	// Decided state once the Create has rolled back: nothing exists.
	want := map[string]any{"List": 0, "Count": uint32(0), "HasParent": false, "Parents": 0}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "r.json")
			s, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
			if err != nil {
				t.Fatal(err)
			}
			mutated := make(chan struct{})
			proceed := make(chan struct{})
			s.afterMutateBeforePersist = func() {
				close(mutated)
				<-proceed
			}
			ctx := context.Background()
			errA := make(chan error, 1)
			go func() { errA <- s.Create(ctx, "p", "X", storetest.Resource{ID: "X"}) }()
			select {
			case <-mutated:
			case <-time.After(5 * time.Second):
				t.Fatal("writer never reached the hook")
			}
			type result struct {
				v   any
				err error
			}
			got := make(chan result, 1)
			go func() {
				v, err := read(ctx, s)
				got <- result{v, err}
			}()
			select {
			case r := <-got:
				t.Fatalf("%s returned %v, %v while the write was undecided", name, r.v, r.err)
			case <-time.After(200 * time.Millisecond):
			}
			blockPersistPath(t, path)
			close(proceed)
			if err := <-errA; err == nil {
				t.Fatal("Create succeeded on a blocked path")
			}
			select {
			case r := <-got:
				if r.err != nil || r.v != want[name] {
					t.Fatalf("%s after rollback = %v, %v; want %v", name, r.v, r.err, want[name])
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s never completed", name)
			}
		})
	}
}

// When the rollback's inner Create fails partway, the records already
// restored keep their keys so later snapshots still write them.
func TestPersistentScopedStore_DeleteParentPartialRollbackKeepsRestoredKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id, Body: "v-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	// After the cascade removed everything, plant "b" so the rollback's
	// Create of "b" fails with ErrAlreadyExists after "a" was restored.
	s.afterMutateBeforePersist = func() {
		if err := s.inner.Create(ctx, "p", "b", storetest.Resource{ID: "b", Body: "planted"}); err != nil {
			t.Errorf("plant: %v", err)
		}
		blockPersistPath(t, path)
	}
	n, err := s.DeleteParent(ctx, "p")
	if err == nil || n != 0 {
		t.Fatalf("DeleteParent = %d, %v; want 0 and an error", n, err)
	}
	s.afterMutateBeforePersist = nil
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "q", "z", storetest.Resource{ID: "z", Body: "later"}); err != nil {
		t.Fatalf("later write: %v", err)
	}
	re, err := NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatal(err)
	}
	got, err := re.Get(ctx, "p", "a")
	if err != nil || got.Body != "v-a" {
		t.Fatalf("restored p/a after the next snapshot = %+v, %v; want v-a", got, err)
	}
}
