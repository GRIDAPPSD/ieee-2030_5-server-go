// CSIP V1.2 Section 8.2 - Group Management (Basic).
//
// BASIC-002 proves that a CSIP server seeded with a 7-level FSA group
// management topology renders the priority chain end-to-end AND that
// the closest-to-inverter program (L6-device) carries both a populated
// DefaultDERControl AND an active DERControl, while the other six
// levels (L0..L5) advertise their links but carry empty/missing
// DERControl payloads.
//
// CSIP V1.2 Section 8.2 wording: "the Service Point closest to inverter has
// highest priority DERProgram + DefaultDERControl." On the server side
// "highest priority" is decorative in today's implementation - the
// store keys DERPrograms by EndDevice id only (router.go
// scopedListHandler) so every FSA's walk returns all 7 programs in
// primacy order. The procedural assertion CORE-010 already pins down
// is re-asserted here as a regression guard; what BASIC-002 adds is:
//
//  1. A populated DefaultDERControl on L6 (opModMaxLimW = 4 kW) and
//     an empty DefaultDERControl on each of L0..L5 (the handler
//     returns 200 OK with an empty body when the resource is missing,
//     per HandleSingletonGetPut's "Return empty default" branch).
//  2. A single active DERControl on L6 carrying opModFixedW = 3 kW.
//     L0..L5 each render an empty DERControlList (all=0).
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 8.2):
//
//	Step 1 (server has 7 FSAs, primacy 0..6)        -> assertSevenLevelPriorityChain
//	Step 2 (client walks /dcap -> /edev -> /fsa)      -> walkAllFSAsBasic002
//	Step 3 (client follows each FSA's program list) -> assertProgramsScopedByEDev
//	Step 4 (L6 DefaultDERControl carries opModMaxLimW = 4 kW)
//	                                                 -> assertL6DefaultControlPopulated
//	Step 5 (L0..L5 DefaultDERControl is the empty
//	         default - DERControlBase == nil)        -> assertOtherLevelsDefaultEmpty
//	Step 6 (L6 DERControlList has exactly 1 active
//	         DERControl with opModFixedW = 3 kW)    -> assertL6ActiveControlPresent
//	Step 7 (L0..L5 DERControlList is empty, all=0)  -> assertOtherLevelsControlListEmpty
//
// Run under both GCM and CCM cipher modes so the spec cipher path is
// exercised end-to-end on the same multi-level walk procedure.
package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic002LevelCount is the level count of the BASIC-002 fixture.
const basic002LevelCount = 7

// basic002ClosestLevelID is the FSA / DERProgram id of the closest-to-
// inverter level (L6-device). The id encodes the priority chain
// position; the loader uses it as the store key.
const basic002ClosestLevelID = "6"

// basic002DDERCLimitValue is the opModMaxLimW value carried by L6's
// DefaultDERControl per the fixture (4 kW, multiplier 0).
const basic002DDERCLimitValue int16 = 4000

// basic002DERCFixedW is the opModFixedW target operating point
// carried by the L6 DERControl per the fixture (3 kW, multiplier 0).
const basic002DERCFixedW int16 = 3000

// TestBASIC_002_GroupManagement implements CSIP V1.2 Section 8.2.
func TestBASIC_002_GroupManagement(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name string
		opts []csiptest.BootOption
	}{
		{name: "GCM", opts: nil},
		{name: "CCM", opts: []csiptest.BootOption{csiptest.WithCCMMode()}},
	} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			runBASIC002(t, mode.opts)
		})
	}
}

// runBASIC002 executes the Section 8.2 procedure once against a freshly
// booted server seeded with basic-002-group-management.yaml.
func runBASIC002(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithBasicFixture(t, "basic-002-group-management.yaml", extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Steps 1+2: walk to the FSA list. Expect 7 FSAs in primacy order.
	fsaList := walkAllFSAsBasic002(t, ctx, client)
	if got := int(fsaList.All); got != basic002LevelCount {
		t.Fatalf("FSAList.All = %d, want %d", got, basic002LevelCount)
	}
	if got := len(fsaList.FunctionSetAssignments); got != basic002LevelCount {
		t.Fatalf("len(FSAList.FunctionSetAssignments) = %d, want %d",
			got, basic002LevelCount)
	}

	// Step 3: every FSA's DERProgramList renders the full 7-program
	// chain (store is scoped by EndDevice id only - see CORE-010 doc).
	// Asserts the priority chain is intact across all FSA paths.
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d] missing DERProgramListLink", i)
		}
		progs := walkDERProgramListBasic002(t, ctx, client, fsa.DERProgramListLink.Href)
		assertSevenLevelPriorityChain(t, i, progs)
	}

	// Step 4: L6 DefaultDERControl carries opModMaxLimW = 4 kW.
	assertL6DefaultControlPopulated(t, ctx, client)

	// Step 5: L0..L5 DefaultDERControl is the empty default - handler
	// returns 200 OK with DERControlBase == nil per
	// HandleSingletonGetPut's "Return empty default" branch.
	assertOtherLevelsDefaultEmpty(t, ctx, client)

	// Step 6: L6 DERControlList carries exactly one active DERControl
	// with opModFixedW = 3 kW.
	assertL6ActiveControlPresent(t, ctx, client)

	// Step 7: L0..L5 DERControlList is empty (all=0, no items).
	assertOtherLevelsControlListEmpty(t, ctx, client)
}

// walkAllFSAsBasic002 walks /dcap -> /edev -> first EndDevice -> /fsa,
// returning the FSA list. Mirrors CORE-013's helper of the same shape
// - kept local to the BASIC-002 test rather than dedup'd into a shared
// helper because BASIC-002 needs a stronger pre-condition assertion
// (7 levels, primacy chain intact) than CORE-013's "non-empty list"
// check; folding both into one helper would obscure the per-test
// procedure contract.
func walkAllFSAsBasic002(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) sep2.FunctionSetAssignmentsList {
	t.Helper()

	dcap, err := c.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if dcap.EndDeviceListLink == nil {
		t.Fatal("DeviceCapability.EndDeviceListLink is nil")
	}

	var edevList sep2.EndDeviceList
	if err := c.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &edevList); err != nil {
		t.Fatalf("walk EndDeviceList: %v", err)
	}
	if len(edevList.EndDevice) == 0 {
		t.Fatal("EndDeviceList empty - fixture not loaded?")
	}
	edev := edevList.EndDevice[0]
	if edev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("EndDevice[0].FunctionSetAssignmentsListLink is nil")
	}

	var fsaList sep2.FunctionSetAssignmentsList
	// l=255 includes all 7 entries in one response (default limit
	// would also fit 7, but explicit l= is a regression guard against
	// a future default change).
	if err := c.WalkLink(ctx, sep2.Link{Href: edev.FunctionSetAssignmentsListLink.Href + "?l=255"}, &fsaList); err != nil {
		t.Fatalf("walk FSAList: %v", err)
	}
	return fsaList
}

// walkDERProgramListBasic002 fetches a DERProgramList with l=255 so
// all 7 programs come back in one response.
func walkDERProgramListBasic002(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	href string,
) []sep2.DERProgram {
	t.Helper()

	var list sep2.DERProgramList
	if err := c.WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("walk DERProgramList %s: %v", href, err)
	}
	return list.DERProgram
}

// assertSevenLevelPriorityChain asserts the 7 programs cover primacy
// 0..6 exactly once in the order produced by store-key sort (single-
// digit ids "0".."6" sort lex == numeric, so the wire order matches
// the priority chain).
func assertSevenLevelPriorityChain(t *testing.T, fsaIdx int, programs []sep2.DERProgram) {
	t.Helper()

	if got := len(programs); got != basic002LevelCount {
		t.Fatalf("FSA[%d] DERProgram count = %d, want %d "+
			"(store scopes by EndDevice id only - every FSA path returns all 7)",
			fsaIdx, got, basic002LevelCount)
	}
	for i, prog := range programs {
		if got := int(prog.Primacy); got != i {
			t.Errorf("FSA[%d] DERProgram[%d].Primacy = %d, want %d",
				fsaIdx, i, got, i)
		}
	}
}

// assertL6DefaultControlPopulated walks the L6 DefaultDERControl link
// and asserts opModMaxLimW.Value = basic002DDERCLimitValue (4 kW). The
// "closest-to-inverter program carries the active operating envelope"
// leg of the procedure.
func assertL6DefaultControlPopulated(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) {
	t.Helper()

	href := "/edev/0/fsa/" + basic002ClosestLevelID + "/derp/" + basic002ClosestLevelID + "/dderc"
	var dderc sep2.DefaultDERControl
	if err := c.WalkLink(ctx, sep2.Link{Href: href}, &dderc); err != nil {
		t.Fatalf("walk L6 DefaultDERControl %s: %v", href, err)
	}
	if dderc.MRID != "BASIC-002-L6-DDERC" {
		t.Errorf("L6 DefaultDERControl.MRID = %q, want BASIC-002-L6-DDERC", dderc.MRID)
	}
	if dderc.DERControlBase == nil {
		t.Fatalf("L6 DefaultDERControl.DERControlBase is nil - fixture dropped on the wire")
	}
	if dderc.DERControlBase.OpModMaxLimW == nil {
		t.Fatalf("L6 DefaultDERControl.OpModMaxLimW is nil")
	}
	if got := dderc.DERControlBase.OpModMaxLimW.Value; got != basic002DDERCLimitValue {
		t.Errorf("L6 OpModMaxLimW.Value = %d, want %d", got, basic002DDERCLimitValue)
	}
	if got := dderc.DERControlBase.OpModMaxLimW.Multiplier; got != 0 {
		t.Errorf("L6 OpModMaxLimW.Multiplier = %d, want 0", got)
	}
}

// assertOtherLevelsDefaultEmpty walks /edev/0/fsa/{i}/derp/{i}/dderc
// for i in 0..5 and asserts each returns 200 OK with DERControlBase ==
// nil (the handler's "Return empty default" branch). This pins down
// the procedure assertion that ONLY the closest-to-inverter program
// carries a populated default - the other six levels have empty
// defaults under the same fixture.
func assertOtherLevelsDefaultEmpty(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) {
	t.Helper()

	for i := 0; i < basic002LevelCount-1; i++ {
		href := "/edev/0/fsa/" + intToID(i) + "/derp/" + intToID(i) + "/dderc"
		var dderc sep2.DefaultDERControl
		if err := c.WalkLink(ctx, sep2.Link{Href: href}, &dderc); err != nil {
			t.Errorf("walk L%d DefaultDERControl %s: %v", i, href, err)
			continue
		}
		if dderc.DERControlBase != nil {
			t.Errorf("L%d DefaultDERControl.DERControlBase = %+v, want nil "+
				"(fixture only seeds L6's default; other levels return the "+
				"handler's empty default per HandleSingletonGetPut)",
				i, dderc.DERControlBase)
		}
	}
}

// assertL6ActiveControlPresent walks the L6 DERControlList and asserts
// exactly one active DERControl carrying opModFixedW = 3 kW.
func assertL6ActiveControlPresent(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) {
	t.Helper()

	href := "/edev/0/fsa/" + basic002ClosestLevelID + "/derp/" + basic002ClosestLevelID + "/derc"
	var list sep2.DERControlList
	if err := c.WalkLink(ctx, sep2.Link{Href: href}, &list); err != nil {
		t.Fatalf("walk L6 DERControlList %s: %v", href, err)
	}
	if got := int(list.All); got != 1 {
		t.Fatalf("L6 DERControlList.All = %d, want 1", got)
	}
	if got := len(list.DERControl); got != 1 {
		t.Fatalf("len(L6 DERControlList.DERControl) = %d, want 1", got)
	}
	dc := list.DERControl[0]
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatalf("L6 DERControl[0].OpModFixedW is nil - fixture dropped")
	}
	if got := dc.DERControlBase.OpModFixedW.Value; got != basic002DERCFixedW {
		t.Errorf("L6 DERControl[0].OpModFixedW.Value = %d, want %d",
			got, basic002DERCFixedW)
	}
}

// assertOtherLevelsControlListEmpty walks each L0..L5 DERControlList
// and asserts All=0 / empty slice - confirming the "only the
// closest-level program has active events" leg of the procedure.
func assertOtherLevelsControlListEmpty(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) {
	t.Helper()

	for i := 0; i < basic002LevelCount-1; i++ {
		href := "/edev/0/fsa/" + intToID(i) + "/derp/" + intToID(i) + "/derc"
		var list sep2.DERControlList
		if err := c.WalkLink(ctx, sep2.Link{Href: href}, &list); err != nil {
			t.Errorf("walk L%d DERControlList %s: %v", i, href, err)
			continue
		}
		if list.All != 0 {
			t.Errorf("L%d DERControlList.All = %d, want 0", i, list.All)
		}
		if len(list.DERControl) != 0 {
			t.Errorf("L%d DERControlList.DERControl len = %d, want 0",
				i, len(list.DERControl))
		}
	}
}

// intToID returns the single-digit string id for level i (0..9). The
// BASIC-002 fixture uses single-digit ids 0..6, matching CORE-010.
func intToID(i int) string {
	return string(rune('0' + i))
}

// bootWithBasicFixture loads the named fixture under fixtures/ into a
// fresh store set and boots an in-process server. Shared across the
// BASIC-002..012 tests - every BASIC-NNN test in #135 has the
// same boot shape (load YAML, then walk via TLS client). Centralizing
// the 12-line boot pattern keeps each procedure test focused on its
// per-mode assertions.
func bootWithBasicFixture(
	t *testing.T,
	fixture string,
	extraOpts []csiptest.BootOption,
) *csiptest.BootedServer {
	t.Helper()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms, // #165 wrapper; #175.
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	owner := csiptest.NewDeviceIdentity(t, "BASIC-FIXTURE-DEVICE")
	path := filepath.Join("fixtures", fixture)
	if err := csiptest.Load(context.Background(), target, path, csiptest.Bind(fixtureEndDeviceID, owner)); err != nil {
		t.Fatalf("load %s: %v", path, err)
	}

	opts := append([]csiptest.BootOption{csiptest.WithStores(stores), csiptest.WithDeviceIdentity(owner)}, extraOpts...)
	return csiptest.BootServer(t, opts...)
}
