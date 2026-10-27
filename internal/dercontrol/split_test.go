package dercontrol_test

import (
	"errors"
	"math/big"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

func weightsW(ws ...int64) []*big.Rat {
	out := make([]*big.Rat, len(ws))
	for n, w := range ws {
		out[n] = big.NewRat(w, 1)
	}
	return out
}

func ap(v int16, m int8) *sep2.ActivePower { return &sep2.ActivePower{Value: v, Multiplier: m} }

// Shares are whole units of the operator's 10^y that sum to x, encoded no
// finer than y, and a share of zero units is not encoded.
func TestSplitTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		x       int64
		y       int8
		sign    int
		weights []*big.Rat
		units   []int64
		targets []*sep2.ActivePower
	}{
		{"even split", 50, 3, 1, weightsW(5000, 5000), []int64{25, 25}, []*sep2.ActivePower{ap(25, 3), ap(25, 3)}},
		{"charge is negative", 50, 3, -1, weightsW(10000, 5000, 5000), []int64{25, 13, 12},
			[]*sep2.ActivePower{ap(-25, 3), ap(-13, 3), ap(-12, 3)}},
		// 10 in thirds: 3.33 each, the one leftover unit goes to the lowest
		// index of the tied remainders.
		{"tie goes to lower index", 10, 0, 1, weightsW(1, 1, 1), []int64{4, 3, 3}, []*sep2.ActivePower{ap(4, 0), ap(3, 0), ap(3, 0)}},
		// Remainders .9 and .1: the larger takes the unit.
		{"largest remainder wins", 10, 0, 1, weightsW(19, 1), []int64{10, 0}, []*sep2.ActivePower{ap(10, 0), nil}},
		{"zero share", 3, 3, 1, weightsW(1000, 1000, 1000, 1000), []int64{1, 1, 1, 0},
			[]*sep2.ActivePower{ap(1, 3), ap(1, 3), ap(1, 3), nil}},
		// 100000 units at 10^0 does not fit int16: 10000 x 10^1 does.
		{"too large for int16 rounds up a multiplier", 100000, 0, 1, weightsW(1), []int64{100000}, []*sep2.ActivePower{ap(10000, 1)}},
		// 66667 at 10^0: 6666.7 rounds to 6667 x 10^1.
		{"rounds half away from zero", 200000, 0, 1, weightsW(2, 1), []int64{133333, 66667}, []*sep2.ActivePower{ap(13333, 1), ap(6667, 1)}},
		{"rounds charge away from zero", 65535, 0, -1, weightsW(1), []int64{65535}, []*sep2.ActivePower{ap(-6554, 1)}},
		{"fractional weights", 7, -2, 1, []*big.Rat{big.NewRat(1, 3), big.NewRat(2, 3)}, []int64{2, 5}, []*sep2.ActivePower{ap(2, -2), ap(5, -2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dercontrol.SplitTarget(tc.x, tc.y, tc.sign, tc.weights)
			if err != nil {
				t.Fatalf("SplitTarget: %v", err)
			}
			var sum int64
			for n, s := range got {
				sum += s.Units
				if s.Units != tc.units[n] {
					t.Errorf("share %d units = %d, want %d", n, s.Units, tc.units[n])
				}
				want := tc.targets[n]
				switch {
				case want == nil && s.TargetW != nil:
					t.Errorf("share %d target = %+v, want none for a zero share", n, *s.TargetW)
				case want != nil && (s.TargetW == nil || *s.TargetW != *want):
					t.Errorf("share %d target = %+v, want %+v", n, s.TargetW, *want)
				case s.TargetW != nil && s.TargetW.Multiplier < tc.y:
					t.Errorf("share %d multiplier %d is finer than the request's %d", n, s.TargetW.Multiplier, tc.y)
				}
			}
			if sum != tc.x {
				t.Errorf("units sum to %d, want x = %d", sum, tc.x)
			}
		})
	}
}

// Encoding error is at most half a unit at the operator's multiplier, and
// below 0.016 percent once the multiplier is raised.
func TestSplitTarget_EncodedWithinStatedRounding(t *testing.T) {
	t.Parallel()
	for _, x := range []int64{1, 999, 32767, 32768, 99999, 123456789} {
		got, err := dercontrol.SplitTarget(x, 0, 1, weightsW(7, 11, 13))
		if err != nil {
			t.Fatalf("x %d: %v", x, err)
		}
		for n, s := range got {
			if s.TargetW == nil {
				continue
			}
			enc := int64(s.TargetW.Value)
			for range s.TargetW.Multiplier {
				enc *= 10
			}
			diff := enc - s.Units
			if diff < 0 {
				diff = -diff
			}
			if s.TargetW.Multiplier == 0 && diff != 0 {
				t.Errorf("x %d share %d: encoded %d, want exactly %d at the operator's multiplier", x, n, enc, s.Units)
			}
			if s.TargetW.Multiplier > 0 && diff*10000 >= s.Units*16/10 {
				t.Errorf("x %d share %d: encoded %d against %d, error not below 0.016 percent", x, n, enc, s.Units)
			}
		}
	}
}

func TestSplitTarget_Refusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		x       int64
		y       int8
		sign    int
		weights []*big.Rat
		want    error
	}{
		{"zero x", 0, 0, 1, weightsW(1), dercontrol.ErrSplitRequest},
		{"negative x", -5, 0, 1, weightsW(1), dercontrol.ErrSplitRequest},
		{"y above 9", 1, 10, 1, weightsW(1), dercontrol.ErrSplitRequest},
		{"y below -9", 1, -10, 1, weightsW(1), dercontrol.ErrSplitRequest},
		{"no sign", 1, 0, 0, weightsW(1), dercontrol.ErrSplitRequest},
		{"no weights", 1, 0, 1, nil, dercontrol.ErrSplitRequest},
		{"zero weight", 1, 0, 1, weightsW(1, 0), dercontrol.ErrSplitRequest},
		{"beyond 10^9 multiplier", 40000, 9, 1, weightsW(1), dercontrol.ErrShareUnencodable},
	} {
		if _, err := dercontrol.SplitTarget(tc.x, tc.y, tc.sign, tc.weights); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}
