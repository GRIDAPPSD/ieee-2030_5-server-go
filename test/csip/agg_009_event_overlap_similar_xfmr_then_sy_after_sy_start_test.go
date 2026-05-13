// CSIP V1.2 §10.9 — AGG-009 Aggregator Event Overlapping Similar
// (Transformer-node then SY, Xfmr starts after SY start).
//
// Mirror of BASIC-023. Third overlap-similar variant; same wire-shape
// invariant as AGG-007 / AGG-008 with distinct injected values to keep
// the named test bodies independently observable.
//
// Procedure step → assertion mapping (per V1.2 §10.9):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: For each managed inverter, walk SY's /dderc + /derc and
//	        assert OpModFixedW (similar invariant).
//	Step 3: For each managed inverter, walk FDx's /dderc + /derc and
//	        assert OpModFixedW (similar invariant).
package csip_test

import "testing"

// TestAGG_009_OverlapSimilarXfmrThenSYAfterSYStart implements CSIP V1.2 §10.9.
func TestAGG_009_OverlapSimilarXfmrThenSYAfterSYStart(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapSimilar(t, "009", aggOverlapPlan{SYValue: 1500, XfmrValue: 1200})
}
