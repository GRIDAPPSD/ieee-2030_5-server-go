// CSIP V1.2 §8.24 — Overlap event prioritization, independent opMod, SP starts before SY.
//
// BASIC-024 is the independent-opMod counterpart of BASIC-021. Timing
// is identical (SP starts first, SY starts inside SP's window) but the
// two DERControls drive DIFFERENT opMod* fields: SP→opModFixedW,
// SY→opModMaxLimW. Per §10.10 different opMod families do not
// conflict — both events apply simultaneously across the overlap.
// The overlap-resolution helper still verifies the windows intersect
// but does NOT pick a winner.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.24):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC overlap independent) ──► fixture load
//	Step 2 (walk /dcap → /edev → /fsa list, expect 2 FSAs)            ──► walkToFirstEDevFSAList
//	Step 3 (each FSA renders its DERProgram in primacy order)         ──► assertBASIC024ProgramList
//	Step 4 (SP DDERC carries opModFixedW; SY carries opModMaxLimW)    ──► assertBASIC024DDERCs
//	Step 5 (SP renders FixedW event; SY renders MaxLimW event)        ──► assertBASIC024Events
//	Step 6 (Rendered intervals overlap; no §10.10 winner)             ──► assertOverlapResolution
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const basic024FixtureName = "basic-024-overlap-independent-sp-then-sy-before.yaml"

var basic024Programs = []struct {
	mrid    string
	primacy uint8
}{
	{mrid: "BASIC-024-DERP-SP", primacy: 0},
	{mrid: "BASIC-024-DERP-SY", primacy: 1},
}

// basic024DDERCs — SP defaults FixedW, SY defaults MaxLimW.
var basic024DDERCs = map[string]struct {
	mrid  string
	field string // "fixedW" or "maxLimW"
	value int64
}{
	"sp": {mrid: "BASIC-024-DDERC-SP", field: "fixedW", value: 4000},
	"sy": {mrid: "BASIC-024-DDERC-SY", field: "maxLimW", value: 6000},
}

// basic024Events — SP=FixedW [60, 180); SY=MaxLimW [100, 220).
var basic024Events = map[string]struct {
	mrid     string
	primacy  uint8
	start    int64
	duration uint32
	field    string // opMod family
	value    int64
}{
	"sp": {mrid: "BASIC-024-DERC-SP-A", primacy: 0, start: 1700000060, duration: 120, field: "fixedW", value: 4500},
	"sy": {mrid: "BASIC-024-DERC-SY-A", primacy: 1, start: 1700000100, duration: 120, field: "maxLimW", value: 5500},
}

// TestBASIC_024_OverlapIndependentSPThenSYBefore implements CSIP V1.2 §8.24.
func TestBASIC_024_OverlapIndependentSPThenSYBefore(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC024)
}

func runBASIC024(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic024FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	wantFSAOrder := []string{"BASIC-024-FSA-SP", "BASIC-024-FSA-SY"}
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
		assertBASIC024ProgramList(t, fsa.MRID, progList)
	}

	assertBASIC024DDERCs(t, ctx, client)

	entries := assertBASIC024Events(t, ctx, client)

	// Independent overlap: helper verifies intersection but the
	// returned winners map is EMPTY (different opMod families).
	winners := assertOverlapResolution(t, entries)
	if len(winners) != 0 {
		t.Errorf("independent overlap: expected empty winners map, got %v", winners)
	}
}

func assertBASIC024ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := len(progList.DERProgram); got != len(basic024Programs) {
		t.Fatalf("[%s] DERProgramList len = %d, want %d",
			fsaLabel, got, len(basic024Programs))
	}
	for i, want := range basic024Programs {
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

func assertBASIC024DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic024DDERCs[prog]
		if dderc.MRID != want.mrid {
			t.Errorf("[%s] DDERC.MRID = %q, want %q", prog, dderc.MRID, want.mrid)
		}
		assertOpModValue(t, "DDERC["+prog+"]", dderc.DERControlBase, want.field, want.value)
	}
}

func assertBASIC024Events(t *testing.T, ctx context.Context, c *csiptest.Client) []overlapEntry {
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
		want := basic024Events[prog]
		if dc.MRID != want.mrid {
			t.Errorf("[%s] DERControl.MRID = %q, want %q", prog, dc.MRID, want.mrid)
		}
		assertEventStatus(t, want.mrid, dc.EventStatus, sep2.EventStatusScheduled)
		assertEventInterval(t, want.mrid, dc.Interval, want.start, want.duration)
		assertOpModValue(t, "DERControl["+prog+"]", dc.DERControlBase, want.field, want.value)
		out = append(out, overlapEntry{
			programLabel: prog,
			primacy:      want.primacy,
			control:      dc,
			opModFamily:  want.field,
		})
	}
	return out
}
