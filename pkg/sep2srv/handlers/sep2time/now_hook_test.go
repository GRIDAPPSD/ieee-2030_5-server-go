//go:build csip_test_hooks

package sep2time

import (
	"testing"
	"time"
)

// TestNow_FollowsAdvanceClockOffset proves Now reflects the csip_test_hooks
// clock offset, not just the nowFunc seam time_test.go already covers: it
// fails if Now stopped calling nowFunc after the hook's init() rebinds it.
func TestNow_FollowsAdvanceClockOffset(t *testing.T) {
	ResetClockOffset()
	defer ResetClockOffset()

	before := Now()
	AdvanceClock(time.Hour)
	after := Now()

	diff := after.Sub(before)
	if diff < 59*time.Minute || diff > 61*time.Minute {
		t.Fatalf("Now() diff after AdvanceClock(1h) = %v, want ~1h", diff)
	}
}
