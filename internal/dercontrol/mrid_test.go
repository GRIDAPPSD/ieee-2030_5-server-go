package dercontrol

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

// Acceptance criterion 2: the mRID is 32 hex digits whose low 32 bits are
// the configured PEN and whose remaining 96 bits come from crypto/rand;
// with no PEN configured the issuer refuses.

func TestIssue_MRID_LowBitsArePEN(t *testing.T) {
	pen := uint32(0xABCD1234)
	h := newHarness(Config{PEN: &pen})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if len(res.Control.MRID) != 32 {
		t.Fatalf("MRID length = %d, want 32", len(res.Control.MRID))
	}
	raw, err := hex.DecodeString(res.Control.MRID)
	if err != nil {
		t.Fatalf("MRID %q is not hex: %v", res.Control.MRID, err)
	}
	if len(raw) != 16 {
		t.Fatalf("decoded MRID length = %d, want 16 bytes", len(raw))
	}
	gotPEN := binary.BigEndian.Uint32(raw[12:])
	if gotPEN != pen {
		t.Fatalf("low 32 bits = %#x, want configured PEN %#x", gotPEN, pen)
	}
}

func TestIssue_MRID_1000DistinctValues(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		res, err := h.issuer.Issue(context.Background(), CreateRequest{
			DERProgramHref:  programHref("dev1", "0", "p1"),
			Type:            Connect,
			DurationSeconds: 3600,
		})
		if err != nil {
			t.Fatalf("Issue() #%d error = %v", i, err)
		}
		if seen[res.Control.MRID] {
			t.Fatalf("duplicate MRID %q at iteration %d", res.Control.MRID, i)
		}
		seen[res.Control.MRID] = true
	}
}

func TestIssue_RefusesWhenPENNotConfigured(t *testing.T) {
	h := newHarness(Config{}) // no PEN
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	assertRefusal(t, err, RefusalPENNotConfigured)
	assertNoNewControl(t, h)
}

// TestNewMRID_NeverProducesAllFReservedForm proves the all-F retry in
// mrid.go actually retries: it forces the first draw to be the reserved
// all-F-prefix form (IEEE 2030.5-2018 mRIDType, "reserved for an object
// being created") and asserts the function draws again rather than
// returning it.
func TestNewMRID_NeverProducesAllFReservedForm(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()

	calls := 0
	randRead = func(b []byte) (int, error) {
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

	mrid, err := newMRID(0x00000001)
	if err != nil {
		t.Fatalf("newMRID() error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("randRead called %d times, want at least 2 (a retry after the all-F draw)", calls)
	}
	raw, err := hex.DecodeString(mrid)
	if err != nil {
		t.Fatalf("mrid %q is not hex: %v", mrid, err)
	}
	if isAllFF(raw[:12]) {
		t.Fatalf("newMRID returned the reserved all-F-prefix form: %q", mrid)
	}
}

func TestNewMRID_RandReadErrorPropagates(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()
	wantErr := errors.New("boom")
	randRead = func(b []byte) (int, error) { return 0, wantErr }

	_, err := newMRID(1)
	if !errors.Is(err, wantErr) {
		t.Fatalf("newMRID() error = %v, want %v", err, wantErr)
	}
}
