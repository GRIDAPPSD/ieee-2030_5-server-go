// CSIP V1.2 Section 10.6 - AGG-006 Aggregator Event
// (2 DERP, 2 DDERC, 2 Non-overlap Similar DERC).
//
// Mirror of BASIC-020 at the aggregator topology level. Per managed
// inverter, inject one DDERC + one DERControl on the SY-level
// DERProgram AND one DDERC + one DERControl on the FDx-level
// DERProgram. Both DERControls use the same opMod (OpModFixedW -
// "similar"). The procedure asserts each managed inverter surfaces the
// full SY/FDx pair.
//
// Procedure step -> assertion mapping (per V1.2 Section 10.6):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2 (SY): walk /dderc + /derc at SY; assert MRID and counts.
//	Step 3 (FDx): walk /dderc + /derc at FDx; assert MRID and counts.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestAGG_006_Event2DERP2DDERC2DERCNonOverlapSimilar implements CSIP V1.2 Section 10.6.
func TestAGG_006_Event2DERP2DDERC2DERCNonOverlapSimilar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	type nodeSpec struct {
		fsaID string
		val   int16
	}
	nodes := []nodeSpec{
		{aggFSAIDSY, 4000},
		{aggFSAIDFD, 3000},
	}

	spec := &csiptest.Spec{}
	for _, edevID := range aggManagedInverters {
		for _, n := range nodes {
			spec.DefaultDERControls = append(spec.DefaultDERControls,
				csiptest.DefaultDERControlSpec{
					EndDeviceID:  edevID,
					FSAID:        n.fsaID,
					DERProgramID: n.fsaID,
					MRID:         aggMRIDPrefix("006-DDERC-") + n.fsaID + "-" + edevID,
					DERControlBase: &csiptest.DERControlBaseSpec{
						OpModMaxLimW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: n.val + 1000},
					},
				},
			)
			spec.DERControls = append(spec.DERControls,
				csiptest.DERControlSpec{
					EndDeviceID:  edevID,
					FSAID:        n.fsaID,
					DERProgramID: n.fsaID,
					ID:           "agg006-" + edevID + "-" + n.fsaID,
					MRID:         aggMRIDPrefix("006-DERC-") + n.fsaID + "-" + edevID,
					DERControlBase: &csiptest.DERControlBaseSpec{
						OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: n.val},
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
			for _, n := range nodes {
				dderc := walkAggregatorDefaultDERControl(t, ctx, client, edevID, n.fsaID, n.fsaID)
				wantDDERC := aggMRIDPrefix("006-DDERC-") + n.fsaID + "-" + edevID
				if dderc.MRID != wantDDERC {
					t.Errorf("node=%s DDERC.MRID = %q, want %q", n.fsaID, dderc.MRID, wantDDERC)
				}
				list := walkAggregatorDERControlList(t, ctx, client, edevID, n.fsaID, n.fsaID)
				if list.All != 1 {
					t.Errorf("node=%s DERControlList.All = %d, want 1", n.fsaID, list.All)
				}
				if len(list.DERControl) != 1 {
					t.Fatalf("node=%s len(DERControlList.DERControl) = %d, want 1",
						n.fsaID, len(list.DERControl))
				}
				if list.DERControl[0].DERControlBase == nil ||
					list.DERControl[0].DERControlBase.OpModFixedW == nil {
					t.Errorf("node=%s DERControl[0] missing OpModFixedW (similar invariant)", n.fsaID)
				}
			}
		})
	}
}
