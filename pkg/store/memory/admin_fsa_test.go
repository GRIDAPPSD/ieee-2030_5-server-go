package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// AdminFSAStore tests (GRIDAPPSD/ieee-2030_5-server-go#163).
//
// The store keeps three things: the FSA struct itself, the set of attached
// DERProgram hrefs, and the set of assigned device ids. Deletion is gated
// on both link sets being empty so the operator must explicitly tear down
// the tree.

func adminFSA(id, desc string, primacy uint8) sep2.FunctionSetAssignments {
	return sep2.FunctionSetAssignments{
		Resource:    sep2.Resource{Href: "/api/fsas/" + id},
		MRID:        id,
		Description: desc,
	}
}

func TestAdminFSAStore_CreateAndGet(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()

	if err := s.Create(ctx, "fsa-1", adminFSA("fsa-1", "Solar program template", 1)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, "fsa-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MRID != "fsa-1" || got.Description != "Solar program template" {
		t.Errorf("Get returned wrong FSA: %+v", got)
	}
}

func TestAdminFSAStore_GetNotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_CreateDuplicateRejected(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "first", 1))

	err := s.Create(ctx, "fsa-1", adminFSA("fsa-1", "second", 2))
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAdminFSAStore_ListSorted(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-b", adminFSA("fsa-b", "b", 1))
	_ = s.Create(ctx, "fsa-a", adminFSA("fsa-a", "a", 1))
	_ = s.Create(ctx, "fsa-c", adminFSA("fsa-c", "c", 1))

	list := s.List(ctx)
	if len(list) != 3 {
		t.Fatalf("expected 3, got %d", len(list))
	}
	if list[0].MRID != "fsa-a" || list[1].MRID != "fsa-b" || list[2].MRID != "fsa-c" {
		t.Errorf("list not sorted: %v / %v / %v", list[0].MRID, list[1].MRID, list[2].MRID)
	}
}

func TestAdminFSAStore_DeleteNotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	err := s.Delete(context.Background(), "ghost")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_DeleteWithProgramsRefused(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AttachProgram(ctx, "fsa-1", "/edev/_admin/fsa/0/derp/p1")

	err := s.Delete(ctx, "fsa-1")
	if !errors.Is(err, memory.ErrAdminFSAInUse) {
		t.Errorf("expected ErrAdminFSAInUse, got %v", err)
	}
}

func TestAdminFSAStore_DeleteWithDevicesRefused(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AssignDevice(ctx, "fsa-1", "dev-A")

	err := s.Delete(ctx, "fsa-1")
	if !errors.Is(err, memory.ErrAdminFSAInUse) {
		t.Errorf("expected ErrAdminFSAInUse, got %v", err)
	}
}

func TestAdminFSAStore_DeleteAfterDetachUnlink(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AttachProgram(ctx, "fsa-1", "/p/1")
	_ = s.AssignDevice(ctx, "fsa-1", "dev-A")

	_ = s.DetachProgram(ctx, "fsa-1", "/p/1")
	_ = s.UnassignDevice(ctx, "fsa-1", "dev-A")

	if err := s.Delete(ctx, "fsa-1"); err != nil {
		t.Errorf("Delete after teardown: %v", err)
	}
	if _, err := s.Get(ctx, "fsa-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("post-delete Get should be ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_AttachProgram_FSANotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	err := s.AttachProgram(context.Background(), "ghost", "/p/1")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_AttachProgram_Duplicate(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AttachProgram(ctx, "fsa-1", "/p/1")

	err := s.AttachProgram(ctx, "fsa-1", "/p/1")
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAdminFSAStore_DetachProgram_NotAttached(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))

	err := s.DetachProgram(ctx, "fsa-1", "/never-attached")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_DetachProgram_FSANotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	err := s.DetachProgram(context.Background(), "ghost", "/p/1")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_ProgramsSortedCopy(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AttachProgram(ctx, "fsa-1", "/p/b")
	_ = s.AttachProgram(ctx, "fsa-1", "/p/a")
	_ = s.AttachProgram(ctx, "fsa-1", "/p/c")

	got := s.Programs(ctx, "fsa-1")
	if len(got) != 3 || got[0] != "/p/a" || got[2] != "/p/c" {
		t.Errorf("expected sorted [a b c], got %v", got)
	}

	// Mutating the returned slice must not affect the store.
	got[0] = "MUTATED"
	again := s.Programs(ctx, "fsa-1")
	if again[0] != "/p/a" {
		t.Errorf("returned slice not independent: %v", again)
	}
}

func TestAdminFSAStore_AssignDevice_FSANotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	err := s.AssignDevice(context.Background(), "ghost", "dev-A")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_AssignDevice_Duplicate(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AssignDevice(ctx, "fsa-1", "dev-A")

	err := s.AssignDevice(ctx, "fsa-1", "dev-A")
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAdminFSAStore_UnassignDevice_NotAssigned(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))

	err := s.UnassignDevice(ctx, "fsa-1", "dev-A")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminFSAStore_DevicesSortedCopy(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))
	_ = s.AssignDevice(ctx, "fsa-1", "dev-B")
	_ = s.AssignDevice(ctx, "fsa-1", "dev-A")
	_ = s.AssignDevice(ctx, "fsa-1", "dev-C")

	got := s.Devices(ctx, "fsa-1")
	if len(got) != 3 || got[0] != "dev-A" || got[2] != "dev-C" {
		t.Errorf("expected sorted [dev-A dev-B dev-C], got %v", got)
	}
	// Independent copy.
	got[0] = "MUTATED"
	again := s.Devices(ctx, "fsa-1")
	if again[0] != "dev-A" {
		t.Errorf("Devices returned shared slice: %v", again)
	}
}

func TestAdminFSAStore_FSAsForDevice(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-a", adminFSA("fsa-a", "a", 1))
	_ = s.Create(ctx, "fsa-b", adminFSA("fsa-b", "b", 1))
	_ = s.Create(ctx, "fsa-c", adminFSA("fsa-c", "c", 1))

	_ = s.AssignDevice(ctx, "fsa-a", "dev-X")
	_ = s.AssignDevice(ctx, "fsa-c", "dev-X")
	_ = s.AssignDevice(ctx, "fsa-b", "dev-Y")

	gotX := s.FSAsForDevice(ctx, "dev-X")
	if len(gotX) != 2 || gotX[0] != "fsa-a" || gotX[1] != "fsa-c" {
		t.Errorf("dev-X expected [fsa-a fsa-c], got %v", gotX)
	}
	gotY := s.FSAsForDevice(ctx, "dev-Y")
	if len(gotY) != 1 || gotY[0] != "fsa-b" {
		t.Errorf("dev-Y expected [fsa-b], got %v", gotY)
	}
	gotZ := s.FSAsForDevice(ctx, "dev-Z")
	if len(gotZ) != 0 {
		t.Errorf("dev-Z expected [], got %v", gotZ)
	}
}

func TestAdminFSAStore_GetReturnsCopy(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "original", 1))

	got, _ := s.Get(ctx, "fsa-1")
	got.Description = "MUTATED"

	again, _ := s.Get(ctx, "fsa-1")
	if again.Description != "original" {
		t.Errorf("Get returned shared reference: %q", again.Description)
	}
}

func TestAdminFSAStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	_ = s.Create(ctx, "fsa-1", adminFSA("fsa-1", "x", 1))

	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			href := "/p/" + string(rune('a'+(i%26)))
			_ = s.AttachProgram(ctx, "fsa-1", href)
			_ = s.Programs(ctx, "fsa-1")
			_, _ = s.Get(ctx, "fsa-1")
		}(i)
	}
	wg.Wait()

	if got := s.Programs(ctx, "fsa-1"); len(got) == 0 {
		t.Error("expected at least one program attached after concurrent run")
	}
}
