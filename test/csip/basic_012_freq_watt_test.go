// CSIP V1.2 §8.12 — Inverter Control: Frequency/Watt.
//
// BASIC-012 calls for a DERControl carrying opModFreqWatt referencing
// a Freq/Watt DERCurve per Figure 12. opModFreqWatt does NOT exist on
// pkg/sep2.DERControlBase today — see IEEE-092. The DERCurve type
// constant `CurveTypeOpModFreqWatt = 1` IS in sep2; opModFreqDroop
// (a uint16 droop coefficient) is on DERControlBase but is NOT the
// §8.12 curve-based mode (it is the §8.5 LFRT/HFRT droop family).
// The procedure's curve-list walk leg renders correctly; the
// per-field assertion is t.Skip'd against the follow-up ticket.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.12):
//
//	Step 1 (server has DERProgram + 1 DERControl + 1 DERCurve)    ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                               ──► basicModeWalk
//	Step 3 (global /dc carries 1 Freq/Watt curve, curveType = 1)   ──► walkSingleCurveBasic
//	Step 4 (DERControl carries opModFreqWatt curve ref)            ──► t.Skip (IEEE-092)
//
// Pinned by IEEE-092 — implementation gap.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestBASIC_012_FreqWatt implements CSIP V1.2 §8.12.
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

			curve := walkSingleCurveBasic(t, context.Background(), c,
				sep2.CurveTypeOpModFreqWatt, 1)
			if curve.MRID != "BASIC-012-CURVE" {
				t.Errorf("[%s] DERCurve.MRID = %q, want BASIC-012-CURVE", cipher, curve.MRID)
			}
			if got := len(curve.CurveData); got != 4 {
				t.Errorf("[%s] DERCurve.CurveData len = %d, want 4 (Figure 12 4-point envelope)",
					cipher, got)
			}

			t.Skip(formatGap("BASIC-012 (Freq/Watt)", "opModFreqWatt"))
		})
}
