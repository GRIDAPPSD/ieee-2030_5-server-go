// CSIP V1.2 §8.5 — Inverter Control: LFRT/HFRT.
//
// BASIC-005 calls for a DERControl carrying opModLFRTMustTrip and
// opModHFRTMustTrip curve references per Figure 5. Neither field
// exists on pkg/sep2.DERControlBase today — see IEEE-092. The DERCurve
// type does exist and the procedure's curve-list walk leg renders;
// the per-field assertion is t.Skip'd against the follow-up ticket.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.5):
//
//	Step 1 (server has DERProgram + 1 DERControl + 2 DERCurves)  ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                              ──► basicModeWalk
//	Step 3 (global /dc carries 2 ride-through curves)             ──► walkDERCurveListLFRTHFRT
//	Step 4 (DERControl carries opModLFRTMustTrip and
//	         opModHFRTMustTrip curve refs)                        ──► t.Skip (IEEE-092)
//
// Pinned by IEEE-092 — implementation gap.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestBASIC_005_LFRTHFRT implements CSIP V1.2 §8.5.
func TestBASIC_005_LFRTHFRT(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-005-lfrt-hfrt.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList) {
			t.Helper()

			if got := list.All; got != 1 {
				t.Fatalf("[%s] DERControlList.All = %d, want 1", cipher, got)
			}
			if got := len(list.DERControl); got != 1 {
				t.Fatalf("[%s] len(DERControlList.DERControl) = %d, want 1", cipher, got)
			}

			var curveList sep2.DERCurveList
			if err := c.WalkLink(context.Background(), sep2.Link{Href: "/dc?l=255"}, &curveList); err != nil {
				t.Fatalf("[%s] walk /dc: %v", cipher, err)
			}
			if got := len(curveList.DERCurve); got != 2 {
				t.Fatalf("[%s] DERCurveList len = %d, want 2 (LFRT-must-trip, HFRT-must-trip)",
					cipher, got)
			}

			t.Skip(formatGap("BASIC-005 (LFRT/HFRT)",
				"opModLFRTMustTrip / opModHFRTMustTrip"))
		})
}
