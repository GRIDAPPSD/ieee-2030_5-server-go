// CSIP V1.2 §10.7 — AGG-007 Aggregator Event Overlapping Similar
// (SY then Transformer-node before SY start).
//
// Mirror of BASIC-021 at the aggregator topology level. "Transformer"
// in the aggregator vocabulary corresponds to FDx ("1") in this
// fixture's IDs (Noor's coverage matrix Section 2 documents that
// substitution).
//
// Per managed inverter inject:
//   - SY-level (FSA="0", DERP="0"): 1 DDERC + 1 DERControl (OpModFixedW).
//   - FDx-level (FSA="1", DERP="1"): 1 DDERC + 1 DERControl (OpModFixedW
//     — "similar" mode, same opMod*).
//
// "Overlap similar" timing semantics live in the event interval fields,
// which the IEEE-057 loader does not surface (DERControlSpec carries
// only ID + MRID + DERControlBase). Per the IEEE-090 Pike-rule
// discipline, AGG-007 asserts the wire-shape invariant — both nodes
// expose the expected DDERC + DERControl carrying the same opMod —
// rather than client-side priority resolution.
//
// Procedure step → assertion mapping (per V1.2 §10.7):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: For each managed inverter, walk SY's /dderc + /derc and
//	        assert both surface with OpModFixedW (similar invariant).
//	Step 3: For each managed inverter, walk FDx's /dderc + /derc and
//	        assert both surface with OpModFixedW (similar invariant).
package csip_test

import "testing"

// TestAGG_007_OverlapSimilarSYThenXfmrBeforeSYStart implements CSIP V1.2 §10.7.
func TestAGG_007_OverlapSimilarSYThenXfmrBeforeSYStart(t *testing.T) {
	t.Parallel()
	runAggregatorOverlapSimilar(t, "007", aggOverlapPlan{SYValue: 2200, XfmrValue: 1800})
}
