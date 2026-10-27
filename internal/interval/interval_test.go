package interval

import (
	"math"
	"testing"
)

func TestEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    int64
		duration uint32
		want     int64
	}{
		{"zero duration ends where it starts", 100, 0, 100},
		{"adds the duration", 100, 600, 700},
		{"no wrap at the largest duration", 10, math.MaxUint32, 10 + math.MaxUint32},
	} {
		if got := End(tc.start, tc.duration); got != tc.want {
			t.Errorf("%s: End(%d, %d) = %d, want %d", tc.name, tc.start, tc.duration, got, tc.want)
		}
	}
}

// One overlap rule for every caller: half open, and an empty interval
// overlaps nothing, even one that holds its start.
func TestOverlaps(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		aStart, aEnd, bStart, bEnd int64
		want                       bool
	}{
		{"shared second", 0, 10, 9, 20, true},
		{"back to back", 0, 10, 10, 20, false},
		{"contained", 0, 100, 10, 20, true},
		{"disjoint", 0, 10, 20, 30, false},
		{"empty inside a wider one", 5, 5, 0, 10, false},
		{"wider one around an empty", 0, 10, 5, 5, false},
		{"two empties at one point", 5, 5, 5, 5, false},
	} {
		if got := Overlaps(tc.aStart, tc.aEnd, tc.bStart, tc.bEnd); got != tc.want {
			t.Errorf("%s: Overlaps(%d, %d, %d, %d) = %v, want %v", tc.name, tc.aStart, tc.aEnd, tc.bStart, tc.bEnd, got, tc.want)
		}
	}
}
