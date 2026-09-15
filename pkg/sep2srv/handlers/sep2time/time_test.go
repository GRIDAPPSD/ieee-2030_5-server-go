package sep2time

import (
	"testing"
	"time"
)

// TestNow_UsesNowFuncSeam proves Now reads the nowFunc seam rather than
// calling time.Now() directly: it fails if Now is ever changed to bypass
// nowFunc, in both build modes, since this file carries no build tag and
// nowFunc itself is declared unconditionally in time.go.
func TestNow_UsesNowFuncSeam(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()

	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	nowFunc = func() time.Time { return fixed }

	if got := Now(); !got.Equal(fixed) {
		t.Fatalf("Now() = %v, want %v", got, fixed)
	}
}
