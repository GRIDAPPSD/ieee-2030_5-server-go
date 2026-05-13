// CSIP V1.2 §10.8 — AGG-008 Aggregator Event Overlapping Similar
// (Transformer-node then SY).
//
// Mirror of BASIC-022 at the aggregator topology level. Reverses
// AGG-007's timing (Xfmr starts first, SY second) but the wire-shape
// invariant on the server side is identical: each managed inverter
// exposes a DDERC + DERControl at SY and at FDx, all four DERControls
// carrying the same opMod* (OpModFixedW — "similar").
//
// AGG-008 differs from AGG-007 by the OpModFixedW values it injects, so
// the test bodies are not literally identical and a regression that
// stomped on the runner-shared spec would surface as a value mismatch
// in only one of the named tests.
//
// Procedure step → assertion mapping (per V1.2 §10.8):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: For each managed inverter, walk SY's /dderc + /derc and
//	        assert OpModFixedW (similar invariant).
//	Step 3: For each managed inverter, walk FDx's /dderc + /derc and
//	        assert OpModFixedW (similar invariant).
package csip_test

import "testing"

// TestAGG_008_OverlapSimilarXfmrThenSY implements CSIP V1.2 §10.8.
func TestAGG_008_OverlapSimilarXfmrThenSY(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapSimilar(t, "008", aggOverlapPlan{SYValue: 2700, XfmrValue: 2300})
}
