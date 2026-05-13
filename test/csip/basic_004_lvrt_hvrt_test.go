// CSIP V1.2 §8.4 — Inverter Control: LVRT/HVRT.
//
// BASIC-004 calls for a DERControl carrying opModLVRTMustTrip,
// opModLVRTMomentaryCessation, opModHVRTMustTrip, and
// opModHVRTMomentaryCessation curve references per Figure 4. None of
// those fields exist on pkg/sep2.DERControlBase today — see
// IEEE-092. The DERCurve type does exist and the procedure's
// curve-list walk leg renders correctly; the per-field assertion is
// t.Skip'd against the follow-up ticket.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.4):
//
//	Step 1 (server has DERProgram + 1 DERControl + 4 DERCurves)  ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                              ──► basicModeWalk
//	Step 3 (global /dc carries 4 ride-through curves)             ──► walkDERCurveListLVRTHVRT
//	Step 4 (DERControl carries opModLVRT*MustTrip /
//	         opModLVRT*MomentaryCessation /
//	         opModHVRT*MustTrip /
//	         opModHVRT*MomentaryCessation curve refs)             ──► t.Skip (IEEE-092)
//
// Pinned by IEEE-092 — implementation gap.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestBASIC_004_LVRTHVRT implements CSIP V1.2 §8.4.
func TestBASIC_004_LVRTHVRT(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-004-lvrt-hvrt.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			// Step 3: procedure walk reaches DERControl correctly; the
			// list shape (1 control, no mode fields on DERControlBase)
			// is the wire baseline.
			if got := list.All; got != 1 {
				t.Fatalf("[%s] DERControlList.All = %d, want 1", cipher, got)
			}
			if got := len(list.DERControl); got != 1 {
				t.Fatalf("[%s] len(DERControlList.DERControl) = %d, want 1", cipher, got)
			}

			// Global /dc carries the 4 ride-through curves (curveType
			// tagging is approximate today; see fixture doc-comment).
			var curveList sep2.DERCurveList
			if err := c.WalkLink(context.Background(), sep2.Link{Href: "/dc?l=255"}, &curveList); err != nil {
				t.Fatalf("[%s] walk /dc: %v", cipher, err)
			}
			if got := len(curveList.DERCurve); got != 4 {
				t.Fatalf("[%s] DERCurveList len = %d, want 4 (LVRT-must-trip, LVRT-momentary, HVRT-must-trip, HVRT-momentary)",
					cipher, got)
			}

			// Step 4: the per-mode opModLVRT* / opModHVRT* curve
			// references would assert against fields the public sep2
			// API does not carry today.
			t.Skip(formatGap("BASIC-004 (LVRT/HVRT)",
				"opModLVRTMustTrip / opModLVRTMomentaryCessation / opModHVRTMustTrip / opModHVRTMomentaryCessation"))
		})
}
