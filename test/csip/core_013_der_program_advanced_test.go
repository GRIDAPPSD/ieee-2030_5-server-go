// CSIP V1.2 §6.6 — DER Program (advanced; multi-program priority chain).
//
// CORE-013 proves that a server seeded with the 7-program fixture
// (derprogram-7.yaml — IEEE-065 ships this fixture) renders the
// priority chain correctly:
//
//	/dcap → /edev → /edev/0 → /edev/0/fsa (list, all=7)
//	  └─► each FSA → DERProgramListLink (the 7 programs are
//	        scoped by EndDevice id only in this implementation —
//	        see internal/server/router.go scopedListHandler — so
//	        every FSA's DERProgramList returns all 7 programs in
//	        primacy order)
//	        └─► for each program → DERControlListLink → single
//	              DERControl with
//	                ├─► Interval.Start = T0 + N minutes
//	                ├─► Interval.Duration = 30 s
//	                └─► DERControlBase.OpModFixedPFInjectW populated
//
// The primacy chain is ascending: program N has primacy N. The test
// asserts both the chain ordering and the per-program DERControl
// payload survives the wire roundtrip.
//
// Procedure note on store scoping: the in-memory DERProgram store
// keys by EndDevice id only (see loader.go DERProgramSpec doc-comment
// and router.go scopedListHandler). FSA position is decorative in
// today's server; the test asserts the actual server semantic — every
// FSA's DERProgramList returns the full 7-program set — and proves
// the per-program DERControlList (scoped by edev/fsa/derp tuple at
// the handler) still routes correctly to the seeded controls.
//
// Procedure note on seeding: the IEEE-057 fixture loader's
// DERControlBaseSpec does not currently carry opModFixedPFInjectW
// (FixedPowerFactor shape), and the server's router exposes only
// `GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc` — no PUT/POST path
// for individual DERControls today. The test therefore seeds the 7
// DERControls programmatically via the public store API (one Create
// per program, scoped by the same edev/fsa/derp tuple the handler
// reads) before booting the server, then drives the procedure over
// chained GETs end-to-end.
//
// V1.2 procedure step → assertion mapping (per V1.2 §6.6 procedure):
//
//	Step 1 (server has 7 DERPrograms, primacy 0..6) ──► fixture + store seed
//	Step 2 (client walks /dcap → /edev → /fsa)      ──► walkAllFSAs
//	Step 3 (client follows DERProgramListLink)      ──► walkProgramListForFirstFSA
//	Step 4 (assert primacy chain ascending 0..6)    ──► assertPrimacyChain
//	Step 5 (for each program, follow DERControlList) ──► walkAndAssertControl
//	Step 6 (each DERControl carries FixedPFInjectW)  ──► assertFixedPFInjectW
//
// Run under both GCM and CCM cipher modes so the spec cipher path is
// exercised end-to-end on the same multi-program walk procedure.
package csip_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// core013ProgramCount is the number of DERPrograms in the
// derprogram-7.yaml fixture. Lifted to a named constant so the count
// is not magic-numbered across assertions.
const core013ProgramCount = 7

// core013ControlDuration is the per-DERControl duration in seconds
// (§6.6 procedure: 30 s).
const core013ControlDuration uint32 = 30

// core013T0 is the synthetic base time for the 7 staggered DERControl
// start offsets (T0 + N*60s for primacy N). Picked to be a fixed
// in-test constant so the assertions are deterministic — the wall
// clock plays no role.
const core013T0 int64 = 1_700_000_000

// TestCORE_013_DERProgramAdvanced implements CSIP V1.2 §6.6.
func TestCORE_013_DERProgramAdvanced(t *testing.T) {
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
			runCORE013(t, mode.opts)
		})
	}
}

// runCORE013 boots a server seeded with derprogram-7.yaml plus 7
// programmatically-seeded DERControls, then walks the priority chain
// and asserts per-program wire fidelity.
func runCORE013(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv := bootWithSevenProgramsAndControls(t, extraOpts)
	ctx := context.Background()
	client := srv.Client()

	// Step 2: walk to the FSA list.
	fsaList := walkAllFSAs(t, ctx, client)
	if fsaList.All != core013ProgramCount {
		t.Fatalf("FunctionSetAssignmentsList.All = %d, want %d", fsaList.All, core013ProgramCount)
	}
	if len(fsaList.FunctionSetAssignments) != core013ProgramCount {
		t.Fatalf("len(FunctionSetAssignmentsList.FunctionSetAssignments) = %d, want %d",
			len(fsaList.FunctionSetAssignments), core013ProgramCount)
	}

	// Step 3 + 4: follow the first FSA's DERProgramListLink — in this
	// implementation the DERProgram store is scoped by EndDevice id
	// only, so every FSA's list returns the full 7-program set. We
	// also assert every FSA advertises an equivalent link to catch a
	// regression that loses the link on any individual FSA.
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil {
			t.Fatalf("FSA[%d].DERProgramListLink is nil", i)
		}
	}
	programs := walkProgramListForFirstFSA(t, ctx, client, fsaList)
	assertPrimacyChain(t, programs)

	// Step 5 + 6: for each program, follow DERControlList and assert
	// the lone DERControl carries the expected start offset, duration,
	// and FixedPFInjectW values.
	for i, prog := range programs {
		i, prog := i, prog
		walkAndAssertControl(t, ctx, client, i, prog)
	}
}

// walkAllFSAs walks /dcap → /edev → first EndDevice → /fsa, returning
// the FSA list. CORE-013 expects 7 FSAs in the list.
func walkAllFSAs(t *testing.T, ctx context.Context, c *csiptest.Client) sep2.FunctionSetAssignmentsList {
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
		t.Fatalf("walk /edev: %v", err)
	}
	if len(edevList.EndDevice) == 0 {
		t.Fatal("EndDeviceList empty — fixture not loaded?")
	}
	edev := edevList.EndDevice[0]
	if edev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("EndDevice.FunctionSetAssignmentsListLink is nil")
	}

	var fsaList sep2.FunctionSetAssignmentsList
	if err := c.WalkLink(ctx, sep2.Link{Href: edev.FunctionSetAssignmentsListLink.Href}, &fsaList); err != nil {
		t.Fatalf("walk FunctionSetAssignmentsListLink: %v", err)
	}
	return fsaList
}

// walkProgramListForFirstFSA follows the first FSA's DERProgramListLink
// and returns the resulting list of DERPrograms. The store-scope quirk
// described in the package-level doc-comment means this list carries
// all 7 programs for this EndDevice — that is the actual server
// semantic and the test asserts it directly.
func walkProgramListForFirstFSA(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	fsaList sep2.FunctionSetAssignmentsList,
) []sep2.DERProgram {
	t.Helper()

	fsa := fsaList.FunctionSetAssignments[0]
	var progList sep2.DERProgramList
	if err := c.WalkLink(ctx, sep2.Link{Href: fsa.DERProgramListLink.Href}, &progList); err != nil {
		t.Fatalf("walk DERProgramListLink %s: %v", fsa.DERProgramListLink.Href, err)
	}
	if progList.All != core013ProgramCount {
		t.Fatalf("DERProgramList.All = %d, want %d (store is scoped by EndDevice id only)",
			progList.All, core013ProgramCount)
	}
	if len(progList.DERProgram) != core013ProgramCount {
		t.Fatalf("len(DERProgramList.DERProgram) = %d, want %d",
			len(progList.DERProgram), core013ProgramCount)
	}
	return progList.DERProgram
}

// assertPrimacyChain asserts the 7 programs ascend strictly in primacy
// (0, 1, 2, ..., 6). Per §10.2, primacy is the priority key — a lower
// number is higher priority. The fixture deliberately ships an
// ascending sequence so the test surfaces any sort-order regression
// in the FSA → DERProgramList chain.
func assertPrimacyChain(t *testing.T, programs []sep2.DERProgram) {
	t.Helper()

	if len(programs) != core013ProgramCount {
		t.Fatalf("programs length = %d, want %d", len(programs), core013ProgramCount)
	}
	for i, prog := range programs {
		if prog.Primacy != uint8(i) {
			t.Errorf("DERProgram[%d].Primacy = %d, want %d", i, prog.Primacy, i)
		}
	}
	for i := 1; i < len(programs); i++ {
		if programs[i-1].Primacy >= programs[i].Primacy {
			t.Errorf("primacy chain not strictly ascending at i=%d: %d -> %d",
				i, programs[i-1].Primacy, programs[i].Primacy)
		}
	}
}

// walkAndAssertControl follows the program's DERControlListLink and
// asserts the lone DERControl matches the §6.6 procedure: Interval
// starts at T0 + N*60s, duration 30s, DERControlBase carries
// opModFixedPFInjectW with deterministic per-program displacement so
// each program is distinguishable on the wire.
func walkAndAssertControl(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	primacy int,
	prog sep2.DERProgram,
) {
	t.Helper()

	if prog.DERControlListLink == nil {
		t.Fatalf("DERProgram[primacy=%d].DERControlListLink is nil", primacy)
	}
	var ctrlList sep2.DERControlList
	if err := c.WalkLink(ctx, sep2.Link{Href: prog.DERControlListLink.Href}, &ctrlList); err != nil {
		t.Fatalf("walk DERControlListLink %s: %v", prog.DERControlListLink.Href, err)
	}
	if ctrlList.All != 1 || len(ctrlList.DERControl) != 1 {
		t.Fatalf("DERControlList primacy=%d: All=%d, len=%d; want 1/1",
			primacy, ctrlList.All, len(ctrlList.DERControl))
	}

	dc := ctrlList.DERControl[0]
	wantStart := core013T0 + int64(primacy)*60
	if dc.Interval == nil {
		t.Fatalf("DERControl primacy=%d: Interval is nil", primacy)
	}
	if dc.Interval.Start != wantStart {
		t.Errorf("DERControl primacy=%d: Interval.Start = %d, want %d",
			primacy, dc.Interval.Start, wantStart)
	}
	if dc.Interval.Duration != core013ControlDuration {
		t.Errorf("DERControl primacy=%d: Interval.Duration = %d, want %d",
			primacy, dc.Interval.Duration, core013ControlDuration)
	}
	if dc.DERControlBase == nil {
		t.Fatalf("DERControl primacy=%d: DERControlBase is nil", primacy)
	}
	pf := dc.DERControlBase.OpModFixedPFInjectW
	if pf == nil {
		t.Fatalf("DERControl primacy=%d: OpModFixedPFInjectW is nil", primacy)
	}
	wantDisp := uint16(900 + primacy*10) // deterministic per-program value
	if pf.Displacement != wantDisp {
		t.Errorf("DERControl primacy=%d: OpModFixedPFInjectW.Displacement = %d, want %d",
			primacy, pf.Displacement, wantDisp)
	}
	if !pf.Excitation {
		t.Errorf("DERControl primacy=%d: OpModFixedPFInjectW.Excitation = false, want true", primacy)
	}
}

// bootWithSevenProgramsAndControls loads derprogram-7.yaml into a
// fresh store set, seeds 7 DERControls (one per program) programmatically
// — covering the FixedPFInjectW field the loader does not carry today —
// and boots an in-process server backed by those stores. Returns the
// booted server; cleanup is handled by csiptest.BootServer.
func bootWithSevenProgramsAndControls(t *testing.T, extraOpts []csiptest.BootOption) *csiptest.BootedServer {
	t.Helper()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	fixture := filepath.Join("fixtures", "derprogram-7.yaml")
	if err := csiptest.Load(context.Background(), target, fixture); err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}

	// Seed one DERControl per program. The scope key matches the server's
	// composite key for DERControls under (edev, fsa, derp) — see
	// internal/server/router.go scopedListHandlerDeep. EndDevice id is "0"
	// across the fixture; FSA id and DERProgram id both equal primacy N.
	ctx := context.Background()
	for n := 0; n < core013ProgramCount; n++ {
		scope := fmt.Sprintf("0/%d/%d", n, n)
		ctrlID := fmt.Sprintf("ctrl-%d", n)
		ctrl := buildCORE013Control(n)
		if err := stores.DERControls.Create(ctx, scope, ctrlID, ctrl); err != nil {
			t.Fatalf("seed DERControl primacy=%d: %v", n, err)
		}
	}

	opts := append([]csiptest.BootOption{csiptest.WithStores(stores)}, extraOpts...)
	return csiptest.BootServer(t, opts...)
}

// buildCORE013Control constructs the §6.6 DERControl for primacy N:
// Interval starts at T0 + N*60s with duration 30s; the DERControlBase
// carries opModFixedPFInjectW with a deterministic per-program
// displacement so each program is distinguishable on the wire.
func buildCORE013Control(primacy int) sep2.DERControl {
	start := core013T0 + int64(primacy)*60
	duration := core013ControlDuration
	displacement := uint16(900 + primacy*10) // 900, 910, 920, ..., 960

	pf := sep2.FixedPowerFactor{
		Displacement: displacement,
		Excitation:   true,
		Multiplier:   0,
	}
	base := sep2.DERControlBase{OpModFixedPFInjectW: &pf}
	dc := sep2.DERControl{
		RandomizableEvent: sep2.RandomizableEvent{
			Event: sep2.Event{
				MRID:        fmt.Sprintf("CORE-013-CTRL-%d", primacy),
				Description: fmt.Sprintf("primacy-%d control", primacy),
				Interval: &sep2.DateTimeInterval{
					Duration: duration,
					Start:    start,
				},
			},
		},
		DERControlBase: &base,
	}
	dc.Href = fmt.Sprintf("/edev/0/fsa/%d/derp/%d/derc/%d", primacy, primacy, primacy)
	return dc
}
