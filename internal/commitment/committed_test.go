package commitment

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestCommitted(t *testing.T) {
	t.Parallel()
	exec := func(value int16, mult int8, reach int, dur uint32) Control {
		return Control{TargetW: &sep2.ActivePower{Value: value, Multiplier: mult}, Reach: reach, Window: Window{Duration: dur}}
	}
	tests := []struct {
		name  string
		execs []Control
		want  string // watt-seconds as an exact fraction
	}{
		{"none", nil, "0/1"},
		{"|target| x reach x duration", []Control{exec(-4000, 0, 1, 900)}, "3600000/1"},
		{"reach multiplies", []Control{exec(4000, 0, 3, 900)}, "10800000/1"},
		{"sums every control", []Control{exec(4000, 0, 1, 900), exec(-1000, 0, 2, 60)}, "3720000/1"},
		{"a positive multiplier scales up", []Control{exec(2, 2, 1, 10)}, "2000/1"},
		{"a negative multiplier stays exact", []Control{exec(5, -1, 1, 3)}, "3/2"},
		{"a control without a target adds nothing", []Control{{Reach: 1, Window: Window{Duration: 99}}, exec(1, 0, 1, 7)}, "7/1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Committed(tc.execs).String(); got != tc.want {
				t.Errorf("Committed = %s Ws, want %s", got, tc.want)
			}
		})
	}
}
