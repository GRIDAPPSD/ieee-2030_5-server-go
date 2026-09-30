// Package mrid mints the 128-bit mRID (32 uppercase hex digits) IEEE 2030.5
// mRIDType requires on every Event-derived resource. internal/dercontrol
// (DERControl) and pkg/sep2srv/handlers/flow_reservation
// (FlowReservationResponse) each mint one; this package is the single copy
// of that logic so a retry-boundary fix lands once, not twice.
package mrid

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// New mints a 128-bit mRID using randRead as the randomness source.
//
// If pen is non-nil and non-zero, its value occupies the low 32 bits and
// only the high 96 bits are drawn from randRead; the all-F reserved form of
// those 96 bits (0xFFFFFFFFFFFFFFFFFFFFFFFF[pen], IEEE 2030.5 mRIDType,
// "reserved for an object still being created") is never returned, and that
// draw is retried. A nil or zero pen (0 is IANA-reserved, so
// internal/dercontrol.Config already treats it as "not configured") mints
// all 128 bits at random instead, retrying the all-F form of the full value.
func New(randRead func([]byte) (int, error), pen *uint32) (string, error) {
	hasPEN := pen != nil && *pen != 0
	n := 16
	if hasPEN {
		n = 12
	}
	var b [16]byte
	for {
		if _, err := randRead(b[:n]); err != nil {
			return "", err
		}
		if !IsAllFF(b[:n]) {
			break
		}
	}
	if hasPEN {
		binary.BigEndian.PutUint32(b[12:], *pen)
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}

// IsAllFF reports whether every byte in b is 0xFF: the IEEE 2030.5 mRIDType
// reserved value ("an object still being created"), checked over whichever
// segment the caller drew at random.
func IsAllFF(b []byte) bool {
	for _, v := range b {
		if v != 0xFF {
			return false
		}
	}
	return true
}
