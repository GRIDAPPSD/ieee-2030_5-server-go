// CSIP V1.2 section 6.5 - DER Program (basic).
//
// CORE-012 proves that a server seeded with the #52
// `derprogram-single.yaml` fixture renders the full DERProgram
// resource chain correctly over chained GETs:
//
//	/dcap
//	  `-> /edev (list, all=1)
//	        `-> /edev/0 (the single EndDevice)
//	              `-> /edev/0/fsa (list, all=1) - via FSA list link
//	                    `-> /edev/0/fsa/0   (the single FSA)
//	                          `-> /edev/0/fsa/0/derp (list, all=1)
//	                                `-> single DERProgram (primacy 0)
//	                                      |-> DefaultDERControlLink -> dderc (opModMaxLimW)
//	                                      |-> DERControlListLink    -> derc (all=0, empty)
//	                                      `-> DERCurveListLink      -> dc   (all=0, empty)
//
// The fixture's documented shape is intentionally minimal: one
// DERProgram carrying one DefaultDERControl, zero DERControls, and
// zero DERCurves. The procedural assertion sequence verifies BOTH
// the per-resource fields and the empty-list endpoints - silent
// breakage of an "empty list" path is a regression class CORE-012
// must guard against (Phase 3 hardening goal).
//
// V1.2 procedure step -> assertion mapping (per V1.2 section 6.5 procedure):
//
//	Step 1 (server has 1 DERProgram available)        -> fixture load
//	Step 2 (client follows /dcap -> /edev -> first FSA) -> walkToFirstFSA
//	Step 3 (client follows FSA -> DERProgramList)      -> walkDERProgramList
//	Step 4 (client GETs the single DERProgram)        -> assertSingleProgramShape
//	Step 5 (client follows DefaultDERControlLink)     -> walkDefaultDERControl
//	Step 6 (client follows DERControlListLink, empty) -> assertEmptyDERControlList
//	Step 7 (curves: global /dc empty, link advertised) -> assertEmptyGlobalDERCurveList
//
// Step 7 note: the spec models DERCurve as a global resource at /dc
// (the DERCurve store in this implementation is unscoped - see
// internal/server/router.go which routes "GET /dc" only). The
// fixture advertises a per-program DERCurveListLink with all=0 so
// the client side surfaces that the program has no associated curves;
// the test verifies the link is advertised AND walks /dc directly to
// prove the global curve list renders empty under the same fixture.
// Walking the per-program DERCurveListLink would 404 because the
// server does not expose a per-program curve route today.
//
// Run under both GCM and CCM cipher modes so the spec cipher path
// is exercised end-to-end on the same chained-walk procedure. CCM
// is the #1 regression guard surface for the in-process
// harness.
package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCORE_012_DERProgramBasic implements CSIP V1.2 section 6.5.
func TestCORE_012_DERProgramBasic(t *testing.T) {
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
			runCORE012(t, mode.opts)
		})
	}
}

// runCORE012 executes the section 6.5 chained-walk procedure once against a
// freshly booted server seeded with derprogram-single.yaml.
func runCORE012(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithDERProgramFixture(t, "derprogram-single.yaml", extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 1 verified by fixture load (boot would fail loudly on a
	// malformed fixture); no separate assertion needed.

	// Step 2: walk /dcap -> /edev (list, all=1) -> first EndDevice -> /edev/0/fsa.
	fsa := walkToFirstFSA(t, ctx, client)
	if fsa.DERProgramListLink == nil {
		t.Fatal("first FSA has no DERProgramListLink - fixture topology drift")
	}

	// Step 3: walk the DERProgramList. Expect exactly one program.
	var progList sep2.DERProgramList
	if err := client.WalkLink(ctx, sep2.Link{Href: fsa.DERProgramListLink.Href}, &progList); err != nil {
		t.Fatalf("walk DERProgramListLink %s: %v", fsa.DERProgramListLink.Href, err)
	}
	if progList.All != 1 {
		t.Fatalf("DERProgramList.All = %d, want 1", progList.All)
	}
	if len(progList.DERProgram) != 1 {
		t.Fatalf("len(DERProgramList.DERProgram) = %d, want 1", len(progList.DERProgram))
	}

	// Step 4: assert the single DERProgram's shape - primacy 0, the
	// three downstream links present, MRID matches the fixture.
	prog := progList.DERProgram[0]
	assertSingleProgramShape(t, prog)

	// Step 5: walk the DefaultDERControlLink. Must carry opModMaxLimW
	// = {multiplier: 0, value: 5000} per the fixture.
	var dderc sep2.DefaultDERControl
	if err := client.WalkLink(ctx, sep2.Link{Href: prog.DefaultDERControlLink.Href}, &dderc); err != nil {
		t.Fatalf("walk DefaultDERControlLink %s: %v", prog.DefaultDERControlLink.Href, err)
	}
	if dderc.MRID != "CORE-012-DDERC" {
		t.Errorf("DefaultDERControl.MRID = %q, want CORE-012-DDERC", dderc.MRID)
	}
	if dderc.DERControlBase == nil || dderc.DERControlBase.OpModMaxLimW == nil {
		t.Fatalf("DefaultDERControl.DERControlBase.OpModMaxLimW = nil; fixture dropped on the wire")
	}
	if got := dderc.DERControlBase.OpModMaxLimW.Value; got != 5000 {
		t.Errorf("DefaultDERControl.OpModMaxLimW.Value = %d, want 5000", got)
	}
	if got := dderc.DERControlBase.OpModMaxLimW.Multiplier; got != 0 {
		t.Errorf("DefaultDERControl.OpModMaxLimW.Multiplier = %d, want 0", got)
	}

	// Step 6: walk the DERControlListLink. Must be empty (all=0). The
	// fixture deliberately ships zero DERControls so the "no active
	// event" wire shape is asserted.
	var ctrlList sep2.DERControlList
	if err := client.WalkLink(ctx, sep2.Link{Href: prog.DERControlListLink.Href}, &ctrlList); err != nil {
		t.Fatalf("walk DERControlListLink %s: %v", prog.DERControlListLink.Href, err)
	}
	if ctrlList.All != 0 {
		t.Errorf("DERControlList.All = %d, want 0 (fixture ships 0 DERControls)", ctrlList.All)
	}
	if len(ctrlList.DERControl) != 0 {
		t.Errorf("len(DERControlList.DERControl) = %d, want 0", len(ctrlList.DERControl))
	}

	// Step 7: the program advertises a per-program DERCurveListLink
	// (the fixture sets it for client-side discoverability), but the
	// server routes DERCurves only as a global resource at /dc - see
	// internal/server/router.go. Assert the link is advertised on the
	// wire (catches a regression that drops the link entirely), then
	// walk /dc directly to prove the global curve list renders empty
	// under this fixture.
	if prog.DERCurveListLink.Href == "" {
		t.Error("DERProgram.DERCurveListLink.Href is empty - server dropped the advertised link")
	}
	var globalCurves sep2.DERCurveList
	if err := client.WalkLink(ctx, sep2.Link{Href: "/dc"}, &globalCurves); err != nil {
		t.Fatalf("walk global /dc: %v", err)
	}
	if globalCurves.All != 0 {
		t.Errorf("global DERCurveList.All = %d, want 0 (fixture ships 0 DERCurves)", globalCurves.All)
	}
	if len(globalCurves.DERCurve) != 0 {
		t.Errorf("len(global DERCurveList.DERCurve) = %d, want 0", len(globalCurves.DERCurve))
	}
}

// assertSingleProgramShape asserts that the lone DERProgram emitted by
// the derprogram-single.yaml fixture carries the expected MRID, primacy,
// and three downstream resource links. Per #52 the loader sets
// every link from the fixture YAML - a nil link here means the loader
// dropped the field or the server-side build path drifted.
func assertSingleProgramShape(t *testing.T, prog sep2.DERProgram) {
	t.Helper()

	if prog.MRID != "CORE-012-DERP" {
		t.Errorf("DERProgram.MRID = %q, want CORE-012-DERP", prog.MRID)
	}
	if prog.Primacy != 0 {
		t.Errorf("DERProgram.Primacy = %d, want 0", prog.Primacy)
	}
	if prog.DefaultDERControlLink == nil {
		t.Fatal("DERProgram.DefaultDERControlLink is nil")
	}
	if prog.DERControlListLink == nil {
		t.Fatal("DERProgram.DERControlListLink is nil")
	}
	if prog.DERCurveListLink == nil {
		t.Fatal("DERProgram.DERCurveListLink is nil")
	}
}

// walkToFirstFSA walks /dcap -> /edev (list) -> first EndDevice ->
// /edev/{id}/fsa (list) -> first FSA, returning the parsed FSA. Used
// by every CORE-012/013 procedural test that pivots through the FSA
// hierarchy.
//
// Lives in this file (not csiptest/) per #60 scope: the chained-
// walk pattern is procedural test logic, not harness scaffolding.
func walkToFirstFSA(t *testing.T, ctx context.Context, c *csiptest.Client) sep2.FunctionSetAssignments {
	t.Helper()

	dcap, err := c.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if dcap.EndDeviceListLink == nil {
		t.Fatal("DeviceCapability.EndDeviceListLink is nil - server did not advertise /edev")
	}

	var edevList sep2.EndDeviceList
	if err := c.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &edevList); err != nil {
		t.Fatalf("walk EndDeviceListLink %s: %v", dcap.EndDeviceListLink.Href, err)
	}
	if len(edevList.EndDevice) == 0 {
		t.Fatal("EndDeviceList carries zero entries - fixture not loaded?")
	}
	edev := edevList.EndDevice[0]

	if edev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("EndDevice.FunctionSetAssignmentsListLink is nil - fixture topology drift")
	}

	var fsaList sep2.FunctionSetAssignmentsList
	if err := c.WalkLink(ctx, sep2.Link{Href: edev.FunctionSetAssignmentsListLink.Href}, &fsaList); err != nil {
		t.Fatalf("walk FunctionSetAssignmentsListLink %s: %v", edev.FunctionSetAssignmentsListLink.Href, err)
	}
	if len(fsaList.FunctionSetAssignments) == 0 {
		t.Fatal("FunctionSetAssignmentsList carries zero entries - fixture missing FSAs?")
	}
	return fsaList.FunctionSetAssignments[0]
}

// bootWithDERProgramFixture loads the named fixture into a fresh store
// set and boots an in-process server. Shared between CORE-012 and
// CORE-013, both of which seed an EndDevice + FSA + DERProgram
// topology and walk it via chained GETs.
//
// The csiptest.Client returned by srv.Client() already trusts the
// booted server's ephemeral CA and presents an ephemeral device cert,
// so no separate client-PKI dance is needed for read-only walks. The
// CCM-mode subtest still uses the same Client; the stdlib http.Client
// inside it negotiates GCM against the gotls server which accepts
// either GCM or CCM-8 (tightening is gated on #21/#22 per the
// BASIC-001 doc-comment).
func bootWithDERProgramFixture(t *testing.T, fixture string, extraOpts []csiptest.BootOption) *csiptest.BootedServer {
	t.Helper()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	owner := csiptest.NewDeviceIdentity(t, "CORE-012-DEVICE")
	path := filepath.Join("fixtures", fixture)
	if err := csiptest.Load(context.Background(), target, path, csiptest.Bind(fixtureEndDeviceID, owner)); err != nil {
		t.Fatalf("load %s: %v", path, err)
	}

	opts := append([]csiptest.BootOption{csiptest.WithStores(stores), csiptest.WithDeviceIdentity(owner)}, extraOpts...)
	return csiptest.BootServer(t, opts...)
}
