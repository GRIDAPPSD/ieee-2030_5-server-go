// CSIP V1.2 §8.10 — Inverter Control: Limit Max Active Power Mode.
//
// BASIC-010 proves the server renders an immediate-control DERControl
// carrying opModMaxLimW = 5 kW per Figure 10. No DERCurve reference.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.10):
//
//	Step 1 (server has DERProgram + 1 DERControl with
//	         opModMaxLimW = 5 kW)                              ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                            ──► basicModeWalk
//	Step 3 (DERControl.OpModMaxLimW.Value = 5000,
//	         Multiplier = 0)                                    ──► assertMaxLimW
//
// Run under both GCM and CCM cipher modes.
//
// Per matrix: BASIC-013 / BASIC-014 (Set Active Power %, Set Active
// Power Watts) are DER-Client-only and skipped from the Server
// roadmap. BASIC-010 is the Server-side "limit" mode; it maps to
// opModMaxLimW (already in pkg/sep2.DERControlBase).
package csip_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic010LimitValue is the opModMaxLimW value the fixture seeds (5 kW).
const basic010LimitValue int64 = 5000

// TestBASIC_010_LimitMaxActiveP implements CSIP V1.2 §8.10.
func TestBASIC_010_LimitMaxActiveP(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-010-limit-max-active-p.yaml",
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
			if dc.DERControlBase.OpModMaxLimW == nil {
				t.Fatalf("[%s] DERControl.OpModMaxLimW is nil — fixture dropped", cipher)
			}
			if got := dc.DERControlBase.OpModMaxLimW.Value; got != basic010LimitValue {
				t.Errorf("[%s] OpModMaxLimW.Value = %d, want %d",
					cipher, got, basic010LimitValue)
			}
			if got := dc.DERControlBase.OpModMaxLimW.Multiplier; got != 0 {
				t.Errorf("[%s] OpModMaxLimW.Multiplier = %d, want 0", cipher, got)
			}
		})
}
