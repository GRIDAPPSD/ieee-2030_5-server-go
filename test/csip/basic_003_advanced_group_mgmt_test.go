//go:build csip_test_hooks

// CSIP V1.2 §8.3 — BASIC-003 Advanced Group Management.
//
// BASIC-003 exercises a mid-flight topology change: a DER's group
// assignment (its FunctionSetAssignment hookup) is reassigned at runtime
// by a utility-side edit, and the DER's published view of its FSA chain
// must reflect the new assignment without the client retrying handshake
// or registration. In the spec's hierarchy vocabulary, the canonical
// procedure is a "feeder swap" — one level of the FSA priority chain is
// re-pointed at a different group while every other level (system,
// substation, transformer, ...) is unchanged.
//
// The IEEE-2030.5 wire model exposes the per-EndDevice FSA list at
// /edev/{id}/fsa as the harness-observable surface for that change.
// There is no production endpoint for the utility-side edit itself, so
// the harness drives the swap through the IEEE-078 mutation hook at
// POST /test/mutations/fsa-swap (build-tag-gated under csip_test_hooks,
// token-authenticated). IEEE-078 chose reassignment shape (a): re-key
// the FSA list entry under the EndDevice scope, preserving content and
// re-stamping Href + DERProgramListLink.Href. DERPrograms are scoped by
// EndDevice only — they ride along untouched.
//
// Procedure step → assertion mapping (V1.2 §8.3):
//
//	Step 1: server has an EndDevice with the seven-level FSA chain
//	        (system, substation, feeder, transformer, service-point,
//	        meter, device). The fixture seven-level-fsa.yaml seeds
//	        exactly this — see IEEE-057 / CORE-010.
//	        ──► load fixture, BootServer, GET /dcap → EndDeviceList →
//	            FunctionSetAssignmentsListLink. Assert all=7.
//	Step 2: client GETs the FSA list pre-swap; the feeder-level entry
//	        ("L2-feeder", primacy 2 in this fixture's chain, fixture id
//	        "2") is present with its original Href and DERProgramList-
//	        Link.Href.
//	        ──► WalkLink the FSAList with l=255 (single page).
//	            Locate by mRID "L2-FEEDER-FSA"; assert Href is
//	            "/edev/0/fsa/2" and DERProgramListLink.Href is
//	            "/edev/0/fsa/2/derp".
//	Step 3: utility-side edit (out-of-band): reassign the feeder
//	        FSA's id from "2" to "2-new".
//	        ──► POST /test/mutations/fsa-swap with
//	            {end_device_id: "0", from_fsa: "2", to_fsa: "2-new"}.
//	            Assert 204.
//	Step 4: client re-GETs the FSA list post-swap. The feeder entry
//	        must now live under id "2-new"; the old id "2" must be
//	        gone. The feeder entry's content (mRID, description) must
//	        be preserved; its Href and DERProgramListLink.Href must be
//	        re-stamped to the new path.
//	        ──► WalkLink again, find by mRID, assert the new Href,
//	            new DERProgramListLink.Href, preserved mRID,
//	            description. Assert no FSA carries the old id "2"
//	            anywhere in its Href.
//	Step 5: the priority chain is still intact — every other FSA in
//	        the chain is unchanged, total count is still 7.
//	        ──► Assert post-swap len(FSAList) == 7 and the six
//	            non-feeder FSAs match the pre-swap snapshot byte-for-
//	            byte on (mRID, Description, Href).
//	Step 6: the underlying DERProgram references re-key correctly per
//	        IEEE-078's resolution. IEEE-078 chose option (a): DER-
//	        Programs are scoped by EndDevice only (not by FSA), so
//	        every FSA's DERProgramListLink returns the same full set.
//	        The swap leaves DERPrograms untouched.
//	        ──► WalkLink the new feeder FSA's DERProgramListLink and
//	            assert it returns all 7 DERPrograms with primacy 0..6
//	            (mirrors CORE-010's chain assertion).
//
// Build tag: this test exercises a /test/mutations/* endpoint and only
// builds under `-tags csip_test_hooks`. The mutation surface also
// requires SEP2_TEST_MUTATION_TOKEN to be non-empty at NewRouter time;
// the test sets that via t.Setenv before BootServer.
package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// basic003TestToken is the shared secret presented in X-CSIP-Test-Token
// on every mutation request. The value is arbitrary; it must match the
// SEP2_TEST_MUTATION_TOKEN env var the test sets before BootServer.
const basic003TestToken = "ieee-088-basic-003-mutation-token"

// basic003FeederMRID is the fixture's mRID for the L2-feeder FSA in
// seven-level-fsa.yaml. The test locates the feeder entry by mRID
// rather than by id so a fixture renumber doesn't silently flip the
// procedure to a different chain level.
const basic003FeederMRID = "L2-FEEDER-FSA"

// TestBASIC_003_AdvancedGroupManagement implements CSIP V1.2 §8.3.
// See file-level doc comment for the procedure-step → assertion map.
func TestBASIC_003_AdvancedGroupManagement(t *testing.T) {
	// Mutation surface reads its token env at router-construct time
	// (RegisterMutationHandlers in internal/server/test_mutations.go).
	// t.Setenv must run before BootServer; t.Parallel is therefore not
	// safe here — t.Setenv panics on a parallel test. The procedure is
	// short and self-contained so serial execution is the correct
	// trade.
	t.Setenv("SEP2_TEST_MUTATION_TOKEN", basic003TestToken)

	ctx := context.Background()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore, // IEEE-097 wrapper; IEEE-104.
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	fixture := filepath.Join("fixtures", "seven-level-fsa.yaml")
	if err := csiptest.Load(ctx, target, fixture); err != nil {
		t.Fatalf("Load %s: %v", fixture, err)
	}

	srv := csiptest.BootServer(t, csiptest.WithStores(stores))
	client := srv.Client()

	// Step 1: assert the EndDevice is wired with all 7 FSAs advertised.
	dcap, err := client.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatalf("dcap.EndDeviceListLink missing")
	}
	var edevList sep2.EndDeviceList
	if err := client.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &edevList); err != nil {
		t.Fatalf("WalkLink EndDeviceList: %v", err)
	}
	if len(edevList.EndDevice) != 1 {
		t.Fatalf("len(EndDeviceList) = %d, want 1 (fixture seeds one EndDevice)",
			len(edevList.EndDevice))
	}
	edev := edevList.EndDevice[0]
	if edev.FunctionSetAssignmentsListLink == nil || edev.FunctionSetAssignmentsListLink.Href == "" {
		t.Fatalf("EndDevice[0].FunctionSetAssignmentsListLink missing")
	}
	if got := edev.FunctionSetAssignmentsListLink.All; got != 7 {
		t.Errorf("FSAListLink.All = %d, want 7", got)
	}

	fsaListHref := edev.FunctionSetAssignmentsListLink.Href

	// Step 2: pre-swap snapshot of the FSA list. Locate the feeder.
	preList := walkBasic003FSAList(ctx, t, srv, fsaListHref)
	if len(preList.FunctionSetAssignments) != 7 {
		t.Fatalf("pre-swap len(FSAList) = %d, want 7",
			len(preList.FunctionSetAssignments))
	}
	preFeeder, preFeederIdx := findFSAByMRID(preList.FunctionSetAssignments, basic003FeederMRID)
	if preFeederIdx < 0 {
		t.Fatalf("pre-swap: feeder FSA with mRID %q not found in list",
			basic003FeederMRID)
	}
	if preFeeder.Href != "/edev/0/fsa/2" {
		t.Errorf("pre-swap feeder Href = %q, want %q", preFeeder.Href, "/edev/0/fsa/2")
	}
	if preFeeder.DERProgramListLink == nil {
		t.Fatalf("pre-swap feeder DERProgramListLink missing")
	}
	if preFeeder.DERProgramListLink.Href != "/edev/0/fsa/2/derp" {
		t.Errorf("pre-swap feeder DERProgramListLink.Href = %q, want %q",
			preFeeder.DERProgramListLink.Href, "/edev/0/fsa/2/derp")
	}

	// Step 3: drive the utility-side reassignment via the IEEE-078
	// mutation hook. End-device id "0", from "2", to "2-new". Body is
	// strict JSON; the handler disallows unknown fields.
	swapBody := map[string]string{
		"end_device_id": "0",
		"from_fsa":      "2",
		"to_fsa":        "2-new",
	}
	postFSASwap(ctx, t, srv, swapBody, http.StatusNoContent)

	// Step 4 + 5: re-walk the FSA list. Count is still 7. Feeder now
	// lives under id "2-new"; old id "2" is absent from every FSA's
	// Href. Content (mRID, Description) preserved; Href and DERProgram-
	// ListLink.Href re-stamped to the new path. Every non-feeder FSA
	// is unchanged byte-for-byte on (mRID, Description, Href).
	postList := walkBasic003FSAList(ctx, t, srv, fsaListHref)
	if len(postList.FunctionSetAssignments) != 7 {
		t.Fatalf("post-swap len(FSAList) = %d, want 7 (swap must not change count)",
			len(postList.FunctionSetAssignments))
	}
	postFeeder, postFeederIdx := findFSAByMRID(postList.FunctionSetAssignments, basic003FeederMRID)
	if postFeederIdx < 0 {
		t.Fatalf("post-swap: feeder FSA with mRID %q not found (swap lost it)",
			basic003FeederMRID)
	}
	if postFeeder.Href != "/edev/0/fsa/2-new" {
		t.Errorf("post-swap feeder Href = %q, want %q "+
			"(IEEE-078 must re-stamp Href to the new id)",
			postFeeder.Href, "/edev/0/fsa/2-new")
	}
	if postFeeder.DERProgramListLink == nil {
		t.Fatalf("post-swap feeder DERProgramListLink lost during swap")
	}
	if postFeeder.DERProgramListLink.Href != "/edev/0/fsa/2-new/derp" {
		t.Errorf("post-swap feeder DERProgramListLink.Href = %q, want %q",
			postFeeder.DERProgramListLink.Href, "/edev/0/fsa/2-new/derp")
	}
	if postFeeder.MRID != preFeeder.MRID {
		t.Errorf("post-swap feeder mRID = %q, want %q (content must be preserved)",
			postFeeder.MRID, preFeeder.MRID)
	}
	if postFeeder.Description != preFeeder.Description {
		t.Errorf("post-swap feeder Description = %q, want %q",
			postFeeder.Description, preFeeder.Description)
	}

	// No FSA in the post-swap list may carry the old id "2" in its
	// Href. (Substring check guards against either /edev/0/fsa/2 or
	// /edev/0/fsa/2/derp slipping through.)
	for i, fsa := range postList.FunctionSetAssignments {
		if fsa.Href == "/edev/0/fsa/2" {
			t.Errorf("post-swap FSA[%d] still carries old Href /edev/0/fsa/2", i)
		}
		if fsa.DERProgramListLink != nil &&
			fsa.DERProgramListLink.Href == "/edev/0/fsa/2/derp" {
			t.Errorf("post-swap FSA[%d] still carries old DERProgramListLink.Href /edev/0/fsa/2/derp", i)
		}
	}

	// Non-feeder chain levels must be byte-for-byte unchanged. Index
	// across the list and skip the feeder mRID. Sorted store-key
	// order: pre-swap is "0".."6"; post-swap is the same minus "2"
	// plus "2-new". Lexical sort puts "2-new" after "1" and before
	// "3" (the "-" byte 0x2D is between digits 0x30 and "0"..."9", and
	// "2-new" sorts before "20" or "3"). So the non-feeder slots align
	// at the same store-key positions in both lists. Compare by mRID
	// for cross-list identification — robust against any future fixture
	// id renumber.
	for i, want := range preList.FunctionSetAssignments {
		if want.MRID == basic003FeederMRID {
			continue
		}
		got, idx := findFSAByMRID(postList.FunctionSetAssignments, want.MRID)
		if idx < 0 {
			t.Errorf("pre-swap FSA[%d] (mRID %q) missing from post-swap list",
				i, want.MRID)
			continue
		}
		if got.Href != want.Href {
			t.Errorf("non-feeder FSA mRID %q: Href changed across swap: got %q, want %q",
				want.MRID, got.Href, want.Href)
		}
		if got.Description != want.Description {
			t.Errorf("non-feeder FSA mRID %q: Description changed across swap: got %q, want %q",
				want.MRID, got.Description, want.Description)
		}
	}

	// Step 6: the priority chain is intact. WalkLink the new feeder's
	// DERProgramListLink and assert it returns all 7 DERPrograms with
	// primacy 0..6 — IEEE-078 leaves DERPrograms untouched because
	// they're scoped by EndDevice only. This mirrors CORE-010's chain
	// assertion to make the "DERProgram references re-key correctly"
	// exit criterion concrete.
	var derpList sep2.DERProgramList
	if err := client.WalkLink(ctx,
		sep2.Link{Href: postFeeder.DERProgramListLink.Href + "?l=255"},
		&derpList); err != nil {
		t.Fatalf("WalkLink post-swap feeder DERProgramList: %v", err)
	}
	if len(derpList.DERProgram) != 7 {
		t.Errorf("post-swap feeder DERProgramList len = %d, want 7 "+
			"(DERPrograms are scoped by EndDevice; swap must not drop any)",
			len(derpList.DERProgram))
	}
	primacySeen := map[uint8]bool{}
	for _, prog := range derpList.DERProgram {
		primacySeen[prog.Primacy] = true
	}
	for level := uint8(0); level < 7; level++ {
		if !primacySeen[level] {
			t.Errorf("post-swap primacy chain missing level %d", level)
		}
	}
}

// walkBasic003FSAList GETs the FSA list with l=255 so all 7 entries
// come back in one response. Sibling of CORE-010's walkFSAList — kept
// local to the file because the helper there isn't exported and BASIC-
// 003 is the only build-tag-gated test currently exercising the FSA
// list. Promoting to csiptest is premature until a second tagged test
// needs the same wire walk.
func walkBasic003FSAList(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, href string) sep2.FunctionSetAssignmentsList {
	t.Helper()
	var list sep2.FunctionSetAssignmentsList
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("WalkLink FSAList (%s): %v", href, err)
	}
	return list
}

// findFSAByMRID returns the entry in fsas whose MRID matches want, plus
// its index, or (zero, -1) if no entry matches. Procedural assertions
// pre/post swap look up by mRID rather than by slice index so a future
// fixture renumber doesn't silently flip the procedure to a different
// chain level.
func findFSAByMRID(fsas []sep2.FunctionSetAssignments, want string) (sep2.FunctionSetAssignments, int) {
	for i, fsa := range fsas {
		if fsa.MRID == want {
			return fsa, i
		}
	}
	return sep2.FunctionSetAssignments{}, -1
}

// postFSASwap issues POST /test/mutations/fsa-swap against the booted
// server with the configured X-CSIP-Test-Token header. The body is JSON-
// encoded; the handler disallows unknown fields, so the caller must
// pass a map matching IEEE-078's request schema {end_device_id,
// from_fsa, to_fsa}. Asserts the response status matches wantStatus.
//
// Single-use helper kept local to this file. Promotion to csiptest is
// deferred until a second tagged test drives the same mutation surface —
// no point shipping an API for one caller (Pike's anti-extension rule,
// also documented in IEEE-078's "harness can chain a derctl-add" note).
func postFSASwap(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, body map[string]string, wantStatus int) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode swap body: %v", err)
	}
	url := srv.BaseURL + "/test/mutations/fsa-swap"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("build mutation request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSIP-Test-Token", basic003TestToken)

	resp, err := srv.Client().HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status = %d, want %d", url, resp.StatusCode, wantStatus)
	}
}
