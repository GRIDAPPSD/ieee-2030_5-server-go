// CSIP V1.2 Section 8.23 - Overlap event prioritization, similar opMod, SP fully inside SY.
//
// BASIC-023 is the "containment" case of the similar-opMod family.
// Topology: 2 DERPrograms (SP primacy=0, SY primacy=1, both opModFixedW).
// SY starts first AND its window fully spans SP's window - SP starts
// mid-SY and ends before SY ends. Per Section 10.10 SP still wins inside its
// own window (lower primacy); SY re-applies outside SP's window.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.23):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC, SP nested in SY)  -> fixture load
//	Step 2 (walk /dcap -> /edev -> /fsa list, expect 2 FSAs)          -> walkToFirstEDevFSAList
//	Step 3 (each FSA renders its DERProgram in primacy order)       -> assertBASIC023ProgramList
//	Step 4 (SP DDERC + SY DDERC carry expected opModFixedW)         -> assertBASIC023DDERCs
//	Step 5 (SP and SY DERControlLists each have 1 event)            -> assertBASIC023Events
//	Step 6 (SP window fully contained in SY; SP wins Section 10.10)        -> assertOverlapResolution
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic023FixtureName = "basic-023-overlap-similar-sy-then-sp-after.yaml"

var basic023Programs = []struct {
	mrid    string
	primacy uint8
}{
	{mrid: "BASIC-023-DERP-SP", primacy: 0},
	{mrid: "BASIC-023-DERP-SY", primacy: 1},
}

var basic023DDERCs = map[string]struct {
	mrid   string
	fixedW int16
}{
	"sp": {mrid: "BASIC-023-DDERC-SP", fixedW: 4000},
	"sy": {mrid: "BASIC-023-DDERC-SY", fixedW: 2000},
}

// basic023Events - SY spans [60, 360); SP nested at [180, 240).
var basic023Events = map[string]struct {
	mrid     string
	primacy  uint8
	start    int64
	duration uint32
	fixedW   int16
}{
	"sp": {mrid: "BASIC-023-DERC-SP-A", primacy: 0, start: 1700000180, duration: 60, fixedW: 4500},
	"sy": {mrid: "BASIC-023-DERC-SY-A", primacy: 1, start: 1700000060, duration: 300, fixedW: 2500},
}

// TestBASIC_023_OverlapSimilarSYThenSPAfter implements CSIP V1.2 Section 8.23.
func TestBASIC_023_OverlapSimilarSYThenSPAfter(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC023)
}

func runBASIC023(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic023FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	wantFSAOrder := []string{"BASIC-023-FSA-SP", "BASIC-023-FSA-SY"}
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
		assertBASIC023ProgramList(t, fsa.MRID, progList)
	}

	assertBASIC023DDERCs(t, ctx, client)

	entries := assertBASIC023Events(t, ctx, client)

	// Section 10.10: SP wins on primacy inside the contained window; SY
	// reapplies outside SP - but that re-application is the
	// scheduler's job, not the server's.
	winners := assertOverlapResolution(t, entries)
	pairKey := "BASIC-023-DERC-SP-A|BASIC-023-DERC-SY-A"
	if got, ok := winners[pairKey]; !ok {
		t.Errorf("assertOverlapResolution returned no winner for pair %q", pairKey)
	} else if got != "BASIC-023-DERC-SP-A" {
		t.Errorf("Section 10.10 winner for %q = %q, want %q",
			pairKey, got, "BASIC-023-DERC-SP-A")
	}
}

func assertBASIC023ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := len(progList.DERProgram); got != len(basic023Programs) {
		t.Fatalf("[%s] DERProgramList len = %d, want %d",
			fsaLabel, got, len(basic023Programs))
	}
	for i, want := range basic023Programs {
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

func assertBASIC023DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic023DDERCs[prog]
		if dderc.MRID != want.mrid {
			t.Errorf("[%s] DDERC.MRID = %q, want %q", prog, dderc.MRID, want.mrid)
		}
		if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DDERC.OpModFixedW is nil - fixture dropped", prog)
			continue
		}
		if got := dderc.DERControlBase.OpModFixedW.Value; got != want.fixedW {
			t.Errorf("[%s] DDERC.OpModFixedW.Value = %d, want %d",
				prog, got, want.fixedW)
		}
	}
}

func assertBASIC023Events(t *testing.T, ctx context.Context, c *csiptest.Client) []overlapEntry {
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
		want := basic023Events[prog]
		if dc.MRID != want.mrid {
			t.Errorf("[%s] DERControl.MRID = %q, want %q", prog, dc.MRID, want.mrid)
		}
		assertEventStatus(t, want.mrid, dc.EventStatus, sep2.EventStatusScheduled)
		assertEventInterval(t, want.mrid, dc.Interval, want.start, want.duration)
		if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
			t.Errorf("[%s] DERControl.OpModFixedW is nil - fixture dropped", prog)
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
