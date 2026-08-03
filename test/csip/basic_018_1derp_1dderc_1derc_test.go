// CSIP V1.2 Section 8.18 - Non-overlap event prioritization, 1 DERP / 1 DDERC / 1 DERC.
//
// BASIC-018 proves that a CSIP server seeded with one DERProgram
// carrying both a DefaultDERControl AND one scheduled DERControl
// renders the full priority chain over chained GETs. Per V1.2 Section 8.18
// the procedure asserts the client applies the DDERC outside the
// event window and transitions to the DERControl inside the window;
// server-side just asserts the fixture renders.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.18):
//
//	Step 1 (server has 1 DERP + 1 DDERC + 1 DERC scheduled)    -> fixture load
//	Step 2 (walk /dcap -> /edev -> /fsa -> DERProgram)            -> walkToFirstEDevFSAList + walkProgramListByHref
//	Step 3 (DDERC carries opModFixedW = 2 kW)                   -> assertBASIC018DDERC
//	Step 4 (DERControlList has 1 scheduled event)               -> walkDERControlListByHref
//	Step 5 (DERControl Interval [t0+60, t0+180), Status=0,      -> assertBASIC018DERControl
//	         opModFixedW = 3 kW)
//
// Run under both GCM and CCM cipher modes.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic018FixtureName = "basic-018-1derp-1dderc-1derc.yaml"

// basic018DDERCFixedW is the DDERC fallback value the fixture seeds (2 kW).
const basic018DDERCFixedW int16 = 2000

// basic018EventStart is the Interval.Start the fixture seeds for the
// scheduled DERControl (t0+60s).
const basic018EventStart int64 = 1700000060

// basic018EventDuration is the Interval.Duration the fixture seeds (2 min).
const basic018EventDuration uint32 = 120

// basic018DERCFixedW is the in-event value the fixture seeds (3 kW).
const basic018DERCFixedW int16 = 3000

// TestBASIC_018_OneDERPOneDDERCOneDERC implements CSIP V1.2 Section 8.18.
func TestBASIC_018_OneDERPOneDDERCOneDERC(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC018)
}

func runBASIC018(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic018FixtureName, extraOpts)
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
	if prog.DefaultDERControlLink == nil {
		t.Fatal("DERProgram.DefaultDERControlLink is nil - fixture topology drift")
	}
	if prog.DERControlListLink == nil {
		t.Fatal("DERProgram.DERControlListLink is nil - fixture topology drift")
	}

	// Step 3: DDERC carries the fallback opModFixedW.
	dderc := walkDefaultDERControlByHref(t, ctx, client, prog.DefaultDERControlLink.Href)
	assertBASIC018DDERC(t, dderc)

	// Step 4: DERControlList has exactly one scheduled event.
	ctrlList := walkDERControlListByHref(t, ctx, client, prog.DERControlListLink.Href)
	if got := int(ctrlList.All); got != 1 {
		t.Fatalf("DERControlList.All = %d, want 1", got)
	}
	if got := len(ctrlList.DERControl); got != 1 {
		t.Fatalf("len(DERControlList.DERControl) = %d, want 1", got)
	}

	// Step 5: assert per-field round-trip on the lone DERControl.
	assertBASIC018DERControl(t, ctrlList.DERControl[0])
}

func assertBASIC018DDERC(t *testing.T, dderc sep2.DefaultDERControl) {
	t.Helper()

	if dderc.MRID != "BASIC-018-DDERC" {
		t.Errorf("DDERC.MRID = %q, want BASIC-018-DDERC", dderc.MRID)
	}
	if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
		t.Fatal("DDERC.DERControlBase.OpModFixedW is nil - fixture dropped")
	}
	if got := dderc.DERControlBase.OpModFixedW.Value; got != basic018DDERCFixedW {
		t.Errorf("DDERC.OpModFixedW.Value = %d, want %d", got, basic018DDERCFixedW)
	}
}

func assertBASIC018DERControl(t *testing.T, dc sep2.DERControl) {
	t.Helper()

	if dc.MRID != "BASIC-018-DERC-A" {
		t.Errorf("DERControl.MRID = %q, want BASIC-018-DERC-A", dc.MRID)
	}
	assertEventStatus(t, "BASIC-018-DERC-A", dc.EventStatus, sep2.EventStatusScheduled)
	assertEventInterval(t, "BASIC-018-DERC-A", dc.Interval,
		basic018EventStart, basic018EventDuration)
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatal("DERControl.DERControlBase.OpModFixedW is nil - fixture dropped")
	}
	if got := dc.DERControlBase.OpModFixedW.Value; got != basic018DERCFixedW {
		t.Errorf("DERControl.OpModFixedW.Value = %d, want %d", got, basic018DERCFixedW)
	}
}
