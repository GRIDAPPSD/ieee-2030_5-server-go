package commitment

import "testing"

// TestMagnitudeSumExceeds_ReconcilesMultipliers proves the comparison
// scales terms at different multipliers to a common exponent by
// multiplying up, never by dividing, so a value like 2^53+1 (which rounds
// to 2^53 in a float64) is still told apart from 2^53.
func TestMagnitudeSumExceeds_ReconcilesMultipliers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		terms []scaledTerm
		bound scaledTerm
		want  bool
	}{
		{
			name:  "equal at different multipliers is not exceeding",
			terms: []scaledTerm{{value: 5, multiplier: 2, factor: 1}}, // 5 * 10^2 = 500
			bound: scaledTerm{value: 500, multiplier: 0, factor: 1},   // 500 * 10^0 = 500
			want:  false,
		},
		{
			name:  "one unit over at a higher multiplier does exceed",
			terms: []scaledTerm{{value: 501, multiplier: 0, factor: 1}},
			bound: scaledTerm{value: 5, multiplier: 2, factor: 1}, // bound = 500
			want:  true,
		},
		{
			name: "a sum of terms at mixed multipliers reconciles exactly",
			terms: []scaledTerm{
				{value: 1, multiplier: 3, factor: 1}, // 1000
				{value: 200, multiplier: 0, factor: 1},
				{value: 3, multiplier: 1, factor: 10}, // 3*10*10 = 300
			}, // sum = 1000 + 200 + 300 = 1500
			bound: scaledTerm{value: 1500, multiplier: 0, factor: 1},
			want:  false, // equal, not exceeding
		},
		{
			name: "the same sum one unit past the bound does exceed",
			terms: []scaledTerm{
				{value: 1, multiplier: 3, factor: 1},
				{value: 200, multiplier: 0, factor: 1},
				{value: 3, multiplier: 1, factor: 10},
			},
			bound: scaledTerm{value: 1499, multiplier: 0, factor: 1},
			want:  true,
		},
		{
			name:  "a float64 would round these to equal; big.Int must not",
			terms: []scaledTerm{{value: (1 << 53) + 1, multiplier: 0, factor: 1}},
			bound: scaledTerm{value: 1 << 53, multiplier: 0, factor: 1},
			want:  true,
		},
		{
			name:  "negative values compare by magnitude",
			terms: []scaledTerm{{value: -500, multiplier: 0, factor: 1}},
			bound: scaledTerm{value: 500, multiplier: 0, factor: 1},
			want:  false,
		},
		{
			name:  "a bound factor scales the bound, mirroring the Wh to Ws conversion",
			terms: []scaledTerm{{value: 3601, multiplier: 0, factor: 1}},
			bound: scaledTerm{value: 1, multiplier: 0, factor: 3600}, // 1 * 3600 = 3600
			want:  true,                                              // 3601 > 3600
		},
		{
			name:  "the same bound factor, one unit under, does not exceed",
			terms: []scaledTerm{{value: 3600, multiplier: 0, factor: 1}},
			bound: scaledTerm{value: 1, multiplier: 0, factor: 3600},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := magnitudeSumExceeds(tc.terms, tc.bound); got != tc.want {
				t.Errorf("magnitudeSumExceeds(%+v, %+v) = %v, want %v", tc.terms, tc.bound, got, tc.want)
			}
		})
	}
}
