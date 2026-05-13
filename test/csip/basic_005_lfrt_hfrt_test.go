// CSIP V1.2 §8.5 — Inverter Control: LFRT/HFRT.
//
// BASIC-005 proves the server renders a DERControl carrying
// opModLFRTMustTrip and opModHFRTMustTrip curve references per Figure
// 5, plus the two matching DERCurves in the global /dc store.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.5):
//
//	Step 1 (server has DERProgram + 1 DERControl + 2 DERCurves)  ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                              ──► basicModeWalk
//	Step 3 (DERControl carries opModLFRTMustTrip and
//	         opModHFRTMustTrip curve refs)                        ──► per-field assertions
//	Step 4 (global /dc carries 2 ride-through curves)             ──► curve-list walk
//
// IEEE-092 added the two ride-through curve-ref fields and flipped
// this test from SKIP to active.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// basic005 LFRT/HFRT curve-ref values (fixture seeds these). Maps
// to der_curves: 0=LFRT-must-trip, 1=HFRT-must-trip.
const (
	basic005LFRTMustTrip int32 = 0
	basic005HFRTMustTrip int32 = 1
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
			dc := list.DERControl[0]
			if dc.DERControlBase == nil {
				t.Fatalf("[%s] DERControl.DERControlBase is nil", cipher)
			}

			assertCurveRef(t, cipher, "opModLFRTMustTrip",
				dc.DERControlBase.OpModLFRTMustTrip, basic005LFRTMustTrip)
			assertCurveRef(t, cipher, "opModHFRTMustTrip",
				dc.DERControlBase.OpModHFRTMustTrip, basic005HFRTMustTrip)

			var curveList sep2.DERCurveList
			if err := c.WalkLink(context.Background(), sep2.Link{Href: "/dc?l=255"}, &curveList); err != nil {
				t.Fatalf("[%s] walk /dc: %v", cipher, err)
			}
			if got := len(curveList.DERCurve); got != 2 {
				t.Fatalf("[%s] DERCurveList len = %d, want 2 (LFRT-must-trip, HFRT-must-trip)",
					cipher, got)
			}
		})
}
