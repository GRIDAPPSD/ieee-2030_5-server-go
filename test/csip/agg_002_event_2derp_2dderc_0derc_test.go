// CSIP V1.2 §10.2 — AGG-002 Aggregator Event (2 DERP, 2 DDERC, 0 DERC).
//
// Mirror of BASIC-016 at the aggregator topology level (Noor's V1.2
// coverage matrix Section 2 documents the symmetry — "transformer" at
// the aggregator layer replaces "service point" at the inverter layer;
// in this fixture's terms that maps to FSA IDs SY ("0") and FDx ("1")
// being the two priority levels exercised).
//
// What this test injects on top of bootAggregatorTopology():
//
//   - For every managed inverter EDA1/EDA2/EDB1/EDB2:
//   - One DefaultDERControl on the SY-level DERProgram (FSA="0", DERP="0").
//   - One DefaultDERControl on the FDx-level DERProgram (FSA="1", DERP="1").
//   - Zero DERControls.
//
// Procedure step → assertion mapping (per V1.2 §10.2):
//
//	Step 1: Server has aggregator topology + 2 DDERCs per inverter.
//	        ──► bootAggregatorTopology + injectEventSpec.
//	Step 2: For each managed inverter, walk /edev/{id}/fsa/0/derp/0/dderc
//	        and /edev/{id}/fsa/1/derp/1/dderc; assert MRID and the
//	        OpModMaxLimW value match the per-priority injection.
//	Step 3: For each managed inverter, walk /edev/{id}/fsa/0/derp/0/derc
//	        and assert the list is empty (All=0). No DERControls injected.
//
// Per the #147 anti-abstraction rule: this test is one named Go
// function, not a parameterized table entry. AGG-003..012 follow.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestAGG_002_Event2DERP2DDERC0DERC implements CSIP V1.2 §10.2.
func TestAGG_002_Event2DERP2DDERC0DERC(t *testing.T) {
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
				MRID:         aggMRIDPrefix("002-DDERC-SY-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModMaxLimW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: 5000},
				},
			},
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDFD,
				DERProgramID: aggFSAIDFD,
				MRID:         aggMRIDPrefix("002-DDERC-FD-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModMaxLimW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: 4000},
				},
			},
		)
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()

			// Step 2: SY-level DDERC.
			ddercSY := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			wantSY := aggMRIDPrefix("002-DDERC-SY-") + edevID
			if ddercSY.MRID != wantSY {
				t.Errorf("SY DDERC MRID = %q, want %q", ddercSY.MRID, wantSY)
			}
			if ddercSY.DERControlBase == nil || ddercSY.DERControlBase.OpModMaxLimW == nil {
				t.Fatalf("SY DDERC missing OpModMaxLimW")
			}
			if got := ddercSY.DERControlBase.OpModMaxLimW.Value; got != 5000 {
				t.Errorf("SY DDERC OpModMaxLimW.Value = %d, want 5000", got)
			}

			// Step 2 (cont): FDx-level DDERC at lower priority.
			ddercFD := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDFD, aggFSAIDFD)
			wantFD := aggMRIDPrefix("002-DDERC-FD-") + edevID
			if ddercFD.MRID != wantFD {
				t.Errorf("FDx DDERC MRID = %q, want %q", ddercFD.MRID, wantFD)
			}
			if ddercFD.DERControlBase == nil || ddercFD.DERControlBase.OpModMaxLimW == nil {
				t.Fatalf("FDx DDERC missing OpModMaxLimW")
			}
			if got := ddercFD.DERControlBase.OpModMaxLimW.Value; got != 4000 {
				t.Errorf("FDx DDERC OpModMaxLimW.Value = %d, want 4000", got)
			}

			// Step 3: SY-level DERControlList must be empty (no events injected).
			list := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			if list.All != 0 {
				t.Errorf("SY DERControlList.All = %d, want 0", list.All)
			}
			if len(list.DERControl) != 0 {
				t.Errorf("len(SY DERControlList.DERControl) = %d, want 0", len(list.DERControl))
			}
		})
	}
}
