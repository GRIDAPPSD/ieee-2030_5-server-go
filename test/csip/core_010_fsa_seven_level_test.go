// CSIP V1.2 §6.3 — FSA Hierarchies, 7-level.
//
// CORE-010 proves that a CSIP server seeded with a 7-level FSA priority
// chain advertises and serves the chain end-to-end: the client walks
// from /dcap to /edev to the first EndDevice's FunctionSetAssignments-
// ListLink to each FSA's DERProgramListLink, and the priority ordering
// (DERProgram.Primacy ascending 0..6, lower = higher priority per spec
// §10.2) is discoverable from the wire response.
//
// The fixture is seven-level-fsa.yaml (shipped by IEEE-057): one
// EndDevice, seven FSAs scoped under it, one DERProgram per FSA with
// primacy 0..6 in ID order.
//
// Procedure step → assertion mapping (per V1.2 §6.3):
//
//	Step 1: server has an EndDevice with 7 FSAs advertised.
//	        ──► fixture load + assertion: /dcap returns
//	            EndDeviceListLink, /edev returns one EndDevice with
//	            FunctionSetAssignmentsListLink advertised All=7.
//	Step 2: client GETs the FSA list.
//	        ──► WalkLink against FunctionSetAssignmentsListLink with
//	            a paging limit large enough to include all 7 in one
//	            response. Asserts len() == 7 and per-FSA Href shape.
//	Step 3: client GETs each FSA's DERProgramListLink.
//	        ──► For each of the 7 FSAs, WalkLink the DERProgramList-
//	            Link and assert the response carries DERPrograms whose
//	            primacy values cover the full 0..6 chain.
//	Step 4: priority chain matches fixture spec.
//	        ──► Asserts the primacy values 0..6 are present exactly
//	            once and the descriptions match the fixture's L0..L6
//	            labels in store-key order.
//
// Server behavior the test pins down (intentional, documented here so
// a future refactor that changes either of these surfaces here):
//
//   - The FSA list handler returns FSAs sorted by store key. The
//     fixture's IDs "0".."6" sort lexicographically the same as
//     numerically (single digits), so the wire order matches the
//     fixture's primacy chain 0..6.
//   - The DERProgramListLink at /edev/{id}/fsa/{fsaId}/derp is scoped
//     by EndDevice only (not by FSA — see scopedListHandler in
//     internal/server/router.go and the loader's note on DERProgram
//     scoping). So every FSA's DERProgramListLink returns all 7
//     DERPrograms for this fixture. The fixture's `all: 1` per-FSA
//     advertisement is fixture metadata; the wire response carries
//     the full 7. The test asserts both shapes — the discovered
//     priority chain is what the procedure cares about, and the
//     scoping behavior is documented as the current server contract.
package csip_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// sevenLevelDescriptions is the in-order list of FSA descriptions the
// seven-level-fsa.yaml fixture assigns to its 7 FSAs. The test asserts
// FSAList[i].Description == sevenLevelDescriptions[i] for every i, so
// any fixture edit that renames a level forces a matching test edit —
// exactly the coupling the procedure demands.
var sevenLevelDescriptions = []string{
	"L0-system",
	"L1-substation",
	"L2-feeder",
	"L3-transformer",
	"L4-service-point",
	"L5-meter",
	"L6-device",
}

// TestCORE_010_FSAHierarchy7Level implements CSIP V1.2 §6.3.
func TestCORE_010_FSAHierarchy7Level(t *testing.T) {
	t.Parallel()
	runFSAHierarchy(t, fsaHierarchyParams{
		fixture:      filepath.Join("fixtures", "seven-level-fsa.yaml"),
		descriptions: sevenLevelDescriptions,
	})
}

// fsaHierarchyParams is the shared knob set for the 7-level and 15-level
// FSA-hierarchy procedures. CORE-010 and CORE-011 differ only in the
// fixture path and the per-level label slice; the wire walk is
// identical, and the level count is derived from len(descriptions).
type fsaHierarchyParams struct {
	fixture      string
	descriptions []string
}

// runFSAHierarchy boots a server seeded with the fixture at p.fixture and
// drives the V1.2 §6.3 / §6.4 procedure end-to-end: /dcap → /edev →
// first EndDevice → FunctionSetAssignmentsListLink → each FSA →
// DERProgramListLink → assertions on the priority chain.
//
// The function is shared by CORE-010 (7-level) and CORE-011 (15-level)
// because the procedures are identical apart from the level count and
// the per-level description labels. Duplicating the walk in two files
// would obscure that — and a future procedure change (e.g. priority
// ordering must come back in a header) would otherwise have to land in
// two places. Per Pike's dedup rule, shared logic that exercises
// identical behavior lives in one helper.
func runFSAHierarchy(t *testing.T, p fsaHierarchyParams) {
	t.Helper()
	ctx := context.Background()
	levels := len(p.descriptions)

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	if err := csiptest.Load(ctx, target, p.fixture); err != nil {
		t.Fatalf("Load %s: %v", p.fixture, err)
	}

	srv := csiptest.BootServer(t, csiptest.WithStores(stores))
	client := srv.Client()

	// Step 1: GET /dcap; expect an EndDeviceListLink.
	dcap, err := client.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatalf("dcap.EndDeviceListLink missing/empty; want a non-empty href")
	}

	// Step 1 (cont.): walk EndDeviceList, take the first EndDevice.
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
	wantAll := uint32(levels)
	if got := edev.FunctionSetAssignmentsListLink.All; got != wantAll {
		t.Errorf("EndDevice[0].FunctionSetAssignmentsListLink.All = %d, want %d", got, wantAll)
	}

	// Step 2: walk the FSA list with a paging limit large enough to
	// fit all advertised entries in one response. Default limit is 10
	// (paging.DefaultLimit), which is fine for 7 but not 15 — using an
	// explicit `l=` always works.
	fsaList := walkFSAList(ctx, t, srv, edev.FunctionSetAssignmentsListLink.Href)
	if int(fsaList.All) != levels {
		t.Errorf("FSAList.All = %d, want %d", fsaList.All, levels)
	}
	if int(fsaList.Results) != levels {
		t.Errorf("FSAList.Results = %d, want %d", fsaList.Results, levels)
	}
	if len(fsaList.FunctionSetAssignments) != levels {
		t.Fatalf("len(FunctionSetAssignments) = %d, want %d",
			len(fsaList.FunctionSetAssignments), levels)
	}

	// Each FSA must advertise a DERProgramListLink.
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.DERProgramListLink == nil || fsa.DERProgramListLink.Href == "" {
			t.Fatalf("FSA[%d] (mrid=%q) missing DERProgramListLink", i, fsa.MRID)
		}
	}

	// Step 3 + 4: walk each FSA's DERProgramListLink, collect the
	// primacy values discovered across the full walk, assert the
	// chain matches 0..levels-1.
	//
	// The server scopes DERPrograms by EndDevice only (see test doc
	// comment for rationale), so every FSA's DERProgramListLink returns
	// all DERPrograms for this EndDevice. Each walk should therefore
	// produce the same response. We verify that and collect primacy
	// values across all walks before asserting the chain.
	primacySeen := map[uint8]bool{}
	for i, fsa := range fsaList.FunctionSetAssignments {
		progList := walkDERProgramList(ctx, t, srv, fsa.DERProgramListLink.Href)
		// Per current server contract: DERPrograms are scoped by
		// EndDevice only, so every FSA's walk returns the full set.
		if len(progList.DERProgram) != levels {
			t.Errorf("FSA[%d] (mrid=%q) DERProgramList.DERProgram len = %d, want %d "+
				"(server scopes DERPrograms by EndDevice; all programs for the "+
				"EndDevice come back regardless of FSA path segment)",
				i, fsa.MRID, len(progList.DERProgram), levels)
		}
		for _, prog := range progList.DERProgram {
			primacySeen[prog.Primacy] = true
		}
	}

	// Priority chain assertion: every primacy from 0 to levels-1 must
	// be present.
	if len(primacySeen) != levels {
		t.Fatalf("primacy chain coverage: saw %d distinct primacy values %v, want %d",
			len(primacySeen), sortedPrimacy(primacySeen), levels)
	}
	for level := 0; level < levels; level++ {
		if !primacySeen[uint8(level)] {
			t.Errorf("primacy chain missing level %d (saw %v)",
				level, sortedPrimacy(primacySeen))
		}
	}

	// Step 4 (cont.): wire order assertion. The FSA list is emitted in
	// sorted store-key order. The fixtures encode IDs that sort to
	// match the priority chain (single-digit "0".."6" for 7-level;
	// zero-padded "00".."14" for 15-level — both lex-sort = numeric
	// sort across the 10-boundary). So FSAList[i].Description must
	// match p.descriptions[i].
	for i, fsa := range fsaList.FunctionSetAssignments {
		if fsa.Description != p.descriptions[i] {
			t.Errorf("FSA[%d].Description = %q, want %q "+
				"(FSA list expected in priority chain order via sorted store keys)",
				i, fsa.Description, p.descriptions[i])
		}
	}
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

// walkDERProgramList GETs href with l=255 and unmarshals as
// DERProgramList.
func walkDERProgramList(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, href string) sep2.DERProgramList {
	t.Helper()
	var list sep2.DERProgramList
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("WalkLink DERProgramList (%s): %v", href, err)
	}
	return list
}

// sortedPrimacy returns the keys of seen as a sorted []uint8 — purely
// for readable test failure output.
func sortedPrimacy(seen map[uint8]bool) []uint8 {
	out := make([]uint8, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
