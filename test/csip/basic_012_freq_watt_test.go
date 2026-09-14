// CSIP V1.2 Section 8.12 - Inverter Control: Frequency/Watt.
//
// BASIC-012 proves the server renders a DERControl carrying
// opModFreqWatt referencing a Freq/Watt DERCurve (curveType =
// CurveTypeOpModFreqWatt = 1) per Figure 12.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.12):
//
//	Step 1 (server has DERProgram + 1 DERControl + 1 DERCurve)    -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa -> DERProgram -> DERControl)
//	                                                               -> basicModeWalk
//	Step 3 (DERControl carries opModFreqWatt curve ref)            -> per-field assertion
//	Step 4 (global /dc carries 1 Freq/Watt curve, curveType = 1)   -> walkSingleCurveBasic
//
// #140 added opModFreqWatt to pkg/sep2.DERControlBase and flipped
// this test from SKIP to active. Note opModFreqDroop (Section 8.5 LFRT/HFRT
// droop coefficient) is unrelated to the curve-based Section 8.12 mode.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic012FreqWattRef is the opModFreqWatt curve ref the fixture seeds.
const basic012FreqWattRef int32 = 0

// TestBASIC_012_FreqWatt implements CSIP V1.2 Section 8.12.
func TestBASIC_012_FreqWatt(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-012-freq-watt.yaml",
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

			assertCurveRef(t, cipher, "opModFreqWatt",
				dc.DERControlBase.OpModFreqWatt, basic012FreqWattRef)

			curve := walkSingleCurveBasic(t, context.Background(), c,
				sep2.CurveTypeOpModFreqWatt, 1)
			if curve.MRID != "BASIC-012-CURVE" {
				t.Errorf("[%s] DERCurve.MRID = %q, want BASIC-012-CURVE", cipher, curve.MRID)
			}
			if got := len(curve.CurveData); got != 4 {
				t.Errorf("[%s] DERCurve.CurveData len = %d, want 4 (Figure 12 4-point envelope)",
					cipher, got)
			}
		})
}
