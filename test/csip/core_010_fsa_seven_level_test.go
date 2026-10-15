// CSIP V1.2 section 6.3 - CORE-010 Function Set Assignments.
//
// CORE-010 Setup steps 4 to 7 build one DERProgram per group of the
// seven-level hierarchy (Figure 1), one DERProgramList holding all seven,
// and one FunctionSetAssignments linking that list. Procedure step 9 walks
// FunctionSetAssignmentsList -> FunctionSetAssignments ->
// DERProgramListLink -> DERProgramList -> DERPrograms, and the pass
// criteria require 200s on all of them and on all seven DERPrograms. The
// fixture is core-010-fsa.yaml.
//
// Procedure step -> assertion mapping:
//
//	Steps 4-5: /dcap -> EndDeviceListLink -> /edev, first EndDevice.
//	Step 8:    its FunctionSetAssignmentsListLink advertises all=1.
//	Step 9:    the one FSA's DERProgramList holds all seven DERPrograms in
//	           Table 50 order (primacy 0..6), each under that FSA.
//	Step 10:   each DERProgram's own href is GET and serves that program
//	           (assertFSAProgramList).
//
// runFSAHierarchy below is CORE-011's fifteen-FSA walk.

package csip_test

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// core010Programs is the DERProgramList CORE-010's one FSA holds, in Table
// 50 order: primacy ascending, then mRID descending.
var core010Programs = []wantProgram{
	{mrid: "L0-SYSTEM-DERP", primacy: 0},
	{mrid: "L1-SUBSTATION-DERP", primacy: 1},
	{mrid: "L2-FEEDER-DERP", primacy: 2},
	{mrid: "L3-TRANSFORMER-DERP", primacy: 3},
	{mrid: "L4-SERVICE-POINT-DERP", primacy: 4},
	{mrid: "L5-METER-DERP", primacy: 5},
	{mrid: "L6-DEVICE-DERP", primacy: 6},
}

// TestCORE_010_FSAHierarchy7Level implements CSIP V1.2 section 6.3.
func TestCORE_010_FSAHierarchy7Level(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, edev := bootFSAFixture(t, filepath.Join("fixtures", "core-010-fsa.yaml"))
	if got := edev.FunctionSetAssignmentsListLink.All; got != 1 {
		t.Errorf("EndDevice[0].FunctionSetAssignmentsListLink.All = %d, want 1", got)
	}
	fsaList := walkFSAList(ctx, t, srv, edev.FunctionSetAssignmentsListLink.Href)
	if fsaList.All != 1 || len(fsaList.FunctionSetAssignments) != 1 {
		t.Fatalf("FSAList all=%d len=%d, want the one FSA of Setup step 6", fsaList.All, len(fsaList.FunctionSetAssignments))
	}
	assertFSAProgramList(t, ctx, srv.Client(), fsaList.FunctionSetAssignments[0], core010Programs)
}

// fsaHierarchyParams names a one-program-per-FSA hierarchy fixture and its
// per-level FSA descriptions; the level count is len(descriptions).
type fsaHierarchyParams struct {
	fixture      string
	descriptions []string
}

// runFSAHierarchy drives CORE-011 (section 6.4): fifteen FSAs, each linking a
// DERProgramList that holds that level's one DERProgram. Step 7 sorts the
// FSAs by mRID for priority, step 8 GETs a DERProgramList, and step 9 picks
// the highest priority program; so each FSA lists only its own program, and
// across the walk the primacy chain 0..levels-1 is reached exactly once.
func runFSAHierarchy(t *testing.T, p fsaHierarchyParams) {
	t.Helper()
	ctx := context.Background()
	levels := len(p.descriptions)

	srv, edev := bootFSAFixture(t, p.fixture)
	if got := edev.FunctionSetAssignmentsListLink.All; got != uint32(levels) {
		t.Errorf("EndDevice[0].FunctionSetAssignmentsListLink.All = %d, want %d", got, levels)
	}

	// l=255 because the default page of 10 does not fit fifteen.
	fsaList := walkFSAList(ctx, t, srv, edev.FunctionSetAssignmentsListLink.Href)
	if int(fsaList.All) != levels || int(fsaList.Results) != levels || len(fsaList.FunctionSetAssignments) != levels {
		t.Fatalf("FSAList all=%d results=%d len=%d, want %d",
			fsaList.All, fsaList.Results, len(fsaList.FunctionSetAssignments), levels)
	}

	// FSA ids are zero-padded, so store-key order is the priority chain and
	// FSA i holds the level-i program, named after its FSA.
	primacySeen := map[uint8]bool{}
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.Description != p.descriptions[i] {
			t.Errorf("FSA[%d].Description = %q, want %q", i, fsa.Description, p.descriptions[i])
		}
		level, ok := strings.CutSuffix(fsa.MRID, "-FSA")
		if !ok {
			t.Fatalf("FSA[%d].MRID = %q, want a level name ending -FSA", i, fsa.MRID)
		}
		progs := assertFSAProgramList(t, ctx, srv.Client(), fsa, []wantProgram{{mrid: level + "-DERP", primacy: uint8(i)}})
		primacySeen[progs[0].Primacy] = true
	}
	if len(primacySeen) != levels {
		t.Errorf("primacy chain: saw %v across the walk, want 0..%d once each", sortedPrimacy(primacySeen), levels-1)
	}
}

// bootFSAFixture boots a server seeded with fixture and walks /dcap ->
// EndDeviceList to the first EndDevice, which must link its FSA list.
func bootFSAFixture(t *testing.T, fixture string) (*csiptest.BootedServer, sep2.EndDevice) {
	t.Helper()
	ctx := context.Background()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	owner := csiptest.NewDeviceIdentity(t, "CORE-FSA-DEVICE")
	if err := csiptest.Load(ctx, target, fixture, csiptest.Bind(fixtureEndDeviceID, owner)); err != nil {
		t.Fatalf("Load %s: %v", fixture, err)
	}
	srv := csiptest.BootServer(t, csiptest.WithStores(stores), csiptest.WithDeviceIdentity(owner))
	client := srv.Client()

	dcap, err := client.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatalf("dcap.EndDeviceListLink missing/empty; want a non-empty href")
	}
	var edevList sep2.EndDeviceList
	if err := client.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &edevList); err != nil {
		t.Fatalf("WalkLink EndDeviceList (%s): %v", dcap.EndDeviceListLink.Href, err)
	}
	if len(edevList.EndDevice) == 0 {
		t.Fatalf("EndDeviceList empty; fixture should seed at least one EndDevice")
	}
	edev := edevList.EndDevice[0]
	if edev.FunctionSetAssignmentsListLink == nil || edev.FunctionSetAssignmentsListLink.Href == "" {
		t.Fatalf("EndDevice[0].FunctionSetAssignmentsListLink missing/empty")
	}
	return srv, edev
}

// walkFSAList GETs href with a paging limit large enough to include the
// full fixture (l=255, paging.MaxLimit) and unmarshals as
// FunctionSetAssignmentsList. Returning a separate helper keeps the
// procedure walk readable.
func walkFSAList(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, href string) sep2.FunctionSetAssignmentsList {
	t.Helper()
	var list sep2.FunctionSetAssignmentsList
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("WalkLink FSAList (%s): %v", href, err)
	}
	return list
}

// sortedPrimacy returns the keys of seen as a sorted []uint8 - purely
// for readable test failure output.
func sortedPrimacy(seen map[uint8]bool) []uint8 {
	out := make([]uint8, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
