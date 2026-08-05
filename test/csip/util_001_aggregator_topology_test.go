// CSIP V1.2 §9.1 — UTIL-001 Utility Server Startup + Group Assignment.
//
// UTIL-001 proves that an aggregator topology seeded into the CSIP
// server is discoverable end-to-end via the standard /dcap → /edev →
// FSA → DERProgram walk. The fixture is aggregator-topology.yaml
// (#139): 5 EndDevices (aggregator EDFI + 4 managed inverters
// EDA1/EDA2/EDB1/EDB2), a 4-level FSA chain per managed inverter
// (SY → FDx → SPxx → DEV), 4 DERPrograms per inverter at primacy
// 0..3 in priority-chain order.
//
// Procedure step → assertion mapping (per V1.2 §9.1):
//
//	Step 1: Server is up with the aggregator topology pre-loaded.
//	        ──► fixture load + BootServer; GET /dcap returns a
//	            non-empty EndDeviceListLink.
//	Step 2: Client GETs /edev and discovers 5 EndDevices.
//	        ──► WalkLink EndDeviceList; len == 5; the aggregator
//	            EDFI (id="0") + the 4 managed inverters (ids 1..4)
//	            are present in store-key order.
//	Step 3: For each managed inverter, walk its FSA chain.
//	        ──► For each of EDA1..EDB2, WalkLink FunctionSetAssignments-
//	            ListLink; assert len == 4 (SY/FDx/SPxx/DEV) and
//	            descriptions match the fixture per
//	            aggInverterDescriptions.
//	Step 4: For each managed inverter, walk one FSA's DERProgramList
//	        and assert the priority chain.
//	        ──► WalkLink the first FSA's DERProgramListLink; the
//	            server scopes DERPrograms by EndDevice (CORE-010
//	            documented this), so the response carries all 4
//	            programs for the inverter. Assert primacy values
//	            {0,1,2,3} are present exactly once.
//
// Out of scope (UTIL-002..004):
//   - PUTting DERCapability/DERSettings/DERStatus/DERAvailability for
//     each managed inverter (UTIL-002).
//   - Subscribing to per-inverter DERProgramList (UTIL-003).
//   - DERControl creation + Notification + Response POST (UTIL-004).
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// TestUTIL_001_AggregatorTopology implements CSIP V1.2 §9.1.
func TestUTIL_001_AggregatorTopology(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	// Step 1: /dcap advertises an EndDeviceListLink.
	dcap, err := client.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("step 1: GetDeviceCapability: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatalf("step 1: dcap.EndDeviceListLink missing/empty")
	}

	// Step 2: /edev returns 5 EndDevices.
	var edevList sep2.EndDeviceList
	if err := client.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href + "?l=255"}, &edevList); err != nil {
		t.Fatalf("step 2: WalkLink EndDeviceList: %v", err)
	}
	const wantEDevs = 5
	if len(edevList.EndDevice) != wantEDevs {
		t.Fatalf("step 2: len(EndDevice) = %d, want %d (aggregator EDFI + 4 managed inverters)",
			len(edevList.EndDevice), wantEDevs)
	}

	// Step 2 (cont.): the aggregator EDFI and the four managed inverters
	// must all be present. Store ordering is by store-key (ID), which
	// matches our fixture id ordering "0".."4".
	wantIDs := []string{aggEDFI, aggEDA1, aggEDA2, aggEDB1, aggEDB2}
	for i, want := range wantIDs {
		got := edevList.EndDevice[i].Href
		wantHref := "/edev/" + want
		if got != wantHref {
			t.Errorf("step 2: EndDevice[%d].Href = %q, want %q", i, got, wantHref)
		}
	}

	// Step 3: for each managed inverter walk the FSA chain and assert
	// length and descriptions in priority-chain order.
	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()
			fsaHref := "/edev/" + edevID + "/fsa?l=255"
			var fsaList sep2.FunctionSetAssignmentsList
			if err := client.WalkLink(ctx, sep2.Link{Href: fsaHref}, &fsaList); err != nil {
				t.Fatalf("step 3: WalkLink %s: %v", fsaHref, err)
			}
			if int(fsaList.All) != aggFSACount {
				t.Errorf("step 3: FSAList.All = %d, want %d", fsaList.All, aggFSACount)
			}
			if len(fsaList.FunctionSetAssignments) != aggFSACount {
				t.Fatalf("step 3: len(FSAs) = %d, want %d",
					len(fsaList.FunctionSetAssignments), aggFSACount)
			}

			wantDescs, ok := aggInverterDescriptions[edevID]
			if !ok {
				t.Fatalf("step 3: missing descriptions map entry for edev %q", edevID)
			}
			for i, fsa := range fsaList.FunctionSetAssignments {
				if fsa.Description != wantDescs[i] {
					t.Errorf("step 3: edev=%q FSA[%d].Description = %q, want %q",
						edevID, i, fsa.Description, wantDescs[i])
				}
				if fsa.DERProgramListLink == nil || fsa.DERProgramListLink.Href == "" {
					t.Errorf("step 3: edev=%q FSA[%d] missing DERProgramListLink",
						edevID, i)
				}
			}

			// Step 4: walk the first FSA's DERProgramList; server scopes
			// DERPrograms by EndDevice (see CORE-010 doc comment), so
			// the response carries all 4 programs for this inverter.
			progHref := fsaList.FunctionSetAssignments[0].DERProgramListLink.Href + "?l=255"
			var progList sep2.DERProgramList
			if err := client.WalkLink(ctx, sep2.Link{Href: progHref}, &progList); err != nil {
				t.Fatalf("step 4: WalkLink %s: %v", progHref, err)
			}
			if len(progList.DERProgram) != aggDERPCount {
				t.Fatalf("step 4: edev=%q DERProgramList len = %d, want %d",
					edevID, len(progList.DERProgram), aggDERPCount)
			}
			primacySeen := map[uint8]bool{}
			for _, prog := range progList.DERProgram {
				primacySeen[prog.Primacy] = true
			}
			for level := uint8(0); level < aggDERPCount; level++ {
				if !primacySeen[level] {
					t.Errorf("step 4: edev=%q missing primacy level %d (saw %v)",
						edevID, level, primacySeen)
				}
			}
		})
	}
}
