// CSIP V1.2 §8.20 — Non-overlap event prioritization, 2 DERP / 2 DDERC / 2 DERC.
//
// BASIC-020 is the most procedural-heavy non-overlap case: two
// DERPrograms (Service Point primacy 0, Smart Inverter Yard primacy
// 1), each with its own DDERC AND its own scheduled DERControl, with
// the two DERControls occupying non-overlapping intervals and sharing
// the same opMod* type (opModFixedW). Per V1.2 §8.20 the procedure
// asserts the client resolves "highest-primacy program wins" outside
// events, then transitions into each event during its window;
// server-side just asserts the full topology renders.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.20):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC non-overlap)  ──► fixture load
//	Step 2 (walk /dcap → /edev → /fsa list, expect 2 FSAs)     ──► walkToFirstEDevFSAList
//	Step 3 (each FSA's DERProgramList renders both programs)   ──► assertBASIC020ProgramList
//	Step 4 (SP DDERC + SY DDERC carry expected opModFixedW)    ──► assertBASIC020DDERCs
//	Step 5 (SP and SY DERControlLists each have 1 event)       ──► assertBASIC020Events
//	Step 6 (Concatenated event intervals are pairwise disjoint) ──► assertDisjointIntervals
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic020FixtureName = "basic-020-2derp-2dderc-2derc-nonoverlap.yaml"

// basic020Programs is the expected per-FSA DERProgramList payload,
// in lex-id order ("sp" < "sy"). Each FSA's program list scopes by
// EndDevice id only, so both programs render at each FSA path.
var basic020Programs = []struct {
	mrid    string
	primacy uint8
}{
	{mrid: "BASIC-020-DERP-SP", primacy: 0},
	{mrid: "BASIC-020-DERP-SY", primacy: 1},
}

// basic020DDERCs is the expected DDERC payload per program key. Keyed
// by the program id segment so the test can iterate without
// duplicating the URL template.
var basic020DDERCs = map[string]struct {
	mrid   string
	fixedW int64
}{
	"sp": {mrid: "BASIC-020-DDERC-SP", fixedW: 4000},
	"sy": {mrid: "BASIC-020-DDERC-SY", fixedW: 2000},
}

// basic020Events is the expected per-program DERControl event payload.
var basic020Events = map[string]struct {
	mrid     string
	start    int64
	duration uint32
	fixedW   int64
}{
	"sp": {mrid: "BASIC-020-DERC-SP-A", start: 1700000060, duration: 60, fixedW: 4500},
	"sy": {mrid: "BASIC-020-DERC-SY-A", start: 1700000180, duration: 60, fixedW: 2500},
}

// TestBASIC_020_TwoDERPTwoDDERCTwoDERCNonOverlap implements CSIP V1.2 §8.20.
func TestBASIC_020_TwoDERPTwoDDERCTwoDERCNonOverlap(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC020)
}

func runBASIC020(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic020FixtureName, extraOpts)
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
	wantFSAOrder := []string{"BASIC-020-FSA-SP", "BASIC-020-FSA-SY"}
	for i, want := range wantFSAOrder {
		if got := fsaList.FunctionSetAssignments[i].MRID; got != want {
			t.Errorf("FSAList[%d].MRID = %q, want %q", i, got, want)
		}
	}

	// Step 3: each FSA's DERProgramList renders both programs in
	// primacy order (store scopes by EndDevice id only).
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d] missing DERProgramListLink", i)
		}
		progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
		assertBASIC020ProgramList(t, fsa.MRID, progList)
	}

	// Step 4: DDERCs.
	assertBASIC020DDERCs(t, ctx, client)

	// Step 5: per-program DERControlList has exactly one event each;
	// collect them into a flat slice so Step 6 can verify the disjoint
	// invariant across programs.
	allEvents := assertBASIC020Events(t, ctx, client)

	// Step 6: every pair of DERControls across the full set is
	// time-disjoint — the BASIC-020 non-overlap invariant.
	assertDisjointIntervals(t, allEvents)
}

// assertBASIC020ProgramList asserts each FSA's DERProgramList renders
// both programs in primacy order with the expected MRIDs.
func assertBASIC020ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := len(progList.DERProgram); got != len(basic020Programs) {
		t.Fatalf("[%s] DERProgramList len = %d, want %d",
			fsaLabel, got, len(basic020Programs))
	}
	for i, want := range basic020Programs {
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

// assertBASIC020DDERCs walks each program's DDERC link and asserts
// the per-program opModFixedW fallback value.
func assertBASIC020DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic020DDERCs[prog]
		if dderc.MRID != want.mrid {
			t.Errorf("[%s] DDERC.MRID = %q, want %q", prog, dderc.MRID, want.mrid)
		}
		if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DDERC.OpModFixedW is nil — fixture dropped", prog)
			continue
		}
		if got := dderc.DERControlBase.OpModFixedW.Value; got != want.fixedW {
			t.Errorf("[%s] DDERC.OpModFixedW.Value = %d, want %d",
				prog, got, want.fixedW)
		}
	}
}

// assertBASIC020Events walks each program's DERControlList, asserts
// exactly one event with the expected payload, and returns the flat
// slice of rendered DERControls so the caller can check the disjoint
// invariant across programs.
func assertBASIC020Events(t *testing.T, ctx context.Context, c *csiptest.Client) []sep2.DERControl {
	t.Helper()

	out := make([]sep2.DERControl, 0, 2)
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
		want := basic020Events[prog]
		if dc.MRID != want.mrid {
			t.Errorf("[%s] DERControl.MRID = %q, want %q", prog, dc.MRID, want.mrid)
		}
		assertEventStatus(t, want.mrid, dc.EventStatus, sep2.EventStatusScheduled)
		assertEventInterval(t, want.mrid, dc.Interval, want.start, want.duration)
		if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DERControl.OpModFixedW is nil — fixture dropped", prog)
			continue
		}
		if got := dc.DERControlBase.OpModFixedW.Value; got != want.fixedW {
			t.Errorf("[%s] DERControl.OpModFixedW.Value = %d, want %d",
				prog, got, want.fixedW)
		}
		out = append(out, dc)
	}
	return out
}
