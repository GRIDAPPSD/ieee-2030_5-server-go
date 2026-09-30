package memory

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// TestDeviceKeyedCascadeEndDeviceStore_UnassignOneAdminFSALinkToleratesErrNotFound
// is a package-internal (white-box) unit test of the exact tolerance
// GRIDAPPSD/ieee-2030_5-server-go#721 requires: a link already gone by the
// time UnassignDevice runs (a concurrent unassign racing the same device's
// two DELETE attempts, for example) must not fail the cascade. FSAsForDevice
// and UnassignDevice read the same map under the same lock, so a genuine
// ErrNotFound from UnassignDevice right after FSAsForDevice named the same
// fsaID only happens across two calls with something else running between
// them; this test reproduces the SHAPE of that outcome directly, by
// unassigning first and then calling the tolerance check with the fsaID
// FSAsForDevice had already named, rather than relying on a real race to
// land it.
func TestDeviceKeyedCascadeEndDeviceStore_UnassignOneAdminFSALinkToleratesErrNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	adminFSAs := NewAdminFSAStore()
	if err := adminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	fsaIDs := adminFSAs.FSAsForDevice(ctx, "1")
	if len(fsaIDs) != 1 || fsaIDs[0] != "fsa-1" {
		t.Fatalf("FSAsForDevice(1) = %v, want [fsa-1]", fsaIDs)
	}

	// Simulate the link already having been cleared by the time the
	// cascade reaches it: the next UnassignDevice(fsa-1, 1) call genuinely
	// returns ErrNotFound.
	if err := adminFSAs.UnassignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Fatalf("setup: pre-clear the link: %v", err)
	}

	s := NewDeviceKeyedCascadeEndDeviceStore(NewEndDeviceStore(), nil, nil, nil, nil, adminFSAs)
	if err := s.unassignOneAdminFSALink(ctx, fsaIDs[0], "1"); err != nil {
		t.Errorf("unassignOneAdminFSALink with an already-cleared link = %v, want nil (ErrNotFound tolerated)", err)
	}
}
