// CSIP V1.2 §8.8 — Inverter Control: Fixed Power Factor.
//
// BASIC-008 proves the server renders a Fixed-PF DERControl carrying
// opModFixedPFInjectW with displacement+excitation per Figure 8 — an
// immediate control mode with no DERCurve reference.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.8):
//
//	Step 1 (server has DERProgram + 1 DERControl with
//	         opModFixedPFInjectW)                              ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                            ──► basicModeWalk
//	Step 3 (DERControl.OpModFixedPFInjectW.Displacement = 950,
//	         Excitation = true)                                 ──► assertFixedPF
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// basic008Displacement is the per-fixture displacement value (pf 0.95
// leading == 950 in tenths-of-percent encoding).
const basic008Displacement uint16 = 950

// TestBASIC_008_FixedPF implements CSIP V1.2 §8.8.
func TestBASIC_008_FixedPF(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-008-fixed-pf.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			if got := list.All; got != 1 {
				t.Fatalf("[%s] DERControlList.All = %d, want 1", cipher, got)
			}
			if got := len(list.DERControl); got != 1 {
				t.Fatalf("[%s] len(DERControlList.DERControl) = %d, want 1", cipher, got)
			}
			dc := list.DERControl[0]
			if dc.DERControlBase == nil {
				t.Fatalf("[%s] DERControl.DERControlBase is nil", cipher)
			}
			pf := dc.DERControlBase.OpModFixedPFInjectW
			if pf == nil {
				t.Fatalf("[%s] DERControl.OpModFixedPFInjectW is nil — fixture dropped", cipher)
			}
			if pf.Displacement != basic008Displacement {
				t.Errorf("[%s] OpModFixedPFInjectW.Displacement = %d, want %d",
					cipher, pf.Displacement, basic008Displacement)
			}
			if !pf.Excitation {
				t.Errorf("[%s] OpModFixedPFInjectW.Excitation = false, want true (pf 0.95 leading)", cipher)
			}
			if pf.Multiplier != 0 {
				t.Errorf("[%s] OpModFixedPFInjectW.Multiplier = %d, want 0", cipher, pf.Multiplier)
			}
		})
}
