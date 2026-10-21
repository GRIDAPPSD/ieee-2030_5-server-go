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
