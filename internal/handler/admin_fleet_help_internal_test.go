package handler

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// TestExportPositive_HelpMapping pins every mapping the DERMS tab's sign help
// names (the 2018 section of SIGN_HELP_SECTIONS in the frontend's lib/fleet.ts).
// want is the export-positive value for a raw value of 100 (-100 for the
// negative-value row); mapped false is "direction unknown".
func TestExportPositive_HelpMapping(t *testing.T) {
	t.Parallel()
	fwd, rev := sep2.FlowDirectionForward, sep2.FlowDirectionReverse
	cases := []struct {
		name    string
		edition SEP2Edition
		isDER   bool
		flow    *uint8
		raw     float64
		want    float64
		mapped  bool
	}{
		{"2018 forward is import", Edition2018, true, &fwd, 100, -100, true},
		{"2018 reverse is export", Edition2018, true, &rev, 100, 100, true},
		{"2018 net is unknown", Edition2018, true, u8(flowDirectionNet), 100, 100, false},
		{"no flowDirection is unknown", Edition2018, true, nil, 100, 100, false},
		{"none with 0 W is a reading", Edition2018, true, u8(flowDirectionNone), 0, 0, true},
		{"none with a value is unknown", Edition2018, true, u8(flowDirectionNone), 7, 7, false},
		{"2018 negative under forward is kept", Edition2018, true, &fwd, -100, -100, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mapped := exportPositive(tc.raw, tc.flow, tc.edition, tc.isDER)
			if got != tc.want || mapped != tc.mapped {
				t.Errorf("exportPositive = (%v, %v), want (%v, %v)", got, mapped, tc.want, tc.mapped)
			}
		})
	}
}
