package memory_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #440: certificate rotation carries a new LFDI, so a pair keyed on the
// retired LFDI must be moved rather than left behind.
// These tests exercise RekeyManager and RekeyManaged directly on the
// concrete store: they are not part of the generic
// store.EndDeviceManagementStore contract in pkg/store/storetest, since
// re-keying is a provisioning-plane operation the interface does not (yet)
// require of every implementation.

const (
	oldManagerLFDI = "AAAA000000000000000000000000000000000001"
	newManagerLFDI = "AAAA000000000000000000000000000000000099"
	otherManager   = "BBBB000000000000000000000000000000000002"
	child1LFDI     = "C001000000000000000000000000000000000001"
	child2LFDI     = "C002000000000000000000000000000000000002"
	newChildLFDI   = "C099000000000000000000000000000000000099"
)

func newRekeyStore(t *testing.T) *memory.EndDeviceManagementStore {
	t.Helper()
	return memory.NewEndDeviceManagementStore()
}

func TestRekeyManager_MovesEveryPair(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)
	mustAssignT(t, s, oldManagerLFDI, child2LFDI)

	if err := s.RekeyManager(ctx, oldManagerLFDI, newManagerLFDI); err != nil {
		t.Fatalf("RekeyManager: %v", err)
	}

	got, err := s.ManagedBy(ctx, newManagerLFDI)
	if err != nil || !slices.Equal(got, []string{child1LFDI, child2LFDI}) {
		t.Errorf("ManagedBy(new) = %v, %v; want [%s %s]", got, err, child1LFDI, child2LFDI)
	}
	for _, child := range []string{child1LFDI, child2LFDI} {
		if manager, err := s.ManagerOf(ctx, child); err != nil || manager != newManagerLFDI {
			t.Errorf("ManagerOf(%s) = %q, %v; want %q", child, manager, err, newManagerLFDI)
		}
	}
}

// TestRekeyManager_RetiredLFDIIsRefused is the brief's decisive assertion:
// after re-key the retired manager LFDI must be refused, not merely
// "no longer preferred".
func TestRekeyManager_RetiredLFDIIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)

	if err := s.RekeyManager(ctx, oldManagerLFDI, newManagerLFDI); err != nil {
		t.Fatalf("RekeyManager: %v", err)
	}

	// The retired manager no longer manages anything: a second rekey from
	// it, or any read against it, finds nothing.
	if got, err := s.ManagedBy(ctx, oldManagerLFDI); err != nil || len(got) != 0 {
		t.Errorf("ManagedBy(retired) = %v, %v; want empty", got, err)
	}
	if err := s.RekeyManager(ctx, oldManagerLFDI, "CCCC000000000000000000000000000000000003"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second RekeyManager from the retired LFDI = %v, want ErrNotFound", err)
	}
	// And the gate-relevant fact: the child's manager is the new LFDI only.
	if manager, _ := s.ManagerOf(ctx, child1LFDI); manager != newManagerLFDI {
		t.Errorf("ManagerOf(child) = %q, want %q (the retired LFDI must not still be recognized)", manager, newManagerLFDI)
	}
}

// TestRekeyManager_CollisionIsRefused is item 5's decisive assertion: the
// two rekey directions must agree about a collision. RekeyManaged already
// refuses a destination that already has a manager (see
// TestRekeyManaged_CollisionIsRefused); before this fix RekeyManager instead
// merged the two fleets silently and irreversibly (the old key is deleted).
// Merging is never what a certificate rotation needs, so it is refused the
// same way.
func TestRekeyManager_CollisionIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)
	mustAssignT(t, s, newManagerLFDI, child2LFDI)

	err := s.RekeyManager(ctx, oldManagerLFDI, newManagerLFDI)
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("RekeyManager(collision) = %v, want ErrAlreadyExists", err)
	}
	// Neither fleet moved: the old manager keeps its device, the target
	// keeps its own and does not gain the old manager's.
	if manager, _ := s.ManagerOf(ctx, child1LFDI); manager != oldManagerLFDI {
		t.Errorf("ManagerOf(child1) after refused collision = %q, want unchanged %q", manager, oldManagerLFDI)
	}
	got, _ := s.ManagedBy(ctx, newManagerLFDI)
	if !slices.Equal(got, []string{child2LFDI}) {
		t.Errorf("ManagedBy(new) after refused collision = %v, want [%s] unchanged", got, child2LFDI)
	}
}

func TestRekeyManager_NothingToRekeyIsNotFound(t *testing.T) {
	s := newRekeyStore(t)
	if err := s.RekeyManager(context.Background(), oldManagerLFDI, newManagerLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RekeyManager(nothing) = %v, want ErrNotFound", err)
	}
}

// TestRekeyManager_NoOpFromUnknownLFDIIsNotFound is item 6's H6 assertion:
// from == to must not short-circuit to a reported success before existence
// is checked. Pasting the same, never-assigned LFDI into both fields must
// answer ErrNotFound, the same as any other rekey of an LFDI that manages
// nothing, not a silent "successful" no-op.
func TestRekeyManager_NoOpFromUnknownLFDIIsNotFound(t *testing.T) {
	s := newRekeyStore(t)
	err := s.RekeyManager(context.Background(), oldManagerLFDI, oldManagerLFDI)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RekeyManager(unknown, unknown) = %v, want ErrNotFound", err)
	}
}

// TestRekeyManager_WouldSelfManageIsRefused covers "cannot orphan a pair or
// leave two managers for one device": if the old manager itself manages the
// LFDI it is being renamed to, the rekey would make that device its own
// manager, and the store must refuse rather than silently create that.
func TestRekeyManager_WouldSelfManageIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, newManagerLFDI)

	err := s.RekeyManager(ctx, oldManagerLFDI, newManagerLFDI)
	if !errors.Is(err, store.ErrInvalidManagementPair) {
		t.Fatalf("RekeyManager(self-management) = %v, want ErrInvalidManagementPair", err)
	}
	// No partial mutation: the original pair is untouched.
	if manager, mErr := s.ManagerOf(ctx, newManagerLFDI); mErr != nil || manager != oldManagerLFDI {
		t.Errorf("ManagerOf after refused rekey = %q, %v; want %q unchanged", manager, mErr, oldManagerLFDI)
	}
}

func TestRekeyManager_NoOpWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)
	if err := s.RekeyManager(ctx, oldManagerLFDI, oldManagerLFDI); err != nil {
		t.Errorf("RekeyManager(same LFDI) = %v, want nil", err)
	}
	if manager, _ := s.ManagerOf(ctx, child1LFDI); manager != oldManagerLFDI {
		t.Errorf("ManagerOf after no-op rekey = %q, want unchanged %q", manager, oldManagerLFDI)
	}
}

func TestRekeyManaged_MovesTheOneEntry(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)

	if err := s.RekeyManaged(ctx, child1LFDI, newChildLFDI); err != nil {
		t.Fatalf("RekeyManaged: %v", err)
	}
	if manager, err := s.ManagerOf(ctx, newChildLFDI); err != nil || manager != oldManagerLFDI {
		t.Errorf("ManagerOf(new) = %q, %v; want %q", manager, err, oldManagerLFDI)
	}
	if _, err := s.ManagerOf(ctx, child1LFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ManagerOf(retired managed) = %v, want ErrNotFound", err)
	}
	got, err := s.ManagedBy(ctx, oldManagerLFDI)
	if err != nil || !slices.Equal(got, []string{newChildLFDI}) {
		t.Errorf("ManagedBy after RekeyManaged = %v, %v; want [%s]", got, err, newChildLFDI)
	}
}

func TestRekeyManaged_NothingToRekeyIsNotFound(t *testing.T) {
	s := newRekeyStore(t)
	if err := s.RekeyManaged(context.Background(), child1LFDI, newChildLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RekeyManaged(unmanaged) = %v, want ErrNotFound", err)
	}
}

// TestRekeyManaged_CollisionIsRefused is the brief's "including when it
// collides with an existing pair": the target LFDI already has a (possibly
// different) manager, so the rekey must not silently overwrite that pair.
func TestRekeyManaged_CollisionIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)
	mustAssignT(t, s, otherManager, child2LFDI)

	err := s.RekeyManaged(ctx, child1LFDI, child2LFDI)
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("RekeyManaged(collision) = %v, want ErrAlreadyExists", err)
	}
	// Neither pair moved: child1 keeps its manager, child2 keeps its own.
	if manager, _ := s.ManagerOf(ctx, child1LFDI); manager != oldManagerLFDI {
		t.Errorf("ManagerOf(child1) after refused collision = %q, want unchanged %q", manager, oldManagerLFDI)
	}
	if manager, _ := s.ManagerOf(ctx, child2LFDI); manager != otherManager {
		t.Errorf("ManagerOf(child2) after refused collision = %q, want unchanged %q", manager, otherManager)
	}
}

func TestRekeyManaged_WouldSelfManageIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)

	err := s.RekeyManaged(ctx, child1LFDI, oldManagerLFDI)
	if !errors.Is(err, store.ErrInvalidManagementPair) {
		t.Fatalf("RekeyManaged(self-management) = %v, want ErrInvalidManagementPair", err)
	}
	if manager, _ := s.ManagerOf(ctx, child1LFDI); manager != oldManagerLFDI {
		t.Errorf("ManagerOf after refused rekey = %q, want unchanged %q", manager, oldManagerLFDI)
	}
}

func TestRekeyManaged_NoOpWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newRekeyStore(t)
	mustAssignT(t, s, oldManagerLFDI, child1LFDI)
	if err := s.RekeyManaged(ctx, child1LFDI, child1LFDI); err != nil {
		t.Errorf("RekeyManaged(same LFDI) = %v, want nil", err)
	}
}

// TestRekeyManaged_NoOpFromUnknownLFDIIsNotFound is RekeyManager's H6 case
// mirrored on the managed side: from == to must not short-circuit before
// existence is checked.
func TestRekeyManaged_NoOpFromUnknownLFDIIsNotFound(t *testing.T) {
	s := newRekeyStore(t)
	err := s.RekeyManaged(context.Background(), child1LFDI, child1LFDI)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RekeyManaged(unknown, unknown) = %v, want ErrNotFound", err)
	}
}

func mustAssignT(t *testing.T, s *memory.EndDeviceManagementStore, manager, managed string) {
	t.Helper()
	if err := s.Assign(context.Background(), manager, managed); err != nil {
		t.Fatalf("Assign(%q, %q): %v", manager, managed, err)
	}
}
