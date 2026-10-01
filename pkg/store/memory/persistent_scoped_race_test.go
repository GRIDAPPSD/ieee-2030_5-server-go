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
