package dercontrol

import (
	"crypto/rand"

	sharedmrid "github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mrid"
)

// randRead is the mRID randomness source. Overridable in tests only, the
// same seam pattern pkg/sep2srv/handlers/sep2time uses for its clock, so a
// test can prove the all-F retry below actually retries rather than assert
// it by inspection.
var randRead = rand.Read

// newMRID builds a 128-bit mRID (32 uppercase hex digits): pen in the low
// 32 bits, crypto/rand in the remaining 96, per IEEE 2030.5-2018 mRIDType
// (line 10300). The all-F form of those 96 bits
// (0xFFFFFFFFFFFFFFFFFFFFFFFF[pen]) is reserved by the standard for an
// object still being created and is never returned; on that draw the
// function retries.
//
// Delegates to internal/mrid, the copy this package shares with
// pkg/sep2srv/handlers/flow_reservation, so the retry logic exists once.
// randRead is still threaded through explicitly: this package's own test
// seam overrides that var directly, and the call below is what makes the
// override reach the shared implementation.
func newMRID(pen uint32) (string, error) {
	return sharedmrid.New(randRead, &pen)
}

// isAllFF is kept as a package-level name because mrid_test.go asserts
// against it directly; the logic itself lives in internal/mrid.
func isAllFF(b []byte) bool {
	return sharedmrid.IsAllFF(b)
}
