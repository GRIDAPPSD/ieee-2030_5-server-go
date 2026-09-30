package flowreservation

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestNewFRPMRID_WithPEN_LowBitsArePEN pins the plumbing: newFRPMRID(pen)
// reaches the shared minter (internal/mrid) with the configured PEN, the
// same place internal/dercontrol embeds one for a DERControl mRID. Moved
// here from pkg/sep2srv/handlers/flow_reservation, which minted directly
// before #666: minting is now Queue.build's job (see mrid.go), so this
// package is where the seam lives and where its own tests belong.
func TestNewFRPMRID_WithPEN_LowBitsArePEN(t *testing.T) {
	pen := uint32(0xABCD1234)
	mrid, err := newFRPMRID(&pen)
	if err != nil {
		t.Fatalf("newFRPMRID(&pen) error = %v", err)
	}
	raw, err := hex.DecodeString(mrid)
	if err != nil {
		t.Fatalf("mRID %q is not hex: %v", mrid, err)
	}
	if gotPEN := binary.BigEndian.Uint32(raw[12:]); gotPEN != pen {
		t.Fatalf("low 32 bits = %#x, want %#x", gotPEN, pen)
	}
}

// TestNewFRPMRID_NeverProducesAllFReservedForm proves the all-F retry in
// newFRPMRID actually retries: it forces the first draw to be the reserved
// all-F value (IEEE 2030.5 mRIDType, "reserved for an object being created")
// and asserts the function draws again rather than returning it.
func TestNewFRPMRID_NeverProducesAllFReservedForm(t *testing.T) {
	orig := frpRandRead
	defer func() { frpRandRead = orig }()

	calls := 0
	frpRandRead = func(b []byte) (int, error) {
		calls++
		if calls == 1 {
			for i := range b {
				b[i] = 0xFF
			}
			return len(b), nil
		}
		for i := range b {
			b[i] = 0x01
		}
		return len(b), nil
	}

	mrid, err := newFRPMRID(nil)
	if err != nil {
		t.Fatalf("newFRPMRID(nil) error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("frpRandRead called %d times, want at least 2 (a retry after the all-F draw)", calls)
	}
	if mrid == strings.Repeat("F", 32) {
		t.Fatalf("newFRPMRID returned the reserved all-F form: %q", mrid)
	}
}

// TestNewFRPMRID_RandReadErrorPropagates asserts a randomness-source failure
// reaches the caller rather than being swallowed into a zero-value mRID.
func TestNewFRPMRID_RandReadErrorPropagates(t *testing.T) {
	orig := frpRandRead
	defer func() { frpRandRead = orig }()
	wantErr := errors.New("boom")
	frpRandRead = func(b []byte) (int, error) { return 0, wantErr }

	_, err := newFRPMRID(nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("newFRPMRID(nil) error = %v, want %v", err, wantErr)
	}
}

// TestNewFRPMRID_SecondDrawErrorPropagates covers the retry loop's own error
// check, not just the first draw's: the first call reports the reserved
// all-F value (forcing a retry), and the second call fails. A mutant that
// drops the error check inside the retry loop passes every other test here
// because the first-draw check already returns early on a first-call error.
func TestNewFRPMRID_SecondDrawErrorPropagates(t *testing.T) {
	orig := frpRandRead
	defer func() { frpRandRead = orig }()
	wantErr := errors.New("boom on retry")

	calls := 0
	frpRandRead = func(b []byte) (int, error) {
		calls++
		if calls == 1 {
			for i := range b {
				b[i] = 0xFF
			}
			return len(b), nil
		}
		return 0, wantErr
	}

	_, err := newFRPMRID(nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("newFRPMRID(nil) error = %v, want %v", err, wantErr)
	}
	if calls != 2 {
		t.Fatalf("frpRandRead called %d times, want exactly 2", calls)
	}
}
