// CSIP V1.2 Section 8.19 - Non-overlap event prioritization, 1 DERP / 1 DDERC / 2 DERC.
//
// BASIC-019 proves that a CSIP server seeded with one DERProgram
// carrying a DefaultDERControl AND two scheduled DERControls that
// share the same opMod* type but occupy non-overlapping intervals
// renders both events in the priority list. Per V1.2 Section 8.19 the
// procedure asserts the client transitions through DDERC ->
// DERControl[a] (in-window) -> DDERC (gap) -> DERControl[b]
// (in-window) -> DDERC. Server-side just asserts both events render
// AND that the intervals are pairwise disjoint (the test-specific
// invariant that defines "non-overlap").
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.19):
//
//	Step 1 (server has 1 DERP + 1 DDERC + 2 DERC non-overlap)  -> fixture load
//	Step 2 (walk to the single DERProgram)                      -> standard walk
//	Step 3 (DDERC carries opModFixedW = 1500 (15.00%))                 -> assertBASIC019DDERC
//	Step 4 (DERControlList has 2 events in lex-id order)        -> walkDERControlListByHref
//	Step 5 (Each event renders its MRID + Interval + Status     -> assertBASIC019DERCAt
//	        + opModFixedW)
//	Step 6 (Intervals are pairwise disjoint)                    -> assertDisjointIntervals
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic019FixtureName = "basic-019-1derp-1dderc-2derc-nonoverlap.yaml"

// basic019DDERCFixedW is the DDERC fallback (15.00%).
const basic019DDERCFixedW sep2.SignedPerCent = 1500

// basic019Events is the expected wire shape of the DERControlList,
// in lex-id order ("a" then "b") which the memory store sorts on.
var basic019Events = []struct {
	mrid     string
	start    int64
	duration uint32
	fixedW   sep2.SignedPerCent
}{
	{mrid: "BASIC-019-DERC-A", start: 1700000060, duration: 60, fixedW: 3000},
	{mrid: "BASIC-019-DERC-B", start: 1700000180, duration: 60, fixedW: 3500},
}

// TestBASIC_019_OneDERPOneDDERCTwoDERCNonOverlap implements CSIP V1.2 Section 8.19.
func TestBASIC_019_OneDERPOneDDERCTwoDERCNonOverlap(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC019)
}

func runBASIC019(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic019FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 2: walk to the single program.
	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if len(fsaList.FunctionSetAssignments) != 1 {
		t.Fatalf("FSA count = %d, want 1", len(fsaList.FunctionSetAssignments))
	}
	fsa := fsaList.FunctionSetAssignments[0]
	progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
	if len(progList.DERProgram) != 1 {
		t.Fatalf("DERProgram count = %d, want 1", len(progList.DERProgram))
	}
	prog := progList.DERProgram[0]

	// Step 3: DDERC payload.
	dderc := walkDefaultDERControlByHref(t, ctx, client, prog.DefaultDERControlLink.Href)
	if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
		t.Fatal("DDERC.DERControlBase.OpModFixedW is nil - fixture dropped")
	}
	if got := *dderc.DERControlBase.OpModFixedW; got != basic019DDERCFixedW {
		t.Errorf("DDERC.OpModFixedW = %d, want %d", got, basic019DDERCFixedW)
	}

	// Step 4: DERControlList has both events.
	ctrlList := walkDERControlListByHref(t, ctx, client, prog.DERControlListLink.Href)
	if got := int(ctrlList.All); got != len(basic019Events) {
		t.Fatalf("DERControlList.All = %d, want %d", got, len(basic019Events))
	}
	if got := len(ctrlList.DERControl); got != len(basic019Events) {
		t.Fatalf("len(DERControlList.DERControl) = %d, want %d",
			got, len(basic019Events))
	}

	// Step 5: assert each event's per-field payload in turn.
	for i, want := range basic019Events {
		assertBASIC019DERCAt(t, ctrlList.DERControl[i], want.mrid,
			want.start, want.duration, want.fixedW)
	}

	// Step 6: assert the [Start, Start+Duration) windows are pairwise
	// disjoint - the defining property of BASIC-019 vs #145's
	// BASIC-021..026.
	assertDisjointIntervals(t, ctrlList.DERControl)
}

// assertBASIC019DERCAt asserts a single rendered DERControl matches
// the expected wire shape (MRID, Status=Scheduled, Interval, opModFixedW).
func assertBASIC019DERCAt(
	t *testing.T,
	dc sep2.DERControl,
	wantMRID string,
	wantStart int64,
	wantDuration uint32,
	wantFixedW sep2.SignedPerCent,
) {
	t.Helper()

	if dc.MRID != wantMRID {
		t.Errorf("DERControl.MRID = %q, want %q", dc.MRID, wantMRID)
	}
	assertEventStatus(t, wantMRID, dc.EventStatus, sep2.EventStatusScheduled)
	assertEventInterval(t, wantMRID, dc.Interval, wantStart, wantDuration)
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatalf("%s DERControlBase.OpModFixedW is nil - fixture dropped", wantMRID)
	}
	if got := *dc.DERControlBase.OpModFixedW; got != wantFixedW {
		t.Errorf("%s OpModFixedW = %d, want %d", wantMRID, got, wantFixedW)
	}
}
