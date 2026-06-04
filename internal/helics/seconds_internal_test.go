//go:build helics

package helics

import (
	"math"
	"testing"
	"time"
)

// TestSecondsToDuration_Saturation locks the M3 overflow regression and
// the M4 HELICS_TIME_MAXTIME / NaN edge cases. The HELICS C header
// publishes cHelicsBigNumber as ~9.22e18 seconds (the value HELICS
// returns to signal "no further updates" on requestTime); multiplied by
// 1e9 to convert to nanoseconds it is far beyond what time.Duration's
// int64 field can represent. The wrapper saturates rather than relying
// on the implementation-defined float64->int64 cast.
func TestSecondsToDuration_Saturation(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want time.Duration
	}{
		{"zero", 0, 0},
		{"one second", 1, time.Second},
		{"hundred ms", 0.1, 100 * time.Millisecond},
		{"helics big number positive", 9.2233720368547758e18, time.Duration(math.MaxInt64)},
		{"helics big number negative", -9.2233720368547758e18, time.Duration(math.MinInt64)},
		{"positive infinity", math.Inf(1), time.Duration(math.MaxInt64)},
		{"negative infinity", math.Inf(-1), time.Duration(math.MinInt64)},
		{"NaN", math.NaN(), 0},
		// Just below the saturation threshold should NOT saturate.
		{"large but representable", 1.0, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := secondsToDuration(tc.in)
			if got != tc.want {
				t.Fatalf("secondsToDuration(%g): got %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
