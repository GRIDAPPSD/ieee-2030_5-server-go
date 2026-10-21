package bootfixture_test

import (
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// TestBuildDERControlBaseRefusesWhatTheCatalogRefuses pins that the fixture
// builds through the control catalog: a value the admin routes refuse is
// refused at boot, in the admin routes' wording, under the fixture's own key.
func TestBuildDERControlBaseRefusesWhatTheCatalogRefuses(t *testing.T) {
	t.Parallel()

	multiplier := func(m int8) *bootfixture.ActivePowerSpec {
		return &bootfixture.ActivePowerSpec{Multiplier: m, Value: 1}
	}
	tests := []struct {
		name string
		spec bootfixture.DERControlBaseSpec
		want string
	}{
		{"target_w multiplier above 9", bootfixture.DERControlBaseSpec{OpModTargetW: multiplier(10)},
			"op_mod_target_w: targetW.multiplier: must be -9 to 9"},
		{"target_w multiplier below -9", bootfixture.DERControlBaseSpec{OpModTargetW: multiplier(-10)},
			"op_mod_target_w: targetW.multiplier: must be -9 to 9"},
		{"max_lim_w above 10000", bootfixture.DERControlBaseSpec{OpModMaxLimW: ptr(sep2.PerCent(10001))},
			"op_mod_max_lim_w: maxLimW: must be 0 to 10000"},
		{"fixed_w below -10000", bootfixture.DERControlBaseSpec{OpModFixedW: ptr(sep2.SignedPerCent(-10001))},
			"op_mod_fixed_w: functions.opModFixedW: must be -10000 to 10000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := bootfixture.BuildDERControlBase(tt.spec)
			if err == nil {
				t.Fatalf("BuildDERControlBase accepted %s", tt.name)
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err.Error(), tt.want)
			}
		})
	}

	// The wording is the catalog's own, so the two cannot drift.
	_, err := bootfixture.BuildDERControlBase(bootfixture.DERControlBaseSpec{OpModTargetW: multiplier(10)})
	var ref *dercontrol.RefusalError
	if !errors.As(err, &ref) || ref.Detail == nil {
		t.Fatalf("error %v does not wrap the catalog's *RefusalError with a Detail", err)
	}
	if ref.Detail.Message != "targetW.multiplier: must be -9 to 9" {
		t.Errorf("Detail.Message = %q", ref.Detail.Message)
	}
}

func ptr[T any](v T) *T { return &v }
