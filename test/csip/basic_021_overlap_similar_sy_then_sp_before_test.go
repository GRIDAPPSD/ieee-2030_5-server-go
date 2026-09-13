// CSIP V1.2 Section 8.21 - Overlap event prioritization, similar opMod, SP starts before SY.
//
// BASIC-021 is the entry case for the BASIC-021..026 overlapping-event
// family (the dual of #143's BASIC-016..020 non-overlap family).
// Topology: 2 DERPrograms (SP primacy=0, SY primacy=1), each with a
// DefaultDERControl and one scheduled DERControl. Both DERControls
// drive the SAME opMod* (opModFixedW - "similar") and their
// [Start, Start+Duration) windows OVERLAP: SP starts first, SY starts
// while SP is still active.
//
// Per V1.2 Section 8.21 the procedure asserts the client transitions through
// the Section 10.10 primacy ladder during the overlap region. Server-side
// just asserts: (a) both events render with the expected wire shape,
// (b) the windows intersect (the inverse of BASIC-019's disjoint
// invariant), and (c) the expected Section 10.10 winner (SP) is reachable
// from the rendered metadata. Scheduler-side enforcement is the DER
// client's problem (plan-1 scope).
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.21):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC overlap similar)  -> fixture load
//	Step 2 (walk /dcap -> /edev -> /fsa list, expect 2 FSAs)         -> walkToFirstEDevFSAList
//	Step 3 (each FSA renders its DERProgram in primacy order)      -> assertBASIC021ProgramList
//	Step 4 (SP DDERC + SY DDERC carry expected opModFixedW)        -> assertBASIC021DDERCs
//	Step 5 (SP and SY DERControlLists each have 1 event)           -> assertBASIC021Events
//	Step 6 (Rendered event intervals overlap; SP wins Section 10.10)      -> assertOverlapResolution
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic021FixtureName = "basic-021-overlap-similar-sp-then-sy-before.yaml"

// basic021Programs is the expected per-FSA DERProgramList payload,
// in lex-id order ("sp" < "sy"). Each FSA's program list scopes by
// EndDevice id only, so both programs render at each FSA path.
var basic021Programs = []struct {
	mrid    string
	primacy uint8
}{
	{mrid: "BASIC-021-DERP-SP", primacy: 0},
	{mrid: "BASIC-021-DERP-SY", primacy: 1},
}

// basic021DDERCs is the expected DDERC payload per program key.
var basic021DDERCs = map[string]struct {
	mrid   string
	fixedW sep2.SignedPerCent
}{
	"sp": {mrid: "BASIC-021-DDERC-SP", fixedW: 4000},
	"sy": {mrid: "BASIC-021-DDERC-SY", fixedW: 2000},
}

// basic021Events is the expected per-program DERControl event payload.
// Both events drive opModFixedW (similar) and overlap on [100, 180).
var basic021Events = map[string]struct {
	mrid     string
	primacy  uint8
	start    int64
	duration uint32
	fixedW   sep2.SignedPerCent
}{
	"sp": {mrid: "BASIC-021-DERC-SP-A", primacy: 0, start: 1700000060, duration: 120, fixedW: 4500},
	"sy": {mrid: "BASIC-021-DERC-SY-A", primacy: 1, start: 1700000100, duration: 120, fixedW: 2500},
}

// TestBASIC_021_OverlapSimilarSPThenSYBefore implements CSIP V1.2 Section 8.21.
func TestBASIC_021_OverlapSimilarSPThenSYBefore(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC021)
}

func runBASIC021(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic021FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 2: walk to the FSA list (2 FSAs lex-sorted as "sp" then "sy").
	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	wantFSAOrder := []string{"BASIC-021-FSA-SP", "BASIC-021-FSA-SY"}
	for i, want := range wantFSAOrder {
		if got := fsaList.FunctionSetAssignments[i].MRID; got != want {
			t.Errorf("FSAList[%d].MRID = %q, want %q", i, got, want)
		}
	}

	// Step 3: each FSA's DERProgramList renders both programs in primacy order.
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d] missing DERProgramListLink", i)
		}
		progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
		assertBASIC021ProgramList(t, fsa.MRID, progList)
	}

	// Step 4: DDERCs.
	assertBASIC021DDERCs(t, ctx, client)

	// Step 5: per-program DERControlList has exactly one event each;
	// collect them so Step 6 can verify the overlap invariant.
	entries := assertBASIC021Events(t, ctx, client)

	// Step 6: the rendered events overlap, and per Section 10.10 SP wins on
	// primacy (both events share opMod*).
	winners := assertOverlapResolution(t, entries)
	pairKey := "BASIC-021-DERC-SP-A|BASIC-021-DERC-SY-A"
	if got, ok := winners[pairKey]; !ok {
		t.Errorf("assertOverlapResolution returned no winner for pair %q", pairKey)
	} else if got != "BASIC-021-DERC-SP-A" {
		t.Errorf("Section 10.10 winner for %q = %q, want %q",
			pairKey, got, "BASIC-021-DERC-SP-A")
	}
}

// assertBASIC021ProgramList asserts each FSA's DERProgramList renders
// both programs in primacy order with the expected MRIDs.
func assertBASIC021ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := len(progList.DERProgram); got != len(basic021Programs) {
		t.Fatalf("[%s] DERProgramList len = %d, want %d",
			fsaLabel, got, len(basic021Programs))
	}
	for i, want := range basic021Programs {
		got := progList.DERProgram[i]
		if got.MRID != want.mrid {
			t.Errorf("[%s] DERProgram[%d].MRID = %q, want %q",
				fsaLabel, i, got.MRID, want.mrid)
		}
		if got.Primacy != want.primacy {
			t.Errorf("[%s] DERProgram[%d].Primacy = %d, want %d",
				fsaLabel, i, got.Primacy, want.primacy)
		}
	}
}

// assertBASIC021DDERCs walks each program's DDERC link and asserts
// the per-program opModFixedW fallback value.
func assertBASIC021DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic021DDERCs[prog]
		if dderc.MRID != want.mrid {
			t.Errorf("[%s] DDERC.MRID = %q, want %q", prog, dderc.MRID, want.mrid)
		}
		if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DDERC.OpModFixedW is nil - fixture dropped", prog)
			continue
		}
		if got := *dderc.DERControlBase.OpModFixedW; got != want.fixedW {
			t.Errorf("[%s] DDERC.OpModFixedW = %d, want %d",
				prog, got, want.fixedW)
		}
	}
}

// assertBASIC021Events walks each program's DERControlList, asserts
// exactly one event with the expected payload, and returns the
// overlapEntry slice for the Section 10.10 resolution check.
func assertBASIC021Events(t *testing.T, ctx context.Context, c *csiptest.Client) []overlapEntry {
	t.Helper()

	out := make([]overlapEntry, 0, 2)
	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/derc"
		list := walkDERControlListByHref(t, ctx, c, href)
		if got := int(list.All); got != 1 {
			t.Errorf("[%s] DERControlList.All = %d, want 1", prog, got)
			continue
		}
		if got := len(list.DERControl); got != 1 {
			t.Errorf("[%s] DERControlList len = %d, want 1", prog, got)
			continue
		}
		dc := list.DERControl[0]
		want := basic021Events[prog]
		if dc.MRID != want.mrid {
			t.Errorf("[%s] DERControl.MRID = %q, want %q", prog, dc.MRID, want.mrid)
		}
		assertEventStatus(t, want.mrid, dc.EventStatus, sep2.EventStatusScheduled)
		assertEventInterval(t, want.mrid, dc.Interval, want.start, want.duration)
		if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DERControl.OpModFixedW is nil - fixture dropped", prog)
			continue
		}
		if got := *dc.DERControlBase.OpModFixedW; got != want.fixedW {
			t.Errorf("[%s] DERControl.OpModFixedW = %d, want %d",
				prog, got, want.fixedW)
		}
		out = append(out, overlapEntry{
			programLabel: prog,
			primacy:      want.primacy,
			control:      dc,
			opModFamily:  "fixedW",
		})
	}
	return out
}
