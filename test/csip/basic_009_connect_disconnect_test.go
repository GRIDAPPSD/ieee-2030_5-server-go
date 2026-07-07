// CSIP V1.2 §8.9 — Inverter Control: Connect/Disconnect.
//
// BASIC-009 proves the server renders an immediate-control DERControl
// carrying opModConnect=true and opModEnergize=true per Figure 9 —
// the "device connected and energized" state. No DERCurve reference.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.9):
//
//	Step 1 (server has DERProgram + 1 DERControl with
//	         opModConnect = true and opModEnergize = true)     ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram → DERControl)
//	                                                            ──► basicModeWalk
//	Step 3 (DERControl.OpModConnect = true,
//	         DERControl.OpModEnergize = true)                   ──► assertConnectEnergize
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestBASIC_009_ConnectDisconnect implements CSIP V1.2 §8.9.
func TestBASIC_009_ConnectDisconnect(t *testing.T) {
	t.Parallel()
	basicModeWalk(t, "basic-009-connect-disconnect.yaml",
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
			if dc.DERControlBase.OpModConnect == nil {
				t.Fatalf("[%s] DERControl.OpModConnect is nil — fixture dropped", cipher)
			}
			if *dc.DERControlBase.OpModConnect != true {
				t.Errorf("[%s] OpModConnect = %v, want true",
					cipher, *dc.DERControlBase.OpModConnect)
			}
			if dc.DERControlBase.OpModEnergize == nil {
				t.Fatalf("[%s] DERControl.OpModEnergize is nil — fixture dropped", cipher)
			}
			if *dc.DERControlBase.OpModEnergize != true {
				t.Errorf("[%s] OpModEnergize = %v, want true",
					cipher, *dc.DERControlBase.OpModEnergize)
			}
		})
}
