// CSIP V1.2 Section 8.15 - Advanced Inverter Control: composed modes.
//
// BASIC-015 is the first procedure that exercises a DERControl
// carrying multiple opMod* fields at once. The procedure validates
// that the server's DERControl store accepts and round-trips a
// composed payload - both curve-reference modes (Volt/Var,
// Volt/Watt, Freq/Watt) AND immediate-control modes (Connect,
// Energize, Fixed PF, Limit Max Active P) on the same DERControl -
// and that the global /dc store renders the heterogeneous
// curve-type set the composed payload references.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.15):
//
//	Step 1 (server has DERProgram + 1 DERControl composing multiple
//	         opMod* fields, plus 3 DERCurves of distinct curveType
//	         in /dc)                                          -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa -> DERProgram ->
//	         DERControl)                                      -> basicModeWalk
//	Step 3 (DERControl carries opModVoltVar, opModVoltWatt,
//	         opModFreqWatt curve refs simultaneously)         -> assertCurveRef x 3
//	Step 4 (DERControl carries opModConnect, opModEnergize,
//	         opModFixedPFInjectW, opModMaxLimW inline values
//	         simultaneously)                                  -> immediate-mode block
//	Step 5 (global /dc carries 3 curves spanning curveType 11,
//	         0, 12 - V-Var, F-Watt, V-Watt)                   -> /dc walk
//
// Standalone ticket (not folded into #135 per backlog) because
// BASIC-015 is the first composed-mode procedure: any DERControl
// store-schema bug that surfaces around multi-mode payload encoding
// shows up here in isolation rather than masking a whole per-mode
// batch. Run under both GCM and CCM cipher modes - same shape as
// every #135 sibling.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic015 curve-ref values the fixture seeds; each must survive
// the wire roundtrip exactly. The mapping is:
//
//	opModVoltVar  -> der_curves[0] (curveType 11, V-Var)
//	opModVoltWatt -> der_curves[1] (curveType 12, V-Watt)
//	opModFreqWatt -> der_curves[2] (curveType 0, F-Watt)
const (
	basic015VoltVarRef  int32 = 0
	basic015VoltWattRef int32 = 1
	basic015FreqWattRef int32 = 2
)

// basic015 immediate-control values per the fixture.
const (
	basic015FixedPFDisplacement uint16       = 950
	basic015MaxLimWValue        sep2.PerCent = 5000
)

// basic015ExpectedCurveCount is the count of curves the fixture
// seeds into /dc. One per curve-ref mode in the composed DERControl.
const basic015ExpectedCurveCount = 3

// TestBASIC_015_ComposedModes implements CSIP V1.2 Section 8.15.
func TestBASIC_015_ComposedModes(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-015-composed-modes.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			// Steps 2-3: exactly one DERControl renders, carrying the
			// composed DERControlBase.
			if got := list.All; got != 1 {
				t.Fatalf("[%s] DERControlList.All = %d, want 1", cipher, got)
			}
			if got := len(list.DERControl); got != 1 {
				t.Fatalf("[%s] len(DERControlList.DERControl) = %d, want 1", cipher, got)
			}
			dc := list.DERControl[0]
			if dc.DERControlBase == nil {
				t.Fatalf("[%s] DERControl.DERControlBase is nil - composed payload dropped", cipher)
			}
			base := dc.DERControlBase

			// Step 3: all three curve refs survive the roundtrip.
			assertCurveRef(t, cipher, "opModVoltVar", base.OpModVoltVar, basic015VoltVarRef)
			assertCurveRef(t, cipher, "opModVoltWatt", base.OpModVoltWatt, basic015VoltWattRef)
			assertCurveRef(t, cipher, "opModFreqWatt", base.OpModFreqWatt, basic015FreqWattRef)

			// Step 4: every immediate-control field is present and
			// carries the seeded value. The composition assertion is
			// that all of these coexist with the curve refs above on
			// the same DERControl - none are dropped or aliased.
			if base.OpModConnect == nil {
				t.Fatalf("[%s] DERControlBase.OpModConnect is nil - composed payload lost connect flag", cipher)
			}
			if !*base.OpModConnect {
				t.Errorf("[%s] DERControlBase.OpModConnect = false, want true", cipher)
			}
			if base.OpModEnergize == nil {
				t.Fatalf("[%s] DERControlBase.OpModEnergize is nil - composed payload lost energize flag", cipher)
			}
			if !*base.OpModEnergize {
				t.Errorf("[%s] DERControlBase.OpModEnergize = false, want true", cipher)
			}
			if base.OpModFixedPFInjectW == nil {
				t.Fatalf("[%s] DERControlBase.OpModFixedPFInjectW is nil - composed payload lost fixed-pf", cipher)
			}
			if got := base.OpModFixedPFInjectW.Displacement; got != basic015FixedPFDisplacement {
				t.Errorf("[%s] OpModFixedPFInjectW.Displacement = %d, want %d",
					cipher, got, basic015FixedPFDisplacement)
			}
			if !base.OpModFixedPFInjectW.Excitation {
				t.Errorf("[%s] OpModFixedPFInjectW.Excitation = false, want true", cipher)
			}
			if base.OpModMaxLimW == nil {
				t.Fatalf("[%s] DERControlBase.OpModMaxLimW is nil - composed payload lost max-lim", cipher)
			}
			if got := *base.OpModMaxLimW; got != basic015MaxLimWValue {
				t.Errorf("[%s] OpModMaxLimW = %d, want %d",
					cipher, got, basic015MaxLimWValue)
			}

			// Step 5: the global /dc list renders three curves of
			// distinct curveType. walkSingleCurveBasic only handles
			// single-curve lists, so we do the /dc walk inline.
			var curves sep2.DERCurveList
			if err := c.WalkLink(context.Background(),
				sep2.Link{Href: "/dc?l=255"}, &curves); err != nil {
				t.Fatalf("[%s] walk /dc: %v", cipher, err)
			}
			if got := len(curves.DERCurve); got != basic015ExpectedCurveCount {
				t.Fatalf("[%s] DERCurveList len = %d, want %d",
					cipher, got, basic015ExpectedCurveCount)
			}

			// Each curveType is present exactly once across the three
			// curves. The /dc handler is not type-sorted, so check by
			// counting hits rather than positional indexing.
			wantTypes := map[uint8]bool{
				sep2.CurveTypeOpModVoltVar:  false, // 11
				sep2.CurveTypeOpModFreqWatt: false, // 0
				sep2.CurveTypeOpModVoltWatt: false, // 12
			}
			for _, cv := range curves.DERCurve {
				if _, ok := wantTypes[cv.CurveType]; !ok {
					t.Errorf("[%s] unexpected DERCurve.CurveType = %d",
						cipher, cv.CurveType)
					continue
				}
				if wantTypes[cv.CurveType] {
					t.Errorf("[%s] DERCurve.CurveType %d seen twice",
						cipher, cv.CurveType)
					continue
				}
				wantTypes[cv.CurveType] = true
			}
			for ct, seen := range wantTypes {
				if !seen {
					t.Errorf("[%s] DERCurveList missing curveType %d", cipher, ct)
				}
			}
		})
}
