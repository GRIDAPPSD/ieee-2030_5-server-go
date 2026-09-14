// CSIP V1.2 Section 8.6 - Inverter Control: Volt/Var.
//
// BASIC-006 proves the server renders a Volt/Var DERControl carrying
// an opModVoltVar curve reference plus the corresponding DERCurve
// (curveType = CurveTypeOpModVoltVar = 0) end-to-end over chained GETs.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.6):
//
//	Step 1 (server has DERProgram + 1 DERControl with opModVoltVar
//	         curve reference, plus 1 DERCurve in /dc)        -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa -> DERProgram -> DERControl)
//	                                                          -> basicModeWalk
//	Step 3 (DERControl.OpModVoltVar matches fixture int32)    -> assertVoltVarRef
//	Step 4 (global /dc carries 1 Volt/Var curve with the seeded
//	         CurveData round-tripped)                         -> walkSingleCurveBasic
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic006VoltVarRef is the opModVoltVar int32 the fixture seeds
// (a reference to the DERCurve with id "0"). The procedure asserts
// this exact value round-trips on the wire.
const basic006VoltVarRef int32 = 0

// TestBASIC_006_VoltVar implements CSIP V1.2 Section 8.6.
func TestBASIC_006_VoltVar(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-006-volt-var.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			// Step 3: opModVoltVar field survives wire roundtrip.
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
			if dc.DERControlBase.OpModVoltVar == nil {
				t.Fatalf("[%s] DERControl.OpModVoltVar is nil - fixture dropped", cipher)
			}
			if got := *dc.DERControlBase.OpModVoltVar; got != basic006VoltVarRef {
				t.Errorf("[%s] DERControl.OpModVoltVar = %d, want %d",
					cipher, got, basic006VoltVarRef)
			}

			// Step 4: global /dc carries the Volt/Var curve.
			curve := walkSingleCurveBasic(t, context.Background(), c, sep2.CurveTypeOpModVoltVar, 1)
			if curve.MRID != "BASIC-006-CURVE" {
				t.Errorf("[%s] DERCurve.MRID = %q, want BASIC-006-CURVE", cipher, curve.MRID)
			}
			if got := len(curve.CurveData); got != 4 {
				t.Fatalf("[%s] DERCurve.CurveData len = %d, want 4 (deadband 4-point curve)",
					cipher, got)
			}
			// Spot-check the deadband: x=9600 -> y=0 (lower deadband
			// edge) and x=10400 -> y=0 (upper deadband edge). The full
			// 4-point assertion would couple to figure 6 numerics; the
			// spot check is enough to confirm wire-fidelity.
			if curve.CurveData[1].XValue != 9600 || curve.CurveData[1].YValue != 0 {
				t.Errorf("[%s] CurveData[1] = (%d,%d), want (9600,0)",
					cipher, curve.CurveData[1].XValue, curve.CurveData[1].YValue)
			}
			if curve.CurveData[2].XValue != 10400 || curve.CurveData[2].YValue != 0 {
				t.Errorf("[%s] CurveData[2] = (%d,%d), want (10400,0)",
					cipher, curve.CurveData[2].XValue, curve.CurveData[2].YValue)
			}
		})
}
