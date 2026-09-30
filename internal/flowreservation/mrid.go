package flowreservation

import (
	"crypto/rand"

	sharedmrid "github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mrid"
)

// frpRandRead is the FlowReservationResponse mRID randomness source.
// Overridable in tests only, so a test can prove the all-F retry actually
// retries rather than assert it by inspection: internal/dercontrol/mrid.go
// and pkg/sep2srv/handlers/flow_reservation use the same seam for the same
// reason.
var frpRandRead = rand.Read

// newFRPMRID mints a 128-bit mRID (32 uppercase hex digits) for a built
// FlowReservationResponse, per IEEE 2030.5 mRIDType, sharing
// internal/mrid's retry logic rather than a third copy of it. pen embeds
// its value in the low 32 bits when non-nil and non-zero (0 is
// IANA-reserved); nil mints all 128 bits at random.
func newFRPMRID(pen *uint32) (string, error) {
	return sharedmrid.New(frpRandRead, pen)
}
