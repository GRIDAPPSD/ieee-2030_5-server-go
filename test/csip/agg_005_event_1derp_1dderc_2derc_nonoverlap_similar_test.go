// CSIP V1.2 §10.5 — AGG-005 Aggregator Event
// (1 DERP, 1 DDERC, 2 Non-overlap Similar DERC).
//
// Mirror of BASIC-019 at the aggregator topology level. Per managed
// inverter, inject one DDERC and two DERControls on the SY-level
// DERProgram. "Non-overlap similar" means the two DERCs use the SAME
// opMod* (similar) and do not overlap in time — but the loader does
// not encode interval timing, so the wire-shape assertion verifies
// both events surface in the DERControlList with the expected
// opMod* (OpModFixedW) values. Timing-resolution is a client-side
// concern beyond the scope of the server conformance harness.
//
// Procedure step → assertion mapping (per V1.2 §10.5):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: Walk /edev/{id}/fsa/0/derp/0/dderc per inverter; assert DDERC.
//	Step 3: Walk /edev/{id}/fsa/0/derp/0/derc per inverter; assert
//	        len == 2 and both entries carry OpModFixedW (similar mode).
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestAGG_005_Event1DERP1DDERC2DERCNonOverlapSimilar implements CSIP V1.2 §10.5.
func TestAGG_005_Event1DERP1DDERC2DERCNonOverlapSimilar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	spec := &csiptest.Spec{}
	for _, edevID := range aggManagedInverters {
		spec.DefaultDERControls = append(spec.DefaultDERControls,
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				MRID:         aggMRIDPrefix("005-DDERC-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModMaxLimW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: 6000},
				},
			},
		)
		for i, val := range []int64{2000, 3000} {
			spec.DERControls = append(spec.DERControls,
				csiptest.DERControlSpec{
					EndDeviceID:  edevID,
					FSAID:        aggFSAIDSY,
					DERProgramID: aggFSAIDSY,
					ID:           "agg005-" + edevID + "-" + string(rune('a'+i)),
					MRID:         aggMRIDPrefix("005-DERC-") + edevID + "-" + string(rune('a'+i)),
					DERControlBase: &csiptest.DERControlBaseSpec{
						OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: val},
					},
				},
			)
		}
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()

			// Step 2: DDERC present.
			dderc := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			wantDDERC := aggMRIDPrefix("005-DDERC-") + edevID
			if dderc.MRID != wantDDERC {
				t.Errorf("DDERC.MRID = %q, want %q", dderc.MRID, wantDDERC)
			}

			// Step 3: exactly 2 DERControls, both same opMod (OpModFixedW).
			list := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			if list.All != 2 {
				t.Errorf("DERControlList.All = %d, want 2", list.All)
			}
			if len(list.DERControl) != 2 {
				t.Fatalf("len(DERControlList.DERControl) = %d, want 2", len(list.DERControl))
			}
			for i, c := range list.DERControl {
				if c.DERControlBase == nil || c.DERControlBase.OpModFixedW == nil {
					t.Errorf("DERControl[%d] missing OpModFixedW (similar-mode invariant)", i)
				}
			}
		})
	}
}
