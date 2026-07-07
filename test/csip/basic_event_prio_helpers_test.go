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
// IEEE-085 (overlapping variants) reuses `bootWithEventPrioFixture`
// and the per-program walk helpers; its overlap-aware invariant
// (`assertOverlapResolution`) lives below as the dual of
// `assertDisjointIntervals` so both families share one home.
package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
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
		DERPrograms:        stores.DERPrograms.ScopedStore, // IEEE-097 wrapper; IEEE-104.
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

// assertOpModValue asserts that base carries the named opMod* field
// and that its scalar Value matches want. The field string is one of
// "fixedW", "maxLimW", "targetW" — extend as new fixtures introduce
// other opMod families.
//
// IEEE-085 BASIC-024..026 exercise multiple opMod families per
// fixture (SP→fixedW, SY→maxLimW), so a single per-field assertion
// helper keeps the test bodies linear instead of branching by string
// at each call site.
func assertOpModValue(t *testing.T, label string, base *sep2.DERControlBase, field string, want int64) {
	t.Helper()

	if base == nil {
		t.Errorf("%s DERControlBase is nil — fixture dropped on the wire", label)
		return
	}
	var got *sep2.ActivePower
	switch field {
	case "fixedW":
		got = base.OpModFixedW
	case "maxLimW":
		got = base.OpModMaxLimW
	case "targetW":
		got = base.OpModTargetW
	default:
		t.Fatalf("%s: unknown opMod field %q (extend assertOpModValue)", label, field)
	}
	if got == nil {
		t.Errorf("%s opMod field %q is nil — fixture dropped", label, field)
		return
	}
	if got.Value != want {
		t.Errorf("%s opMod[%s].Value = %d, want %d", label, field, got.Value, want)
	}
}

// overlapEntry is the per-program tuple the overlap-resolution helper
// consumes. Programs are identified by their primacy + a stable label
// (the MRID is the usual choice). Each entry carries the rendered
// DERControl whose [Start, Start+Duration) window participates in the
// overlap and a tag describing which opMod* family the event drives —
// "similar" vs "independent" is the BASIC-021..023 vs BASIC-024..026
// distinction.
//
// IEEE-085 keeps the tuple flat (one DERControl per program) because
// the V1.2 §8.21-§8.26 procedures specify exactly one scheduled event
// per program. The helper signature could be generalized to a slice
// per program, but that would invite over-abstraction the procedures
// do not justify.
type overlapEntry struct {
	programLabel string // for failure messages (typically the DERProgram MRID)
	primacy      uint8
	control      sep2.DERControl
	opModFamily  string // "fixedW", "maxLimW", "voltVar" etc — anything stable
}

// assertOverlapResolution is the IEEE-085 analog of
// assertDisjointIntervals. It asserts the inverse invariant — at
// least one pair of rendered DERControls occupies INTERSECTING
// [Start, Start+Duration) windows — and additionally surfaces the
// IEEE 2030.5 §10.10 expected-winner per overlap region so a future
// scheduler-side test (or DER client conformance suite) can pin the
// resolution rule without re-deriving it.
//
// Resolution rule (server-side renders both events; scheduler resolves):
//
//  1. Same opMod* family ("similar")    → lower Primacy wins.
//  2. Tie on Primacy                    → earlier Interval.Start wins.
//  3. Tie on (Primacy, Start)           → MRID lex order wins (mRID per §10.10).
//  4. Different opMod* family ("independent") → both events apply
//     simultaneously; resolution is per-mode, not per-event. The
//     helper still verifies the overlap exists but does NOT pick a
//     single winner.
//
// Server-side IEEE-085 wires only the overlap-detection arm — clients
// and the scheduler test (plan-1 scope) consume the returned winners
// when present. Returns (winners-by-pair) keyed by a stable
// "<a>|<b>" label so callers can spot-check without duplicating the
// rule.
func assertOverlapResolution(t *testing.T, entries []overlapEntry) map[string]string {
	t.Helper()

	winners := make(map[string]string)
	if len(entries) < 2 {
		t.Errorf("assertOverlapResolution: need ≥2 entries, got %d", len(entries))
		return winners
	}

	overlapFound := false
	for i := 0; i < len(entries); i++ {
		a := entries[i].control.Interval
		if a == nil {
			t.Errorf("%s DERControl (mrid=%q): Interval is nil",
				entries[i].programLabel, entries[i].control.MRID)
			continue
		}
		aEnd := a.Start + int64(a.Duration)
		for j := i + 1; j < len(entries); j++ {
			b := entries[j].control.Interval
			if b == nil {
				t.Errorf("%s DERControl (mrid=%q): Interval is nil",
					entries[j].programLabel, entries[j].control.MRID)
				continue
			}
			bEnd := b.Start + int64(b.Duration)
			// Overlap iff NOT (a.End ≤ b.Start OR b.End ≤ a.Start).
			if aEnd <= b.Start || bEnd <= a.Start {
				t.Errorf("DERControls %q and %q intervals are disjoint, want overlap: [%d,%d) vs [%d,%d)",
					entries[i].control.MRID, entries[j].control.MRID,
					a.Start, aEnd, b.Start, bEnd)
				continue
			}
			overlapFound = true
			pairKey := entries[i].control.MRID + "|" + entries[j].control.MRID
			if entries[i].opModFamily == entries[j].opModFamily {
				winners[pairKey] = pickSimilarWinner(entries[i], entries[j])
			}
			// independent (different opMod*): no single winner; both apply.
		}
	}
	if !overlapFound {
		t.Error("assertOverlapResolution: no overlapping pairs found — fixture is fully disjoint")
	}
	return winners
}

// pickSimilarWinner applies the §10.10 tie-break ladder for two
// same-opMod-family events: lower Primacy, then earlier Start, then
// MRID lex. Returns the winner's MRID.
func pickSimilarWinner(a, b overlapEntry) string {
	if a.primacy != b.primacy {
		if a.primacy < b.primacy {
			return a.control.MRID
		}
		return b.control.MRID
	}
	aStart, bStart := int64(0), int64(0)
	if a.control.Interval != nil {
		aStart = a.control.Interval.Start
	}
	if b.control.Interval != nil {
		bStart = b.control.Interval.Start
	}
	if aStart != bStart {
		if aStart < bStart {
			return a.control.MRID
		}
		return b.control.MRID
	}
	if a.control.MRID < b.control.MRID {
		return a.control.MRID
	}
	return b.control.MRID
}
