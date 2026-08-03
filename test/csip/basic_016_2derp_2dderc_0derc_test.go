// CSIP V1.2 Section 8.16 - Non-overlap event prioritization, 2 DERP / 2 DDERC / 0 DERC.
//
// BASIC-016 proves that a CSIP server seeded with two DERPrograms
// (Service Point at primacy 0, Smart Inverter Yard at primacy 1),
// each carrying its own DefaultDERControl (no DERControl events at
// all), renders the full priority chain correctly over chained GETs.
// Per V1.2 Section 8.16 the procedure asserts that the client applies the
// highest-primacy DDERC outside any event window - server-side
// here we just assert the topology + primacy + DDERC payloads
// round-trip exactly as the fixture seeded them.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.16):
//
//	Step 1 (server has 2 DERPrograms SP@0, SY@1)         -> fixture load
//	Step 2 (client walks /dcap -> /edev -> /fsa list)      -> walkToFirstEDevFSAList
//	Step 3 (each FSA's DERProgramList renders both       -> assertBASIC016ProgramList
//	         programs in primacy order)
//	Step 4 (SP DDERC carries opModFixedW = 4 kW)         -> assertBASIC016DDERCPayload (SP)
//	Step 5 (SY DDERC carries opModFixedW = 2 kW)         -> assertBASIC016DDERCPayload (SY)
//	Step 6 (no DERControl events on either program)      -> assertBASIC016NoEvents
//
// Run under both GCM and CCM cipher modes so the spec cipher path
// (CCM-8) is exercised end-to-end on the multi-program walk.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic016FixtureName is the YAML fixture seeded by every BASIC-016
// run. Hoisted to package-level so a fixture rename surfaces in one
// place rather than per-cipher.
const basic016FixtureName = "basic-016-2derp-2dderc-0derc.yaml"

// basic016ProgramCount is the expected DERProgram count rendered at
// each FSA's DERProgramList. Per the server's scopedListHandler the
// program list scopes by EndDevice id only (see BASIC-002 / CORE-010
// doc), so EVERY FSA path returns both programs.
const basic016ProgramCount = 2

// basic016SPFixedW is the opModFixedW value seeded by the SP DDERC
// per the fixture (4 kW, multiplier 0).
const basic016SPFixedW int16 = 4000

// basic016SYFixedW is the opModFixedW value seeded by the SY DDERC
// per the fixture (2 kW, multiplier 0).
const basic016SYFixedW int16 = 2000

// TestBASIC_016_TwoDERPTwoDDERCZeroDERC implements CSIP V1.2 Section 8.16.
func TestBASIC_016_TwoDERPTwoDDERCZeroDERC(t *testing.T) {
	t.Parallel()
	runUnderBothCiphers(t, runBASIC016)
}

func runBASIC016(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithEventPrioFixture(t, basic016FixtureName, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 1 verified by fixture load (boot would fail loudly on a
	// malformed fixture); no separate assertion needed.

	// Step 2: walk /dcap -> /edev -> /edev/0/fsa. Expect 2 FSAs (SP, SY)
	// in lex-sorted order: "sp" then "sy".
	fsaList := walkToFirstEDevFSAList(t, ctx, client)
	if got := int(fsaList.All); got != 2 {
		t.Fatalf("FSAList.All = %d, want 2", got)
	}
	if got := len(fsaList.FunctionSetAssignments); got != 2 {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want 2", got)
	}
	// FSA store sorts keys lexically - "sp" sorts before "sy".
	wantFSAOrder := []string{"BASIC-016-FSA-SP", "BASIC-016-FSA-SY"}
	for i, want := range wantFSAOrder {
		if got := fsaList.FunctionSetAssignments[i].MRID; got != want {
			t.Errorf("FSAList[%d].MRID = %q, want %q", i, got, want)
		}
	}

	// Step 3: each FSA's DERProgramList renders BOTH programs (store
	// scopes by EndDevice id only). Assert primacy chain is intact at
	// each FSA path.
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d] missing DERProgramListLink", i)
		}
		progList := walkProgramListByHref(t, ctx, client, fsa.DERProgramListLink.Href)
		assertBASIC016ProgramList(t, fsa.MRID, progList)
	}

	// Steps 4 + 5: walk SP and SY DDERC links directly and assert the
	// per-program opModFixedW value.
	spDDERC := walkDefaultDERControlByHref(t, ctx, client, "/edev/0/fsa/sp/derp/sp/dderc")
	assertBASIC016DDERCPayload(t, "SP", spDDERC, "BASIC-016-DDERC-SP", basic016SPFixedW)
	syDDERC := walkDefaultDERControlByHref(t, ctx, client, "/edev/0/fsa/sy/derp/sy/dderc")
	assertBASIC016DDERCPayload(t, "SY", syDDERC, "BASIC-016-DDERC-SY", basic016SYFixedW)

	// Step 6: no DERControl events on either program - both lists
	// render All=0 / empty slice.
	for _, prog := range []string{"sp", "sy"} {
		href := "/edev/0/fsa/" + prog + "/derp/" + prog + "/derc"
		list := walkDERControlListByHref(t, ctx, client, href)
		if list.All != 0 {
			t.Errorf("%s DERControlList.All = %d, want 0", prog, list.All)
		}
		if len(list.DERControl) != 0 {
			t.Errorf("%s DERControlList.DERControl len = %d, want 0",
				prog, len(list.DERControl))
		}
	}
}

// assertBASIC016ProgramList asserts a DERProgramList renders both
// programs (SP@0, SY@1) in primacy order with the expected MRIDs.
func assertBASIC016ProgramList(t *testing.T, fsaLabel string, progList sep2.DERProgramList) {
	t.Helper()

	if got := int(progList.All); got != basic016ProgramCount {
		t.Errorf("[%s] DERProgramList.All = %d, want %d",
			fsaLabel, got, basic016ProgramCount)
	}
	if got := len(progList.DERProgram); got != basic016ProgramCount {
		t.Fatalf("[%s] len(DERProgramList.DERProgram) = %d, want %d",
			fsaLabel, got, basic016ProgramCount)
	}
	// Store-key sort is lex: "sp" < "sy", so SP renders first.
	wantPrograms := []struct {
		mrid    string
		primacy uint8
	}{
		{mrid: "BASIC-016-DERP-SP", primacy: 0},
		{mrid: "BASIC-016-DERP-SY", primacy: 1},
	}
	for i, want := range wantPrograms {
		got := progList.DERProgram[i]
		if got.MRID != want.mrid {
			t.Errorf("[%s] DERProgram[%d].MRID = %q, want %q",
				fsaLabel, i, got.MRID, want.mrid)
		}
		if got.Primacy != want.primacy {
			t.Errorf("[%s] DERProgram[%d].Primacy = %d, want %d",
				fsaLabel, i, got.Primacy, want.primacy)
		}
		if got.DefaultDERControlLink == nil {
			t.Errorf("[%s] DERProgram[%d] (%s) missing DefaultDERControlLink",
				fsaLabel, i, got.MRID)
		}
	}
}

// assertBASIC016DDERCPayload asserts a DefaultDERControl carries the
// expected MRID + opModFixedW value. Surfaces "loader dropped the
// payload" regressions clearly with the SP/SY label so failures point
// at the right fixture leg.
func assertBASIC016DDERCPayload(
	t *testing.T,
	label string,
	dderc sep2.DefaultDERControl,
	wantMRID string,
	wantFixedW int16,
) {
	t.Helper()

	if dderc.MRID != wantMRID {
		t.Errorf("%s DDERC.MRID = %q, want %q", label, dderc.MRID, wantMRID)
	}
	if dderc.DERControlBase == nil {
		t.Fatalf("%s DDERC.DERControlBase is nil - fixture dropped on the wire", label)
	}
	if dderc.DERControlBase.OpModFixedW == nil {
		t.Fatalf("%s DDERC.OpModFixedW is nil", label)
	}
	if got := dderc.DERControlBase.OpModFixedW.Value; got != wantFixedW {
		t.Errorf("%s DDERC.OpModFixedW.Value = %d, want %d",
			label, got, wantFixedW)
	}
	if got := dderc.DERControlBase.OpModFixedW.Multiplier; got != 0 {
		t.Errorf("%s DDERC.OpModFixedW.Multiplier = %d, want 0", label, got)
	}
}
