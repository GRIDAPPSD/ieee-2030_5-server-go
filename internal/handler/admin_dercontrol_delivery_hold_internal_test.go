package handler

import (
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// The mirror reading retention floor is built from this hold ceiling; a
// longer ceiling here without one there would let retention remove a reading
// the delivery figure still reaches (#806).
func TestMaxHoldSeconds_MatchesMirrorReadingRetentionFloor(t *testing.T) {
	if got := config.MirrorReadingMaxHold; got != maxHoldSeconds*time.Second {
		t.Fatalf("config.MirrorReadingMaxHold = %v, want %d s", got, maxHoldSeconds)
	}
}

// A window is expired once the earliest reading that can reach its start was
// received before the retention cutoff, including a window that ended after
// the cutoff but started too early to be whole.
func TestReadingsExpired_Boundary(t *testing.T) {
	const now, ret = int64(1_800_000_000), 90000 * time.Second
	cutoff := now - 90000
	for _, tc := range []struct {
		name string
		ws   int64
		ret  time.Duration
		want bool
	}{
		{"earliest reading at the cutoff", cutoff + maxHoldSeconds, ret, false},
		{"earliest reading one second past it", cutoff + maxHoldSeconds - 1, ret, true},
		{"ended after the cutoff, started before it", cutoff - 100, ret, true},
		{"retention unknown", cutoff - 100000, 0, false},
	} {
		if got := readingsExpired(tc.ws, now, tc.ret); got != tc.want {
			t.Errorf("%s: readingsExpired = %v, want %v", tc.name, got, tc.want)
		}
	}
}
