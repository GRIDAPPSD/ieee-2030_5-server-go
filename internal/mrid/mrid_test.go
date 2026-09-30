package mrid

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func fixedRand(fill byte) func([]byte) (int, error) {
	return func(b []byte) (int, error) {
		for i := range b {
			b[i] = fill
		}
		return len(b), nil
	}
}

func TestNew_WithPEN_LowBitsArePEN(t *testing.T) {
	pen := uint32(0xABCD1234)
	got, err := New(fixedRand(0x01), &pen)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatalf("mRID %q is not hex: %v", got, err)
	}
	if gotPEN := binary.BigEndian.Uint32(raw[12:]); gotPEN != pen {
		t.Fatalf("low 32 bits = %#x, want %#x", gotPEN, pen)
	}
}

func TestNew_NilPEN_AllBitsRandom(t *testing.T) {
	calls := 0
	randRead := func(b []byte) (int, error) {
		calls++
		if len(b) != 16 {
			t.Fatalf("randRead asked for %d bytes, want 16 with no PEN", len(b))
		}
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	}
	got, err := New(randRead, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("randRead called %d times, want 1 (no retry needed)", calls)
	}
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
}

func TestNew_ZeroPEN_TreatedAsNil(t *testing.T) {
	// 0 is IANA-reserved; internal/dercontrol.Config already treats it as
	// unset before it ever reaches a mint call, and this package repeats the
	// same rule so a caller that has not normalized still gets full-width
	// randomness rather than an embedded zero PEN.
	zero := uint32(0)
	calls := 0
	randRead := func(b []byte) (int, error) {
		calls++
		if len(b) != 16 {
			t.Fatalf("randRead asked for %d bytes, want 16 for a zero PEN", len(b))
		}
		return len(b), nil
	}
	if _, err := New(randRead, &zero); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("randRead called %d times, want 1", calls)
	}
}

func TestNew_WithPEN_AllFRetryChecksOnlyTheRandomSegment(t *testing.T) {
	pen := uint32(1)
	calls := 0
	randRead := func(b []byte) (int, error) {
		calls++
		fill := byte(0x01)
		if calls == 1 {
			fill = 0xFF
		}
		for i := range b {
			b[i] = fill
		}
		return len(b), nil
	}
	mrid, err := New(randRead, &pen)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("randRead called %d times, want at least 2 (a retry after the all-F draw)", calls)
	}
	raw, err := hex.DecodeString(mrid)
	if err != nil {
		t.Fatalf("mRID %q is not hex: %v", mrid, err)
	}
	if IsAllFF(raw[:12]) {
		t.Fatalf("New returned the reserved all-F-prefix form: %q", mrid)
	}
}

func TestNew_NilPEN_AllFRetryChecksTheFullValue(t *testing.T) {
	calls := 0
	randRead := func(b []byte) (int, error) {
		calls++
		fill := byte(0x02)
		if calls == 1 {
			fill = 0xFF
		}
		for i := range b {
			b[i] = fill
		}
		return len(b), nil
	}
	mrid, err := New(randRead, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("randRead called %d times, want at least 2 (a retry after the all-F draw)", calls)
	}
	if mrid == strings.Repeat("F", 32) {
		t.Fatalf("New returned the reserved all-F form: %q", mrid)
	}
}

func TestNew_RandReadErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	randRead := func(b []byte) (int, error) { return 0, wantErr }

	if _, err := New(randRead, nil); !errors.Is(err, wantErr) {
		t.Fatalf("New(nil pen) error = %v, want %v", err, wantErr)
	}
	pen := uint32(1)
	if _, err := New(randRead, &pen); !errors.Is(err, wantErr) {
		t.Fatalf("New(pen) error = %v, want %v", err, wantErr)
	}
}

func TestNew_SecondDrawErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom on retry")
	calls := 0
	randRead := func(b []byte) (int, error) {
		calls++
		if calls == 1 {
			for i := range b {
				b[i] = 0xFF
			}
			return len(b), nil
		}
		return 0, wantErr
	}
	if _, err := New(randRead, nil); !errors.Is(err, wantErr) {
		t.Fatalf("New() error = %v, want %v", err, wantErr)
	}
	if calls != 2 {
		t.Fatalf("randRead called %d times, want exactly 2", calls)
	}
}

func TestIsAllFF(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"all FF", []byte{0xFF, 0xFF, 0xFF}, true},
		{"one byte off", []byte{0xFF, 0x01, 0xFF}, false},
		{"empty", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAllFF(tc.b); got != tc.want {
				t.Errorf("IsAllFF(%v) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}
