package sep2admin

import "testing"

func TestExtensionSlotNeverZero(t *testing.T) {
	if ExtensionSlot(0).isZero() {
		t.Fatal("ExtensionSlot(0).isZero() = true, want false: a graft's slot must never read as unset")
	}
}

func TestCorePlacementSortsBeforeExtensionSlot(t *testing.T) {
	core := corePlacement(1)
	graft := ExtensionSlot(1)
	if core.group >= graft.group {
		t.Fatalf("corePlacement group %d is not before ExtensionSlot group %d", core.group, graft.group)
	}
}

func TestZeroValuePlacementIsZero(t *testing.T) {
	var p Placement
	if !p.isZero() {
		t.Fatal("a zero-value Placement must report isZero() true")
	}
}
