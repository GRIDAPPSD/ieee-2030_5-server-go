package flow_reservation

import (
	"errors"
	"strings"
	"testing"
)

// TestNewFRPMRID_NeverProducesAllFReservedForm proves the all-F retry in
// newFRPMRID actually retries: it forces the first draw to be the reserved
// all-F value (IEEE 2030.5 mRIDType, "reserved for an object being created")
// and asserts the function draws again rather than returning it. Mirrors
// internal/dercontrol/mrid_test.go's proof of the same seam pattern.
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

	mrid, err := newFRPMRID()
	if err != nil {
		t.Fatalf("newFRPMRID() error = %v", err)
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

	_, err := newFRPMRID()
	if !errors.Is(err, wantErr) {
		t.Fatalf("newFRPMRID() error = %v, want %v", err, wantErr)
	}
}
