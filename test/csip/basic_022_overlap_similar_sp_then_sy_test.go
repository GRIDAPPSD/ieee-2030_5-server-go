// CSIP V1.2 §8.22 — Overlap event prioritization, similar opMod, SY starts before SP.
//
// BASIC-022 is the reverse-timing case of BASIC-021. Same topology
// (2 DERPrograms, SP primacy=0, SY primacy=1, both opModFixedW —
// "similar"), same overlap shape, but SY starts FIRST and SP starts
// while SY is still active. Per §10.10 the primacy ladder still picks
// SP as the winner even though SP starts later: primacy beats start
// time when programs share an opMod*.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.22):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC overlap similar reversed) ──► fixture load
//	Step 2 (walk /dcap → /edev → /fsa list, expect 2 FSAs)                  ──► walkToFirstEDevFSAList
//	Step 3 (each FSA renders its DERProgram in primacy order)               ──► assertBASIC022ProgramList
//	Step 4 (SP DDERC + SY DDERC carry expected opModFixedW)                 ──► assertBASIC022DDERCs
//	Step 5 (SP and SY DERControlLists each have 1 event)                    ──► assertBASIC022Events
//	Step 6 (Rendered event intervals overlap; SP wins §10.10)               ──► assertOverlapResolution
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic022FixtureName = "basic-022-overlap-similar-sp-then-sy.yaml"

var basic022Programs = []struct {
	mrid    string
	primacy uint8
}{
	{mrid: "BASIC-022-DERP-SP", primacy: 0},
	{mrid: "BASIC-022-DERP-SY", primacy: 1},
}

var basic022DDERCs = map[string]struct {
	mrid   string
	fixedW int64
}{
	"sp": {mrid: "BASIC-022-DDERC-SP", fixedW: 4000},
	"sy": {mrid: "BASIC-022-DDERC-SY", fixedW: 2000},
}

// basic022Events — SY starts first (60s), SP starts inside SY (100s).
var basic022Events = map[string]struct {
	mrid     string
	primacy  uint8
	start    int64
	duration uint32
	fixedW   int64
}{
	"sp": {mrid: "BASIC-022-DERC-SP-A", primacy: 0, start: 1700000100, duration: 120, fixedW: 4500},
	"sy": {mrid: "BASIC-022-DERC-SY-A", primacy: 1, start: 1700000060, duration: 120, fixedW: 2500},
}

// TestBASIC_022_OverlapSimilarSPThenSY implements CSIP V1.2 §8.22.
func TestBASIC_022_OverlapSimilarSPThenSY(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC022)
}

func runBASIC022(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic022FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	wantFSAOrder := []string{"BASIC-022-FSA-SP", "BASIC-022-FSA-SY"}
	for i, want := range wantFSAOrder {
		if got := fsaList.FunctionSetAssignments[i].MRID; got != want {
			t.Errorf("FSAList[%d].MRID = %q, want %q", i, got, want)
		}
	}

	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d] missing DERProgramListLink", i)
		}
		progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
		assertBASIC022ProgramList(t, fsa.MRID, progList)
	}

	assertBASIC022DDERCs(t, ctx, client)

	entries := assertBASIC022Events(t, ctx, client)

	// §10.10: SP wins on primacy even though SY started first.
	winners := assertOverlapResolution(t, entries)
	pairKey := "BASIC-022-DERC-SP-A|BASIC-022-DERC-SY-A"
	if got, ok := winners[pairKey]; !ok {
		t.Errorf("assertOverlapResolution returned no winner for pair %q", pairKey)
	} else if got != "BASIC-022-DERC-SP-A" {
		t.Errorf("§10.10 winner for %q = %q, want %q",
			pairKey, got, "BASIC-022-DERC-SP-A")
	}
}

func assertBASIC022ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := len(progList.DERProgram); got != len(basic022Programs) {
		t.Fatalf("[%s] DERProgramList len = %d, want %d",
			fsaLabel, got, len(basic022Programs))
	}
	for i, want := range basic022Programs {
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

func assertBASIC022DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic022DDERCs[prog]
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

func assertBASIC022Events(t *testing.T, ctx context.Context, c *csiptest.Client) []overlapEntry {
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
		want := basic022Events[prog]
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
		out = append(out, overlapEntry{
			programLabel: prog,
			primacy:      want.primacy,
			control:      dc,
			opModFamily:  "fixedW",
		})
	}
	return out
}
