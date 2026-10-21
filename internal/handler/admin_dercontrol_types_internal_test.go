package handler

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

func TestControlTypeName_LegacyShapesOnlyElseCombined(t *testing.T) {
	tr, fa := true, false
	pct := sep2.PerCent(10)
	pf := &sep2.FixedPowerFactor{Displacement: 900}
	tw := &sep2.ActivePower{Value: 1}
	tests := []struct {
		name string
		base *sep2.DERControlBase
		want string
	}{
		{"nil", nil, ""},
		{"empty", &sep2.DERControlBase{}, "other"},
		{"connect pair", &sep2.DERControlBase{OpModConnect: &tr, OpModEnergize: &tr}, "connect"},
		{"disconnect pair", &sep2.DERControlBase{OpModConnect: &fa, OpModEnergize: &fa}, "disconnect"},
		{"connect alone", &sep2.DERControlBase{OpModConnect: &tr}, "combined"},
		{"energize alone", &sep2.DERControlBase{OpModEnergize: &tr}, "combined"},
		{"connect true energize false", &sep2.DERControlBase{OpModConnect: &tr, OpModEnergize: &fa}, "combined"},
		{"maxLimW", &sep2.DERControlBase{OpModMaxLimW: &pct}, "maxLimW"},
		{"fixedPFInjectW", &sep2.DERControlBase{OpModFixedPFInjectW: pf}, "fixedPFInjectW"},
		{"targetW", &sep2.DERControlBase{OpModTargetW: tw}, "targetW"},
		{"two functions", &sep2.DERControlBase{OpModMaxLimW: &pct, OpModFixedPFInjectW: pf}, "combined"},
		{"connect pair and maxLimW", &sep2.DERControlBase{OpModConnect: &tr, OpModEnergize: &tr, OpModMaxLimW: &pct}, "combined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dercontrol.TypeName(tc.base); got != tc.want {
				t.Errorf("TypeName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewDERControlView_EndedAtTheLastSecond(t *testing.T) {
	ctrl := sep2.DERControl{}
	ctrl.Interval = &sep2.DateTimeInterval{Start: 1000, Duration: 100}
	for _, tc := range []struct {
		now  int64
		want bool
	}{{1099, false}, {1100, true}, {1101, true}} {
		v := newDERControlView(dercontrol.Scope{}, ctrl, dercontrol.LifecycleRecord{}, tc.now)
		if v.Ended != tc.want {
			t.Errorf("now %d: ended = %v, want %v", tc.now, v.Ended, tc.want)
		}
	}
}
