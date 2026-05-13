// CSIP V1.2 §10.10 — AGG-010 Aggregator Event Overlapping Independent
// (SY then Transformer-node before SY start).
//
// Mirror of BASIC-024 at the aggregator topology level. "Independent"
// means the two overlapping events use DIFFERENT opMod* — they don't
// conflict because they touch different control axes. AGG-010 injects
// OpModFixedW at SY and OpModConnect at FDx; the wire-shape invariant
// is that each node surfaces a DDERC + DERControl with its own opMod
// untouched by the other.
//
// Procedure step → assertion mapping (per V1.2 §10.10):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: SY-level /dderc + /derc carry OpModFixedW only.
//	Step 3: FDx-level /dderc + /derc carry OpModConnect only.
package csip_test

import "testing"

// TestAGG_010_OverlapIndependentSYThenXfmrBeforeSYStart implements CSIP V1.2 §10.10.
func TestAGG_010_OverlapIndependentSYThenXfmrBeforeSYStart(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapIndependent(t, "010", aggOverlapPlan{SYValue: 4500, XfmrValue: 0})
}
