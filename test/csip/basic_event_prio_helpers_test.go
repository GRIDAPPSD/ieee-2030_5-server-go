// Shared helpers for the BASIC-016..020 non-overlap event-prioritization
// tests, plus a forward-compatible seam for IEEE-085 (BASIC-021..026
// overlapping variants). The procedural shape across all five
// non-overlap tests is:
//
//	Step 1: server is seeded with a multi-DERProgram (+ optional
//	        DefaultDERControl, + optional DERControl) topology where
//	        the DERControl events occupy disjoint [start, start+duration)
//	        windows.
//	Step 2: client walks /dcap → /edev → /fsa → DERProgramList,
//	        asserting the program count + primacy ordering per fixture.
//	Step 3: per-program, client walks DefaultDERControlLink and
//	        DERControlListLink, asserting payloads round-trip exactly
//	        as the fixture seeded them (MRID, Interval, EventStatus,
//	        opMod*).
//
// IEEE-084 wires the server-side wire-fidelity tests against this
// shape — the server just renders the seeded fixture; scheduler
// runtime prioritization is the DER client's problem (plan-1's scope).
//
// IEEE-085 (overlapping variants) will reuse `bootWithEventPrioFixture`
// and the per-program walk helpers; the assertion-level
// non-overlap-specific helper (`assertDisjointIntervals`) is local
// to IEEE-084 because IEEE-085's intervals overlap by construction.
package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// bootWithEventPrioFixture loads the named fixture under fixtures/ into
// a fresh store set and boots an in-process server. Shared across the
// BASIC-016..020 tests — each test in IEEE-084 has the same boot shape
// (load YAML, walk via TLS client). Mirrors the same pattern used by
// CORE-012's bootWithDERProgramFixture; centralizing this 12-line
// boot keeps each procedure test focused on its per-fixture
// assertions.
//
// IEEE-085 will call this same helper for BASIC-021..026 — non-overlap
// vs overlap is a fixture-content distinction, not a topology-shape
// distinction, so the boot path is identical.
func bootWithEventPrioFixture(
	t *testing.T,
	fixture string,
	extraOpts []csiptest.BootOption,
) *csiptest.BootedServer {
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
	path := filepath.Join("fixtures", fixture)
	if err := csiptest.Load(context.Background(), target, path); err != nil {
		t.Fatalf("load %s: %v", path, err)
	}

	opts := append([]csiptest.BootOption{csiptest.WithStores(stores)}, extraOpts...)
	return csiptest.BootServer(t, opts...)
}

// runUnderBothCiphers invokes inner under each of GCM and CCM cipher
// modes as t.Run subtests, marking each subtest t.Parallel. Mirrors
// the per-mode harness used by CORE-012/013 and IEEE-082's
// basic_002 — every BASIC-NNN test in this package runs under both
// ciphers so the spec cipher path (CCM-8) is exercised on the same
// procedure walk.
func runUnderBothCiphers(t *testing.T, inner func(t *testing.T, extraOpts []csiptest.BootOption)) {
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
			inner(t, mode.opts)
		})
	}
}

// walkToFirstEDevFSAList walks /dcap → /edev (list) → first EndDevice
// → /edev/{id}/fsa (list, l=255) and returns the parsed FSA list. Used
// by every BASIC-016..020 test that pivots through the FSA hierarchy.
// Local to IEEE-084 — CORE-012's walkToFirstFSA returns only the
// first FSA, whereas BASIC-020 needs the full list to walk both FSAs
// (2 DERPrograms → 2 FSAs in the canonical fixture shape).
func walkToFirstEDevFSAList(
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
		t.Fatal("DeviceCapability.EndDeviceListLink is nil — server did not advertise /edev")
	}

	var edevList sep2.EndDeviceList
	if err := c.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &edevList); err != nil {
		t.Fatalf("walk EndDeviceListLink %s: %v", dcap.EndDeviceListLink.Href, err)
	}
	if len(edevList.EndDevice) == 0 {
		t.Fatal("EndDeviceList carries zero entries — fixture not loaded?")
	}
	edev := edevList.EndDevice[0]

	if edev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("EndDevice.FunctionSetAssignmentsListLink is nil — fixture topology drift")
	}

	var fsaList sep2.FunctionSetAssignmentsList
	if err := c.WalkLink(ctx,
		sep2.Link{Href: edev.FunctionSetAssignmentsListLink.Href + "?l=255"},
		&fsaList,
	); err != nil {
		t.Fatalf("walk FunctionSetAssignmentsListLink %s: %v",
			edev.FunctionSetAssignmentsListLink.Href, err)
	}
	return fsaList
}

// walkProgramListByHref fetches a DERProgramList at the given href
// with l=255 so the entire program set returns in one response. Used
// by BASIC-020 (2 programs per FSA) and reused as the basic per-FSA
// program walker by the single-program fixtures.
func walkProgramListByHref(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	href string,
) sep2.DERProgramList {
	t.Helper()

	var list sep2.DERProgramList
	if err := c.WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("walk DERProgramList %s: %v", href, err)
	}
	return list
}

// walkDERControlListByHref fetches a DERControlList at the given href
// with l=255. All BASIC-016..020 fixtures ship at most 2 controls per
// program, so l=255 is a regression guard rather than a paging limit
// in practice.
func walkDERControlListByHref(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	href string,
) sep2.DERControlList {
	t.Helper()

	var list sep2.DERControlList
	if err := c.WalkLink(ctx, sep2.Link{Href: href + "?l=255"}, &list); err != nil {
		t.Fatalf("walk DERControlList %s: %v", href, err)
	}
	return list
}

// walkDefaultDERControlByHref fetches a DefaultDERControl at the given
// href. Per the server's HandleSingletonGetPut, a missing default
// renders as 200 OK with DERControlBase == nil; tests assert which
// branch they expect.
func walkDefaultDERControlByHref(
	t *testing.T,
	ctx context.Context,
	c *csiptest.Client,
	href string,
) sep2.DefaultDERControl {
	t.Helper()

	var dderc sep2.DefaultDERControl
	if err := c.WalkLink(ctx, sep2.Link{Href: href}, &dderc); err != nil {
		t.Fatalf("walk DefaultDERControl %s: %v", href, err)
	}
	return dderc
}

// assertEventInterval asserts the rendered Interval matches the
// expected Start and Duration. Surfaces an actionable failure message
// for the common "loader dropped Interval" regression.
func assertEventInterval(
	t *testing.T,
	label string,
	got *sep2.DateTimeInterval,
	wantStart int64,
	wantDuration uint32,
) {
	t.Helper()
	if got == nil {
		t.Errorf("%s Interval is nil — fixture interval dropped on the wire", label)
		return
	}
	if got.Start != wantStart {
		t.Errorf("%s Interval.Start = %d, want %d", label, got.Start, wantStart)
	}
	if got.Duration != wantDuration {
		t.Errorf("%s Interval.Duration = %d, want %d", label, got.Duration, wantDuration)
	}
}

// assertEventStatus asserts the rendered EventStatus.CurrentStatus
// matches the expected status code. The other EventStatus fields are
// asserted inline where they matter — most BASIC-016..020 events
// ship CurrentStatus = Scheduled (0).
func assertEventStatus(t *testing.T, label string, got *sep2.EventStatus, wantStatus uint8) {
	t.Helper()
	if got == nil {
		t.Errorf("%s EventStatus is nil — fixture event status dropped on the wire", label)
		return
	}
	if got.CurrentStatus != wantStatus {
		t.Errorf("%s EventStatus.CurrentStatus = %d, want %d",
			label, got.CurrentStatus, wantStatus)
	}
}

// assertDisjointIntervals asserts that the rendered DERControls
// occupy pairwise-disjoint [Start, Start+Duration) windows — the
// defining property of BASIC-016..020 vs IEEE-085's BASIC-021..026.
// Caller passes the controls in any order; this helper checks every
// (i, j) pair (the list is small — max 2 per the fixture set).
func assertDisjointIntervals(t *testing.T, controls []sep2.DERControl) {
	t.Helper()

	for i := 0; i < len(controls); i++ {
		a := controls[i].Interval
		if a == nil {
			t.Errorf("DERControl[%d] (mrid=%q): Interval is nil", i, controls[i].MRID)
			continue
		}
		aEnd := a.Start + int64(a.Duration)
		for j := i + 1; j < len(controls); j++ {
			b := controls[j].Interval
			if b == nil {
				t.Errorf("DERControl[%d] (mrid=%q): Interval is nil", j, controls[j].MRID)
				continue
			}
			bEnd := b.Start + int64(b.Duration)
			// Disjoint iff a.End ≤ b.Start  OR  b.End ≤ a.Start.
			if !(aEnd <= b.Start || bEnd <= a.Start) {
				t.Errorf("DERControls %q and %q intervals overlap: [%d,%d) vs [%d,%d)",
					controls[i].MRID, controls[j].MRID,
					a.Start, aEnd, b.Start, bEnd)
			}
		}
	}
}
