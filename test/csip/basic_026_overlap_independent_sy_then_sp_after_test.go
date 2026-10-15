// CSIP V1.2 Section 8.26 - Overlap event prioritization, independent opMod, SP fully inside SY.
//
// BASIC-026 is the independent-opMod counterpart of BASIC-023. SY's
// window fully spans SP's window, but the two DERControls drive
// DIFFERENT opMod* fields (SP->opModFixedW, SY->opModMaxLimW). Per
// Section 10.10 different opMod families do not conflict - both events
// apply simultaneously across the overlap. The overlap-resolution
// helper still verifies the windows intersect but does NOT pick a
// winner.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.26):
//
//	Step 1 (server has 2 DERP + 2 DDERC + 2 DERC, SP nested in SY indep) --> fixture load
//	Step 2 (walk /dcap -> /edev -> /fsa list, expect 2 FSAs)               --> walkToFirstEDevFSAList
//	Step 3 (each FSA lists only its own DERProgram)                       --> assertFSAProgramList
//	Step 4 (SP DDERC carries opModFixedW; SY carries opModMaxLimW)       --> assertBASIC026DDERCs
//	Step 5 (SP renders FixedW event; SY renders MaxLimW event)           --> assertBASIC026Events
//	Step 6 (SP window contained in SY; no Section 10.10 winner)                 --> assertOverlapResolution
//
// Run over CCM-8, the spec cipher path.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic026FixtureName = "basic-026-overlap-independent-sy-then-sp-after.yaml"

// basic026Programs is the one program each FSA's DERProgramList holds:
// the procedure GETs an SY DERProgramList and an SP DERProgramList.
var basic026Programs = map[string]wantProgram{
	"BASIC-026-FSA-SP": {mrid: "BASIC-026-DERP-SP", primacy: 0},
	"BASIC-026-FSA-SY": {mrid: "BASIC-026-DERP-SY", primacy: 1},
}

var basic026DDERCs = map[string]struct {
	mrid  string
	field string
	value int16
}{
	"sp": {mrid: "BASIC-026-DDERC-SP", field: "fixedW", value: 4000},
	"sy": {mrid: "BASIC-026-DDERC-SY", field: "maxLimW", value: 6000},
}

// basic026Events - SY=MaxLimW spans [60, 360); SP=FixedW nested [180, 240).
var basic026Events = map[string]struct {
	mrid     string
	primacy  uint8
	start    int64
	duration uint32
	field    string
	value    int16
}{
	"sp": {mrid: "BASIC-026-DERC-SP-A", primacy: 0, start: 1700000180, duration: 60, field: "fixedW", value: 4500},
	"sy": {mrid: "BASIC-026-DERC-SY-A", primacy: 1, start: 1700000060, duration: 300, field: "maxLimW", value: 5500},
}

// TestBASIC_026_OverlapIndependentSYThenSPAfter implements CSIP V1.2 Section 8.26.
func TestBASIC_026_OverlapIndependentSYThenSPAfter(t *testing.T) {
	t.Parallel()
	runUnderCCM(t, runBASIC026)
}

func runBASIC026(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic026FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	wantFSAOrder := []string{"BASIC-026-FSA-SP", "BASIC-026-FSA-SY"}
	for i, want := range wantFSAOrder {
		if got := fsaList.FunctionSetAssignments[i].MRID; got != want {
			t.Errorf("FSAList[%d].MRID = %q, want %q", i, got, want)
		}
	}

	// Step 3: the SY and SP DERProgramLists each hold only their own program.
	for _, fsa := range fsaList.FunctionSetAssignments {
		assertFSAProgramList(t, ctx, client, fsa, []wantProgram{basic026Programs[fsa.MRID]})
	}

	assertBASIC026DDERCs(t, ctx, client)

	entries := assertBASIC026Events(t, ctx, client)

	winners := assertOverlapResolution(t, entries)
	if len(winners) != 0 {
		t.Errorf("independent overlap: expected empty winners map, got %v", winners)
	}
}

func assertBASIC026DDERCs(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/dderc"
		dderc := walkDefaultDERControlByHref(t, ctx, c, href)
		want := basic026DDERCs[prog]
		if dderc.MRID != want.mrid {
			t.Errorf("[%s] DDERC.MRID = %q, want %q", prog, dderc.MRID, want.mrid)
		}
		assertOpModValue(t, "DDERC["+prog+"]", dderc.DERControlBase, want.field, want.value)
	}
}

func assertBASIC026Events(t *testing.T, ctx context.Context, c *csiptest.Client) []overlapEntry {
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
		want := basic026Events[prog]
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
