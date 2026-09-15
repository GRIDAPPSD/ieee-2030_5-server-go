// Shared helpers for the BASIC-004..012 per-mode procedure tests.
//
// CSIP V1.2 Section 8.4-8.12 share a near-identical procedure shape:
//
//	Step 1: server has a DERProgram with one DERControl (or for Section 8.7,
//	        a DefaultDERControl) carrying the per-mode opMod*
//	        parameter, plus the global /dc store carries the per-mode
//	        DERCurve when the mode is curve-based.
//	Step 2: client walks /dcap -> /edev -> /fsa -> DERProgram -> DERControl
//	        (or DefaultDERControl for Section 8.7).
//	Step 3: client asserts the seeded opMod* survives the wire
//	        roundtrip; for curve-based modes, walks /dc and asserts
//	        the curve renders.
//
// #135 wires server-side wire-fidelity tests against this shape.
// The procedure's "act (advance time / FSA-swap / drive state)" and
// "assert Response POST back" legs are NOT in scope for these
// server-side tests - server just renders the seeded fixture; Response
// POST is exercised by #112/#116, and time/state are exercised by
// #92/#123 in their own tests.
//
// Five of the procedures (BASIC-004, 005, 007, 011, 012) reference
// opMod* / set* fields that do NOT exist in pkg/sep2.DERControlBase
// today. Per #135 scope ("Do not change public API of
// internal/handler or pkg/sep2"), those tests run the procedure walk
// up to the field assertion and t.Skip with a // Pinned by #140
// - implementation gap comment referencing the follow-up ticket. The
// DERCurveList walk leg still runs for curve-based modes - DERCurve
// IS in pkg/sep2 today and renders correctly via /dc.
package csip_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basicModeWalk drives the chained-walk procedure shape shared by
// BASIC-004..012: load the named fixture, walk /dcap -> /edev -> /fsa ->
// DERProgram, then invoke the per-mode assert callback with the
// resulting program, the walked DERControlList, and the booted server's
// client so the callback can issue mode-specific follow-up GETs.
//
// Boots under both GCM and CCM cipher modes - same shape as
// CORE-012/013. The callback receives the cipher mode label for
// failure-message diagnosability.
func basicModeWalk(
	t *testing.T,
	fixture string,
	assertMode func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, list sep2.DERControlList),
) {
	t.Helper()

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

			srv := bootWithBasicFixture(t, fixture, mode.opts)
			ctx := context.Background()
			client := srv.Client()

			prog := walkSingleProgramBasic(t, ctx, client)
			ctrlList := walkSingleControlListBasic(t, ctx, client, prog)
			assertMode(t, mode.name, client, prog, ctrlList)
		})
	}
}

// basicModeWalkDefault is the variant for Section 8.7 - Ramp Rates is the
// only V1.2 BASIC procedure that targets DefaultDERControl directly
// (no DERControl event). The callback receives the DefaultDERControl.
func basicModeWalkDefault(
	t *testing.T,
	fixture string,
	assertMode func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, dderc sep2.DefaultDERControl),
) {
	t.Helper()

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

			srv := bootWithBasicFixture(t, fixture, mode.opts)
			ctx := context.Background()
			client := srv.Client()

			prog := walkSingleProgramBasic(t, ctx, client)
			dderc := walkDefaultDERControlBasic(t, ctx, client, prog)
			assertMode(t, mode.name, client, prog, dderc)
		})
	}
}

// walkSingleProgramBasic walks /dcap -> /edev -> /fsa -> first FSA's
// DERProgramList and returns the lone DERProgram. The BASIC-004..012
// fixtures all ship exactly one program; this helper fatals if the
// fixture topology drifts.
func walkSingleProgramBasic(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
) sep2.DERProgram {
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
	if len(edevList.EndDevice) != 1 {
		t.Fatalf("EndDeviceList: got %d EndDevices, want 1 (BASIC-004..012 fixtures all ship a single EndDevice)",
			len(edevList.EndDevice))
	}
	edev := edevList.EndDevice[0]
	if edev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("EndDevice[0].FunctionSetAssignmentsListLink is nil")
	}

	var fsaList sep2.FunctionSetAssignmentsList
	if err := c.WalkLink(ctx, sep2.Link{Href: edev.FunctionSetAssignmentsListLink.Href}, &fsaList); err != nil {
		t.Fatalf("walk FSAList: %v", err)
	}
	if len(fsaList.FunctionSetAssignments) != 1 {
		t.Fatalf("FSAList: got %d FSAs, want 1", len(fsaList.FunctionSetAssignments))
	}
	fsa := fsaList.FunctionSetAssignments[0]
	if fsa.DERProgramListLink == nil {
		t.Fatal("FSA[0].DERProgramListLink is nil")
	}

	var progList sep2.DERProgramList
	if err := c.WalkLink(ctx, sep2.Link{Href: fsa.DERProgramListLink.Href}, &progList); err != nil {
		t.Fatalf("walk DERProgramList: %v", err)
	}
	if len(progList.DERProgram) != 1 {
		t.Fatalf("DERProgramList: got %d programs, want 1", len(progList.DERProgram))
	}
	return progList.DERProgram[0]
}

// walkSingleControlListBasic follows the program's DERControlListLink
// and returns the resulting list. The per-mode tests then assert the
// expected len() (1 for BASIC-006/008/009/010, varies for the
// gap-skipped tests).
func walkSingleControlListBasic(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	prog sep2.DERProgram,
) sep2.DERControlList {
	t.Helper()

	if prog.DERControlListLink == nil {
		t.Fatal("DERProgram.DERControlListLink is nil")
	}
	var list sep2.DERControlList
	if err := c.WalkLink(ctx, sep2.Link{Href: prog.DERControlListLink.Href}, &list); err != nil {
		t.Fatalf("walk DERControlListLink %s: %v", prog.DERControlListLink.Href, err)
	}
	return list
}

// walkDefaultDERControlBasic follows the program's
// DefaultDERControlLink and returns the resulting resource. Used by
// the Section 8.7 (Ramp Rates) helper variant.
func walkDefaultDERControlBasic(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	prog sep2.DERProgram,
) sep2.DefaultDERControl {
	t.Helper()

	if prog.DefaultDERControlLink == nil {
		t.Fatal("DERProgram.DefaultDERControlLink is nil")
	}
	var dderc sep2.DefaultDERControl
	if err := c.WalkLink(ctx, sep2.Link{Href: prog.DefaultDERControlLink.Href}, &dderc); err != nil {
		t.Fatalf("walk DefaultDERControlLink %s: %v", prog.DefaultDERControlLink.Href, err)
	}
	return dderc
}

// walkSingleCurveBasic walks /dc with l=255 and asserts the global
// DERCurveList carries exactly `want` curves of the given curveType.
// Used by curve-based BASIC tests (BASIC-006 today; the gap-skipped
// BASIC-011/012 also call this for their DERCurveList leg before
// skipping for the missing field on DERControlBase).
func walkSingleCurveBasic(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	wantCurveType uint8,
	wantCount int,
) sep2.DERCurve {
	t.Helper()

	var list sep2.DERCurveList
	if err := c.WalkLink(ctx, sep2.Link{Href: "/dc?l=255"}, &list); err != nil {
		t.Fatalf("walk /dc: %v", err)
	}
	if got := len(list.DERCurve); got != wantCount {
		t.Fatalf("DERCurveList len = %d, want %d", got, wantCount)
	}
	// Locate the curve of the wanted type (the fixtures ship exactly
	// one curve so list.DERCurve[0].CurveType must match).
	got := list.DERCurve[0]
	if got.CurveType != wantCurveType {
		t.Fatalf("DERCurve[0].CurveType = %d, want %d (curve type drift between fixture and assertion)",
			got.CurveType, wantCurveType)
	}
	return got
}

// formatGap is a small string-builder so each gap-skipped test prints
// a consistent message format pointing at #140 - the follow-up
// ticket gathering all BASIC-NNN sep2 implementation gaps.
//
// #140 closed all five gaps formatGap used to flag. Kept here so
// any future gap re-opens land at the same call site.
//
//nolint:unused // retained per comment above for future BASIC-NNN gap re-opens
func formatGap(test, missingField string) string {
	return fmt.Sprintf(
		"%s wire-fidelity assertion pinned by #140 - pkg/sep2.DERControlBase carries no %s field today; "+
			"adding it changes the public API and is out of scope per #135. "+
			"Procedure walk up to this point succeeded (program + control list rendered correctly).",
		test, missingField)
}

// assertCurveRef checks that a *int32 curve-reference field on a
// DERControlBase survived the wire roundtrip with the expected
// value. Shared by BASIC-004 (4 refs), BASIC-005 (2 refs), BASIC-011
// (1 ref), and BASIC-012 (1 ref).
func assertCurveRef(t *testing.T, cipher, field string, got *int32, want int32) {
	t.Helper()
	if got == nil {
		t.Fatalf("[%s] DERControlBase.%s is nil - fixture dropped or wire-decode lost field", cipher, field)
	}
	if *got != want {
		t.Errorf("[%s] DERControlBase.%s = %d, want %d", cipher, field, *got, want)
	}
}
