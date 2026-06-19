// CSIP V1.2 §8.4 — Inverter Control: LVRT/HVRT.
//
// BASIC-004 proves the server renders a DERControl carrying the four
// LVRT/HVRT curve references per Figure 4, plus the four matching
// DERCurves in the global /dc store, end-to-end over chained GETs.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.4):
//
//	Step 1 (server has DERProgram + 1 DERControl + 4 DERCurves)  ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                              ──► basicModeWalk
//	Step 3 (DERControl carries opModLVRT*MustTrip /
//	         opModLVRT*MomentaryCessation /
//	         opModHVRT*MustTrip /
//	         opModHVRT*MomentaryCessation curve refs)             ──► per-field assertions
//	Step 4 (global /dc carries 4 ride-through curves)             ──► curve-list walk
//
// IEEE-092 added the four ride-through curve-ref fields to
// pkg/sep2.DERControlBase and flipped this test from SKIP to active.
package csip_test

import (
	"context"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// basic004 LVRT/HVRT curve-ref values (fixture seeds these). The
// fixture maps each ref to the corresponding DERCurve id in /dc:
// 0=LVRT-must-trip, 1=LVRT-momentary, 2=HVRT-must-trip, 3=HVRT-momentary.
const (
	basic004LVRTMustTrip           int32 = 0
	basic004LVRTMomentaryCessation int32 = 1
	basic004HVRTMustTrip           int32 = 2
	basic004HVRTMomentaryCessation int32 = 3
)

// TestBASIC_004_LVRTHVRT implements CSIP V1.2 §8.4.
func TestBASIC_004_LVRTHVRT(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-004-lvrt-hvrt.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			// Step 2: procedure walk reaches DERControl correctly.
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

			// Step 3: each LVRT/HVRT curve ref survives wire roundtrip.
			assertCurveRef(t, cipher, "opModLVRTMustTrip",
				dc.DERControlBase.OpModLVRTMustTrip, basic004LVRTMustTrip)
			assertCurveRef(t, cipher, "opModLVRTMomentaryCessation",
				dc.DERControlBase.OpModLVRTMomentaryCessation, basic004LVRTMomentaryCessation)
			assertCurveRef(t, cipher, "opModHVRTMustTrip",
				dc.DERControlBase.OpModHVRTMustTrip, basic004HVRTMustTrip)
			assertCurveRef(t, cipher, "opModHVRTMomentaryCessation",
				dc.DERControlBase.OpModHVRTMomentaryCessation, basic004HVRTMomentaryCessation)

			// Step 4: global /dc carries the 4 ride-through curves.
			var curveList sep2.DERCurveList
			if err := c.WalkLink(context.Background(), sep2.Link{Href: "/dc?l=255"}, &curveList); err != nil {
				t.Fatalf("[%s] walk /dc: %v", cipher, err)
			}
			if got := len(curveList.DERCurve); got != 4 {
				t.Fatalf("[%s] DERCurveList len = %d, want 4 (LVRT-must-trip, LVRT-momentary, HVRT-must-trip, HVRT-momentary)",
					cipher, got)
			}
		})
}
