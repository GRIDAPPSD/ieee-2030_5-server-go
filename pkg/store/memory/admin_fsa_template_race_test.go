package memory

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

var errInjectedPersist = errors.New("injected persist failure")

// interleave runs writer a until it has changed memory, starts writer b, lets
// b finish or block, then fails a's snapshot write and releases a. It
// returns both errors. A store that serializes writers across mutate,
// persist and undo makes b wait for a's rollback; one that does not lets b
// commit inside a's window, and a's undo then lands on b's state.
func interleave(t *testing.T, s *AdminFSAStore, a, b func() error) (errA, errB error) {
	t.Helper()
	mutated := make(chan struct{})
	proceed := make(chan struct{})
	var paused, fail atomic.Bool
	s.afterMutateBeforePersist = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		close(mutated)
		<-proceed
	}
	s.persistFault = func() error {
		if fail.CompareAndSwap(true, false) {
			return errInjectedPersist
		}
		return nil
	}
	t.Cleanup(func() {
		s.afterMutateBeforePersist = nil
		s.persistFault = nil
	})

	chA := make(chan error, 1)
	go func() { chA <- a() }()
	select {
	case <-mutated:
	case <-time.After(5 * time.Second):
		t.Fatal("writer a never reached the hook")
	}

	chB := make(chan error, 1)
	go func() { chB <- b() }()
	bDone := false
	select {
	case errB = <-chB:
		bDone = true
	case <-time.After(200 * time.Millisecond):
	}

	fail.Store(true)
	close(proceed)
	errA = <-chA
	if !bDone {
		errB = <-chB
	}
	if !errors.Is(errA, errInjectedPersist) {
		t.Fatalf("writer a: err = %v, want the injected persist failure", errA)
	}
	return errA, errB
}

func raceStore(t *testing.T) (*AdminFSAStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fsas.json")
	s, err := NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewAdminFSAStoreWithPersistence: %v", err)
	}
	if err := s.Create(context.Background(), "fsa-A", sep2.FunctionSetAssignments{MRID: "MRa"}); err != nil {
		t.Fatalf("Create fsa-A: %v", err)
	}
	return s, path
}

func raceTemplate(id, mRID, desc string) ProgramTemplate {
	return ProgramTemplate{ID: id, MRID: mRID, Description: desc, Primacy: 1}
}

// A failed update must not undo a concurrent update that succeeded.
func TestAdminFSATemplate_FailedUpdateKeepsConcurrentUpdate(t *testing.T) {
	ctx := context.Background()
	s, path := raceStore(t)
	if err := s.CreateTemplate(ctx, "fsa-A", raceTemplate("t1", "M1", "v0")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, errB := interleave(t, s,
		func() error { return s.UpdateTemplate(ctx, "fsa-A", raceTemplate("t1", "M1", "v1")) },
		func() error { return s.UpdateTemplate(ctx, "fsa-A", raceTemplate("t1", "M1", "v2")) },
	)
	if errB != nil {
		t.Fatalf("writer b: %v", errB)
	}

	got, err := s.Template(ctx, "fsa-A", "t1")
	if err != nil || got.Description != "v2" {
		t.Errorf("in memory: %+v, %v; want v2, the update that succeeded", got, err)
	}
	reloaded, err := NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, err := reloaded.Template(ctx, "fsa-A", "t1"); err != nil || got.Description != "v2" {
		t.Errorf("on disk: %+v, %v; want v2", got, err)
	}
}

// A failed delete must not restore a template whose mRID a concurrent create
// has since taken, or two templates would share one mRID and the snapshot
// would no longer load.
func TestAdminFSATemplate_FailedDeleteNeverDuplicatesMRID(t *testing.T) {
	ctx := context.Background()
	s, path := raceStore(t)
	if err := s.CreateTemplate(ctx, "fsa-A", raceTemplate("t1", "M", "first")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, errB := interleave(t, s,
		func() error { return s.DeleteTemplate(ctx, "fsa-A", "t1") },
		func() error { return s.CreateTemplate(ctx, "fsa-A", raceTemplate("t2", "M", "second")) },
	)

	holders := 0
	for _, tmpl := range s.Templates(ctx, "fsa-A") {
		if tmpl.MRID == "M" {
			holders++
		}
	}
	if holders != 1 {
		t.Errorf("templates holding mRID M = %d, want 1 (writer b err %v)", holders, errB)
	}
	if !errors.Is(errB, store.ErrAlreadyExists) {
		t.Errorf("writer b: err = %v, want ErrAlreadyExists once t1 is restored", errB)
	}

	// Any later successful write snapshots memory; the result must load.
	if err := s.CreateTemplate(ctx, "fsa-A", raceTemplate("t3", "M3", "third")); err != nil {
		t.Fatalf("later write: %v", err)
	}
	if _, err := NewAdminFSAStoreWithPersistence(path); err != nil {
		t.Errorf("reload after the later write: %v", err)
	}
}

// A failed template delete must not resurrect the template under an FSA that
// a concurrent delete removed.
func TestAdminFSATemplate_FailedDeleteNeverOrphansTemplate(t *testing.T) {
	ctx := context.Background()
	s, _ := raceStore(t)
	if err := s.CreateTemplate(ctx, "fsa-A", raceTemplate("t1", "M1", "only")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, errB := interleave(t, s,
		func() error { return s.DeleteTemplate(ctx, "fsa-A", "t1") },
		func() error { return s.Delete(ctx, "fsa-A") },
	)

	_, getErr := s.Get(ctx, "fsa-A")
	if errors.Is(getErr, store.ErrNotFound) && len(s.Templates(ctx, "fsa-A")) > 0 {
		t.Errorf("template t1 restored under absent FSA fsa-A")
	}
	if !errors.Is(errB, ErrAdminFSAHasTemplates) {
		t.Errorf("writer b: err = %v, want ErrAdminFSAHasTemplates once t1 is restored", errB)
	}
	if getErr != nil {
		t.Errorf("Get(fsa-A): %v, want the FSA kept", getErr)
	}
}

// The window predates templates: a writer that commits inside another
// writer's failed window must not carry that writer's change to disk.
func TestAdminFSA_SecondWriterNeverPersistsRolledBackFSA(t *testing.T) {
	ctx := context.Background()
	s, path := raceStore(t)

	_, errB := interleave(t, s,
		func() error { return s.Create(ctx, "fsa-X", sep2.FunctionSetAssignments{MRID: "MRx"}) },
		func() error { return s.Create(ctx, "fsa-Y", sep2.FunctionSetAssignments{MRID: "MRy"}) },
	)
	if errB != nil {
		t.Fatalf("writer b: %v", errB)
	}
	reloaded, err := NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := reloaded.Get(ctx, "fsa-X"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("fsa-X on disk: err = %v, want ErrNotFound for a create that reported failure", err)
	}
	if _, err := reloaded.Get(ctx, "fsa-Y"); err != nil {
		t.Errorf("fsa-Y on disk: %v", err)
	}
}
