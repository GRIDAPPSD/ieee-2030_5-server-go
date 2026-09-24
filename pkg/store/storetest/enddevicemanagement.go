package storetest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Canonical test LFDIs for the management suite.
const (
	managerLFDIA = "AAAA000000000000000000000000000000000001"
	managerLFDIB = "BBBB000000000000000000000000000000000002"
	childLFDI1   = "C001000000000000000000000000000000000001"
	childLFDI2   = "C002000000000000000000000000000000000002"
	childLFDI3   = "C003000000000000000000000000000000000003"
)

// RunEndDeviceManagementSuite holds an implementation to the
// [store.EndDeviceManagementStore] contract. newStore must return an empty
// store on every call.
func RunEndDeviceManagementSuite(t *testing.T, newStore func(*testing.T) store.EndDeviceManagementStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("unmanaged device has no manager", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.ManagerOf(ctx, childLFDI1); !errors.Is(err, store.ErrNotFound) || got != "" {
			t.Errorf("ManagerOf(unmanaged) = %q, %v; want \"\", ErrNotFound", got, err)
		}
		got, err := s.ManagedBy(ctx, managerLFDIA)
		if err != nil || len(got) != 0 {
			t.Errorf("ManagedBy(no pairs) = %v, %v; want empty, nil", got, err)
		}
	})

	t.Run("assign records the pair both ways", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		if got, err := s.ManagerOf(ctx, childLFDI1); err != nil || got != managerLFDIA {
			t.Errorf("ManagerOf = %q, %v; want %q", got, err, managerLFDIA)
		}
		if got, err := s.ManagedBy(ctx, managerLFDIA); err != nil || !slices.Equal(got, []string{childLFDI1}) {
			t.Errorf("ManagedBy = %v, %v; want [%s]", got, err, childLFDI1)
		}
	})

	t.Run("one manager per device", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		if err := s.Assign(ctx, managerLFDIB, childLFDI1); !errors.Is(err, store.ErrAlreadyExists) {
			t.Errorf("second manager: err = %v, want ErrAlreadyExists", err)
		}
		if got, _ := s.ManagerOf(ctx, childLFDI1); got != managerLFDIA {
			t.Errorf("refused reassignment changed the manager to %q", got)
		}
		if got, _ := s.ManagedBy(ctx, managerLFDIB); len(got) != 0 {
			t.Errorf("refused manager holds %v", got)
		}
	})

	t.Run("assigning the same pair again succeeds", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		if got, _ := s.ManagedBy(ctx, managerLFDIA); !slices.Equal(got, []string{childLFDI1}) {
			t.Errorf("ManagedBy after repeat = %v, want one entry", got)
		}
	})

	t.Run("invalid input is refused and stores nothing", func(t *testing.T) {
		// "short manager"/"short managed" are #677 fix round item 5: the
		// write path's checkCanonicalLFDI shares its length rule with the
		// load path's, but the load path (LoadRefusesNonHex40LFDI) had a
		// test of its own and the write path did not, so a future split
		// between them could relax one and leave the other pinned only by
		// accident. "AAAA" is valid hex and already upper case, so it
		// exercises the length check on its own rather than the hex-decode
		// failure a non-hex short value would trip first.
		cases := map[string][2]string{
			"empty manager":        {"", childLFDI1},
			"empty managed":        {managerLFDIA, ""},
			"self-management":      {managerLFDIA, managerLFDIA},
			"lower-case manager":   {strings.ToLower(managerLFDIA), childLFDI1},
			"lower-case managed":   {managerLFDIA, strings.ToLower(childLFDI1)},
			"space around manager": {" " + managerLFDIA, childLFDI1},
			"space around managed": {managerLFDIA, childLFDI1 + " "},
			"short manager":        {"AAAA", childLFDI1},
			"short managed":        {managerLFDIA, "AAAA"},
		}
		for name, pair := range cases {
			s := newStore(t)
			err := s.Assign(ctx, pair[0], pair[1])
			if !errors.Is(err, store.ErrInvalidManagementPair) || errors.Is(err, store.ErrAlreadyExists) || errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: Assign(%q, %q) = %v; want ErrInvalidManagementPair and no other sentinel", name, pair[0], pair[1], err)
			}
			for _, lfdi := range []string{pair[0], pair[1], managerLFDIA, childLFDI1} {
				if got, err := s.ManagerOf(ctx, lfdi); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("%s: refused Assign left ManagerOf(%q) = %q, %v", name, lfdi, got, err)
				}
				if got, _ := s.ManagedBy(ctx, lfdi); len(got) != 0 {
					t.Errorf("%s: refused Assign left ManagedBy(%q) = %v", name, lfdi, got)
				}
			}
		}
	})

	t.Run("lookups do not fold case", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		if got, err := s.ManagerOf(ctx, strings.ToLower(childLFDI1)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ManagerOf(lower-case) = %q, %v; want ErrNotFound", got, err)
		}
		if got, _ := s.ManagedBy(ctx, strings.ToLower(managerLFDIA)); len(got) != 0 {
			t.Errorf("ManagedBy(lower-case) = %v; want empty", got)
		}
	})

	t.Run("ManagedBy is sorted and a copy", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI3)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		mustAssign(t, s, managerLFDIA, childLFDI2)
		want := []string{childLFDI1, childLFDI2, childLFDI3}
		got, err := s.ManagedBy(ctx, managerLFDIA)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("ManagedBy = %v, %v; want %v", got, err, want)
		}
		got[0] = "CLOBBERED"
		if again, _ := s.ManagedBy(ctx, managerLFDIA); !slices.Equal(again, want) {
			t.Errorf("mutating the returned slice changed the store: %v", again)
		}
	})

	t.Run("management is not transitive", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, managerLFDIB)
		mustAssign(t, s, managerLFDIB, childLFDI1)
		if got, _ := s.ManagedBy(ctx, managerLFDIA); !slices.Equal(got, []string{managerLFDIB}) {
			t.Errorf("ManagedBy(A) = %v, want only B", got)
		}
		if got, _ := s.ManagerOf(ctx, childLFDI1); got != managerLFDIB {
			t.Errorf("ManagerOf(child) = %q, want B", got)
		}
	})

	t.Run("unassign revokes", func(t *testing.T) {
		s := newStore(t)
		mustAssign(t, s, managerLFDIA, childLFDI1)
		mustAssign(t, s, managerLFDIA, childLFDI2)
		if err := s.Unassign(ctx, childLFDI1); err != nil {
			t.Fatalf("Unassign: %v", err)
		}
		if got, err := s.ManagerOf(ctx, childLFDI1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ManagerOf after Unassign = %q, %v; want ErrNotFound", got, err)
		}
		if got, _ := s.ManagedBy(ctx, managerLFDIA); !slices.Equal(got, []string{childLFDI2}) {
			t.Errorf("ManagedBy after Unassign = %v, want [%s]", got, childLFDI2)
		}
		if err := s.Unassign(ctx, childLFDI1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("second Unassign = %v, want ErrNotFound", err)
		}
		mustAssign(t, s, managerLFDIB, childLFDI1)
		if got, _ := s.ManagerOf(ctx, childLFDI1); got != managerLFDIB {
			t.Errorf("reassign after Unassign: manager %q, want B", got)
		}
	})
}

func mustAssign(t *testing.T, s store.EndDeviceManagementStore, manager, managed string) {
	t.Helper()
	if err := s.Assign(context.Background(), manager, managed); err != nil {
		t.Fatalf("Assign(%q, %q): %v", manager, managed, err)
	}
}
