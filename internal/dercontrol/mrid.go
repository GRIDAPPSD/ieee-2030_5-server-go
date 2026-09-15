package dercontrol

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
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
func newMRID(pen uint32) (string, error) {
	var b [16]byte
	for {
		if _, err := randRead(b[:12]); err != nil {
			return "", err
		}
		if !isAllFF(b[:12]) {
			break
		}
	}
	binary.BigEndian.PutUint32(b[12:], pen)
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}

func isAllFF(b []byte) bool {
	for _, v := range b {
		if v != 0xFF {
			return false
		}
	}
	return true
}
