// CSIP V1.2 Section 8.11 - Inverter Control: Volt/Watt.
//
// BASIC-011 proves the server renders a DERControl carrying
// opModVoltWatt referencing a Volt/Watt DERCurve (curveType =
// CurveTypeOpModVoltWatt = 3) per Figure 11.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.11):
//
//	Step 1 (server has DERProgram + 1 DERControl + 1 DERCurve)    -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa -> DERProgram -> DERControl)
//	                                                               -> basicModeWalk
//	Step 3 (DERControl carries opModVoltWatt curve ref)            -> per-field assertion
//	Step 4 (global /dc carries 1 Volt/Watt curve, curveType = 3)   -> walkSingleCurveBasic
//
// #140 added opModVoltWatt to pkg/sep2.DERControlBase and flipped
// this test from SKIP to active.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic011VoltWattRef is the opModVoltWatt curve ref the fixture seeds.
const basic011VoltWattRef int32 = 0

// TestBASIC_011_VoltWatt implements CSIP V1.2 Section 8.11.
func TestBASIC_011_VoltWatt(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-011-volt-watt.yaml",
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

			assertCurveRef(t, cipher, "opModVoltWatt",
				dc.DERControlBase.OpModVoltWatt, basic011VoltWattRef)

			curve := walkSingleCurveBasic(t, context.Background(), c,
				sep2.CurveTypeOpModVoltWatt, 1)
			if curve.MRID != "BASIC-011-CURVE" {
				t.Errorf("[%s] DERCurve.MRID = %q, want BASIC-011-CURVE", cipher, curve.MRID)
			}
			if got := len(curve.CurveData); got != 4 {
				t.Errorf("[%s] DERCurve.CurveData len = %d, want 4 (Figure 11 4-point envelope)",
					cipher, got)
			}
		})
}
