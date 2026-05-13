// CSIP V1.2 §10.3 — AGG-003 Aggregator Event (1 DERP, 0 DDERC, 1 DERC).
//
// Mirror of BASIC-017 at the aggregator topology level. Per managed
// inverter, inject one DERControl on the SY-level DERProgram and no
// DefaultDERControls. The procedure asserts each managed inverter sees
// exactly one DERControl at the SY node and zero DDERCs.
//
// Per the IEEE-090 anti-abstraction rule: dedicated named test, even
// where the procedure shape repeats neighbouring AGG-* tests.
//
// Procedure step → assertion mapping (per V1.2 §10.3):
//
//	Step 1: bootAggregatorTopology + injectEventSpec (1 DERC per
//	        inverter, no DDERC).
//	Step 2: For each managed inverter, walk /edev/{id}/fsa/0/derp/0/derc
//	        and assert exactly 1 entry whose Href matches the injection.
//
// Loader gap (out of IEEE-090 scope): csiptest.buildDERControl
// (test/csip/csiptest/loader.go) drops DERControlSpec.MRID — it sets
// only Href and DERControlBase. AGG-003..012 therefore identify
// injected DERControls by Href, not MRID. DefaultDERControl is
// unaffected (buildDefaultDERControl honors MRID). Filing as a
// follow-up against IEEE-057. See IEEE-090 PR description.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestAGG_003_Event1DERP0DDERC1DERC implements CSIP V1.2 §10.3.
func TestAGG_003_Event1DERP0DDERC1DERC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	spec := &csiptest.Spec{}
	for _, edevID := range aggManagedInverters {
		spec.DERControls = append(spec.DERControls,
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				ID:           "agg003-" + edevID,
				MRID:         aggMRIDPrefix("003-DERC-SY-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModMaxLimW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: 3000},
				},
			},
		)
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()

			// Step 2: exactly one DERControl at SY.
			list := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			if list.All != 1 {
				t.Errorf("SY DERControlList.All = %d, want 1", list.All)
			}
			if len(list.DERControl) != 1 {
				t.Fatalf("len(SY DERControlList.DERControl) = %d, want 1", len(list.DERControl))
			}
			wantHref := "/edev/" + edevID + "/fsa/" + aggFSAIDSY + "/derp/" + aggFSAIDSY + "/derc/agg003-" + edevID
			if list.DERControl[0].Href != wantHref {
				t.Errorf("DERControl[0].Href = %q, want %q", list.DERControl[0].Href, wantHref)
			}
			if list.DERControl[0].DERControlBase == nil ||
				list.DERControl[0].DERControlBase.OpModMaxLimW == nil ||
				list.DERControl[0].DERControlBase.OpModMaxLimW.Value != 3000 {
				t.Errorf("DERControl[0] OpModMaxLimW dropped on the wire")
			}
		})
	}
}
