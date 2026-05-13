// CSIP V1.2 §10.12 — AGG-012 Aggregator Event Overlapping Independent
// (Transformer-node then SY, Xfmr starts after SY start).
//
// Mirror of BASIC-026 at the aggregator topology level. Third
// overlap-independent variant; same wire-shape invariant as
// AGG-010 / AGG-011 with distinct injected values to keep the named
// test bodies independently observable.
//
// Procedure step → assertion mapping (per V1.2 §10.12):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: SY-level /dderc + /derc carry OpModFixedW only.
//	Step 3: FDx-level /dderc + /derc carry OpModConnect only.
package csip_test

import "testing"

// TestAGG_012_OverlapIndependentXfmrThenSYAfterSYStart implements CSIP V1.2 §10.12.
func TestAGG_012_OverlapIndependentXfmrThenSYAfterSYStart(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapIndependent(t, "012", aggOverlapPlan{SYValue: 3200, XfmrValue: 0})
}
