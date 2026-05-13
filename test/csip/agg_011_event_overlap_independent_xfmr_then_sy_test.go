// CSIP V1.2 §10.11 — AGG-011 Aggregator Event Overlapping Independent
// (Transformer-node then SY).
//
// Mirror of BASIC-025. Reverses AGG-010's timing; the server-side
// wire-shape invariant is identical. Different injected SYValue keeps
// the named test bodies independently observable.
//
// Procedure step → assertion mapping (per V1.2 §10.11):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: SY-level /dderc + /derc carry OpModFixedW only.
//	Step 3: FDx-level /dderc + /derc carry OpModConnect only.
package csip_test

import "testing"

// TestAGG_011_OverlapIndependentXfmrThenSY implements CSIP V1.2 §10.11.
func TestAGG_011_OverlapIndependentXfmrThenSY(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapIndependent(t, "011", aggOverlapPlan{SYValue: 5500, XfmrValue: 0})
}
