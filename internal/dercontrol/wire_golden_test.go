package dercontrol

import (
	"encoding/xml"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// The goldens pin the DERControlBase bytes a device reads. Element order is
// the sep.xsd sequence (opModConnect, opModEnergize, opModFixedPFInjectW,
// opModMaxLimW, opModTargetW), and the legacy-type goldens are identical at
// the commit before function sets existed.
func TestBuildBase_WireBytes(t *testing.T) {
	over := true
	under := false
	tests := []struct {
		name string
		req  CreateRequest
		want string
	}{
		{"legacy connect", CreateRequest{Type: Connect}, `<DERControlBase><opModConnect>true</opModConnect><opModEnergize>true</opModEnergize></DERControlBase>`},
		{"legacy disconnect", CreateRequest{Type: Disconnect}, `<DERControlBase><opModConnect>false</opModConnect><opModEnergize>false</opModEnergize></DERControlBase>`},
		{"legacy maxLimW", CreateRequest{Type: MaxLimW, MaxLimW: uint16ptr(6000)}, `<DERControlBase><opModMaxLimW>6000</opModMaxLimW></DERControlBase>`},
		{"legacy fixedPFInjectW over-excited", CreateRequest{Type: FixedPFInjectW, PowerFactor: &PowerFactorValue{Displacement: 900, Excitation: &over}}, `<DERControlBase><opModFixedPFInjectW><displacement>900</displacement><excitation>true</excitation><multiplier>-3</multiplier></opModFixedPFInjectW></DERControlBase>`},
		{"legacy fixedPFInjectW under-excited", CreateRequest{Type: FixedPFInjectW, PowerFactor: &PowerFactorValue{Displacement: 900, Excitation: &under}}, `<DERControlBase><opModFixedPFInjectW><displacement>900</displacement><excitation>false</excitation><multiplier>-3</multiplier></opModFixedPFInjectW></DERControlBase>`},
		{"legacy targetW", CreateRequest{Type: TargetW, TargetW: &sep2.ActivePower{Value: -1500, Multiplier: 1}}, `<DERControlBase><opModTargetW><multiplier>1</multiplier><value>-1500</value></opModTargetW></DERControlBase>`},
		{"two functions", CreateRequest{Functions: &sep2.DERControlBase{
			OpModMaxLimW:        pct(5000),
			OpModFixedPFInjectW: thousandths(900, false),
		}}, `<DERControlBase><opModFixedPFInjectW><displacement>900</displacement><excitation>false</excitation><multiplier>-3</multiplier></opModFixedPFInjectW><opModMaxLimW>5000</opModMaxLimW></DERControlBase>`},
		{"five functions", CreateRequest{Functions: &sep2.DERControlBase{
			OpModConnect:        boolPtr(true),
			OpModEnergize:       boolPtr(false),
			OpModMaxLimW:        pct(5000),
			OpModFixedPFInjectW: thousandths(900, true),
			OpModTargetW:        &sep2.ActivePower{Value: 500},
		}}, `<DERControlBase><opModConnect>true</opModConnect><opModEnergize>false</opModEnergize><opModFixedPFInjectW><displacement>900</displacement><excitation>true</excitation><multiplier>-3</multiplier></opModFixedPFInjectW><opModMaxLimW>5000</opModMaxLimW><opModTargetW><multiplier>0</multiplier><value>500</value></opModTargetW></DERControlBase>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, err := buildBase(tc.req)
			if err != nil {
				t.Fatalf("buildBase: %v", err)
			}
			got, err := xml.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("wire bytes:\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}
