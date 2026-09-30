package commitment

import (
	"errors"
	"testing"
)

// TestConflictCodes_AreClosedAndDistinct pins the closed set of codes 3.1
// defines: every code is a fixed string (never request text) and no two
// collide, since a caller matches on the string in a 409 body.
func TestConflictCodes_AreClosedAndDistinct(t *testing.T) {
	t.Parallel()
	codes := []ConflictCode{
		ConflictFleetWindow,
		ConflictGrantNotLive,
		ConflictNotExecutable,
		ConflictModeNotTarget,
		ConflictOutsideFleet,
		ConflictOutsideInterval,
		ConflictDirection,
		ConflictPower,
		ConflictEnergy,
		ConflictOverlap,
	}
	seen := make(map[ConflictCode]bool, len(codes))
	for _, c := range codes {
		if c == "" {
			t.Errorf("ConflictCode %q must not be empty", c)
		}
		if seen[c] {
			t.Errorf("ConflictCode %q is duplicated", c)
		}
		seen[c] = true
	}
}

func TestConflictError_ImplementsError(t *testing.T) {
	t.Parallel()
	err := &ConflictError{Code: ConflictGrantNotLive, MRID: "grant-1"}
	var target *ConflictError
	if !errors.As(error(err), &target) {
		t.Fatal("ConflictError does not round-trip through errors.As")
	}
	if target.Code != ConflictGrantNotLive || target.MRID != "grant-1" {
		t.Errorf("errors.As target = %+v, want Code=%q MRID=%q", target, ConflictGrantNotLive, "grant-1")
	}
	if err.Error() == "" {
		t.Error("Error() must not be empty")
	}
}
