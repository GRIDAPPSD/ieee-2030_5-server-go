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
