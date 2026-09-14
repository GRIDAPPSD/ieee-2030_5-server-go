// CSIP V1.2 §10.4 — AGG-004 Aggregator Event (1 DERP, 1 DDERC, 1 DERC).
//
// Mirror of BASIC-018 at the aggregator topology level. Per managed
// inverter, inject one DDERC and one DERC on the SY-level DERProgram.
// The procedure asserts each managed inverter sees both surfaces
// correctly populated on the wire.
//
// Procedure step → assertion mapping (per V1.2 §10.4):
//
//	Step 1: bootAggregatorTopology + injectEventSpec.
//	Step 2: For each managed inverter, walk /edev/{id}/fsa/0/derp/0/dderc;
//	        MRID + OpModMaxLimW match the injection.
//	Step 3: For each managed inverter, walk /edev/{id}/fsa/0/derp/0/derc;
//	        list All=1, the single entry's MRID + OpModFixedW match.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestAGG_004_Event1DERP1DDERC1DERC implements CSIP V1.2 §10.4.
func TestAGG_004_Event1DERP1DDERC1DERC(t *testing.T) {
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
				MRID:         aggMRIDPrefix("004-DDERC-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModMaxLimW: ptrTo[sep2.PerCent](5000),
				},
			},
		)
		spec.DERControls = append(spec.DERControls,
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				ID:           "agg004-" + edevID,
				MRID:         aggMRIDPrefix("004-DERC-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: ptrTo[sep2.SignedPerCent](2500),
				},
			},
		)
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()

			// Step 2: DDERC.
			dderc := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			wantDDERC := aggMRIDPrefix("004-DDERC-") + edevID
			if dderc.MRID != wantDDERC {
				t.Errorf("DDERC.MRID = %q, want %q", dderc.MRID, wantDDERC)
			}
			if dderc.DERControlBase == nil || dderc.DERControlBase.OpModMaxLimW == nil ||
				*dderc.DERControlBase.OpModMaxLimW != 5000 {
				t.Errorf("DDERC OpModMaxLimW dropped on the wire")
			}

			// Step 3: DERControl list has exactly 1 entry.
			list := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			if list.All != 1 {
				t.Errorf("DERControlList.All = %d, want 1", list.All)
			}
			if len(list.DERControl) != 1 {
				t.Fatalf("len(DERControlList.DERControl) = %d, want 1", len(list.DERControl))
			}
			// Loader gap: buildDERControl drops Spec.MRID. Identify via
			// Href instead. See AGG-003 file-level note for context.
			wantHref := "/edev/" + edevID + "/fsa/" + aggFSAIDSY + "/derp/" + aggFSAIDSY + "/derc/agg004-" + edevID
			if list.DERControl[0].Href != wantHref {
				t.Errorf("DERControl[0].Href = %q, want %q", list.DERControl[0].Href, wantHref)
			}
			if list.DERControl[0].DERControlBase == nil ||
				list.DERControl[0].DERControlBase.OpModFixedW == nil ||
				*list.DERControl[0].DERControlBase.OpModFixedW != 2500 {
				t.Errorf("DERControl[0] OpModFixedW dropped on the wire")
			}
		})
	}
}
