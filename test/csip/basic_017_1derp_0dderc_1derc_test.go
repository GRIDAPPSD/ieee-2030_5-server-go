// CSIP V1.2 Section 8.17 - Non-overlap event prioritization, 1 DERP / 0 DDERC / 1 DERC.
//
// BASIC-017 proves that a CSIP server seeded with one DERProgram
// carrying NO DefaultDERControl and exactly one active DERControl
// renders the lone event correctly over chained GETs. Per V1.2 Section 8.17
// the procedure asserts that the client transitions into the event
// at Interval.Start and out at Interval.Start+Duration; server-side
// just asserts the fixture's MRID + Interval + EventStatus + opMod*
// round-trip on the wire.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.17):
//
//	Step 1 (server has 1 DERProgram, 0 DDERC, 1 DERC active)  -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa -> DERProgram)   -> walkToFirstEDevFSAList + walkProgramListByHref
//	Step 3 (DDERC link MAY be absent / DDERC renders empty)   -> assertBASIC017DDERCEmpty
//	Step 4 (DERControlList has exactly 1 event)                -> walkDERControlListByHref
//	Step 5 (Event MRID, Interval, EventStatus, opModFixedW)    -> assertBASIC017ActiveControl
//
// Step 3 note: this fixture deliberately omits the
// default_der_control_link off the DERProgram, AND seeds no
// DefaultDERControl entry. The server's HandleSingletonGetPut walks
// the singleton store and, when no record exists, renders the empty
// default response (200 OK with DERControlBase == nil). The test
// asserts that empty-default behavior regardless of whether the
// program's DefaultDERControlLink is advertised - the procedure
// concern is "no operating envelope outside the active event".
//
// Run over CCM-8, the spec cipher path.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const basic017FixtureName = "basic-017-1derp-0dderc-1derc.yaml"

// basic017EventStart is the Interval.Start the fixture seeds for the
// lone DERControl. Hoisted so a fixture-time shift surfaces in one
// place rather than scattered through assertions.
const basic017EventStart int64 = 1700000000

// basic017EventDuration is the Interval.Duration the fixture seeds (2 min).
const basic017EventDuration uint32 = 120

// basic017DERCFixedW is the opModFixedW value seeded on the lone
// DERControl (3500, 35.00%).
const basic017DERCFixedW sep2.SignedPerCent = 3500

// TestBASIC_017_OneDERPZeroDDERCOneDERC implements CSIP V1.2 Section 8.17.
func TestBASIC_017_OneDERPZeroDDERCOneDERC(t *testing.T) {
	t.Parallel()
	runUnderCCM(t, runBASIC017)
}

func runBASIC017(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic017FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 2: walk to the FSA list and pull the single FSA.
	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := len(fsaList.FunctionSetAssignments); got != 1 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 1", got)
	}
	fsa := fsaList.FunctionSetAssignments[0]
	if fsa.DERProgramListLink == nil {
		t.Fatal("FSA.DERProgramListLink is nil")
	}
	progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
	if got := len(progList.DERProgram); got != 1 {
		t.Fatalf("len(DERProgramList.DERProgram) = %d, want 1", got)
	}
	prog := progList.DERProgram[0]
	if prog.MRID != "BASIC-017-DERP" {
		t.Errorf("DERProgram.MRID = %q, want BASIC-017-DERP", prog.MRID)
	}
	if prog.DERControlListLink == nil {
		t.Fatal("DERProgram.DERControlListLink is nil")
	}

	// Step 3: DDERC absence - fixture omits both the singleton entry
	// AND the default_der_control_link on the program. The handler's
	// singleton GET returns 200 + empty (DERControlBase == nil) when
	// hit directly; we use the canonical path to assert that branch.
	assertBASIC017DDERCEmpty(t, ctx, client)

	// Step 4: walk the DERControlList - must carry exactly the one
	// active event.
	ctrlList := walkDERControlListByHref(t, ctx, client, prog.DERControlListLink.Href)
	if got := int(ctrlList.All); got != 1 {
		t.Fatalf("DERControlList.All = %d, want 1", got)
	}
	if got := len(ctrlList.DERControl); got != 1 {
		t.Fatalf("len(DERControlList.DERControl) = %d, want 1", got)
	}

	// Step 5: assert per-field round-trip.
	assertBASIC017ActiveControl(t, ctrlList.DERControl[0])
}

// assertBASIC017DDERCEmpty walks the canonical DDERC path and asserts
// the server returns 200 OK with DERControlBase == nil (the
// HandleSingletonGetPut "no record" branch).
func assertBASIC017DDERCEmpty(t *testing.T, ctx context.Context, c *csiptest.Client) {
	t.Helper()

	dderc := walkDefaultDERControlByHref(t, ctx, c, "/edev/0/fsa/0/derp/0/dderc")
	if dderc.DERControlBase != nil {
		t.Errorf("DDERC.DERControlBase = %+v, want nil "+
			"(fixture deliberately omits the default; server should render the empty default)",
			dderc.DERControlBase)
	}
}

// assertBASIC017ActiveControl asserts the lone DERControl carries the
// expected MRID, EventStatus, Interval, and opModFixedW.
func assertBASIC017ActiveControl(t *testing.T, dc sep2.DERControl) {
	t.Helper()

	if dc.MRID != "BASIC-017-DERC-A" {
		t.Errorf("DERControl.MRID = %q, want BASIC-017-DERC-A", dc.MRID)
	}
	assertEventStatus(t, "BASIC-017-DERC-A", dc.EventStatus, sep2.EventStatusActive)
	assertEventInterval(t, "BASIC-017-DERC-A", dc.Interval,
		basic017EventStart, basic017EventDuration)
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatal("DERControl.DERControlBase.OpModFixedW is nil - fixture dropped on the wire")
	}
	if got := *dc.DERControlBase.OpModFixedW; got != basic017DERCFixedW {
		t.Errorf("DERControl.OpModFixedW = %d, want %d",
			got, basic017DERCFixedW)
	}
}
