package wadl_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// outsidePolicyCeiling is the number of methods outside the method-mode policy
// (policy.go): Mandatory and Optional methods that are not conformant, Deprecated
// methods answering anything but 405 or conformant, and Error methods answering
// anything but 400 or 405. Methods of a resource the crawl did not reach are not
// probed and count as outside.
//
// It is a RATCHET, not a target. The gap it records is real and tracked; pinning
// it keeps the gap from widening unnoticed. Lower it whenever a fix beats it:
// TestMethodModePolicyRatchet logs the new value to use.
//
// Measured against the server csiptest boots, with the route set core's
// assembly registers, on 2026-11-05.
const outsidePolicyCeiling = 455

// outsidePolicyByMode is outsidePolicyCeiling split by wx:mode, so a rise in one
// mode cannot hide behind a fall in another. The values sum to the total.
var outsidePolicyByMode = map[wadl.Mode]int{
	wadl.ModeMandatory:  195,
	wadl.ModeOptional:   64,
	wadl.ModeDeprecated: 47,
	wadl.ModeError:      149,
}

// The manager-only writes (restrictedWrites) the sweep reaches are probed as the
// ordinary device. restrictedProbedFloor is how many it reaches, and
// restrictedNotForbiddenCeiling is how many of those the device is not refused
// with a 403 from an authority check. The server has no such check yet, so every
// probed pair is not refused; the ceiling falls to zero, and is then an equality,
// as the check lands. The unreached remainder of the declared writes is not
// probed and sits in neither number.
const (
	restrictedProbedFloor         = 5
	restrictedNotForbiddenCeiling = 5
)

// notReachedCeiling is the number of WADL resources with a Mandatory or Optional
// method that the crawl from /dcap misses though the schema names a link for
// them, and crawlReachedFloor is how many it finds. A resource served without a
// link to it counts against the first and cannot raise the second.
const (
	notReachedCeiling = 92
	crawlReachedFloor = 32
)

// EnvArtifact names an optional file to write the full row-by-row sweep to.
//
// It is opt-in and defaults to off. The output is a record of what THIS server
// did, not a copy of the standard, but it is still verbose enough that writing
// it unasked into a repo working tree would be a nuisance.
const EnvArtifact = "SEP2_WADL_SWEEP_OUT"

// bootTarget boots a real server over real mTLS and returns it as a sweep
// target.
//
// The server is booted through csiptest, the same helper the CSIP suite uses,
// so this harness measures the same server the rest of the conformance tests
// measure. It listens on a kernel-assigned loopback port and is torn down by
// t.Cleanup, so nothing is left running and no port is chosen by hand.
func bootTarget(t *testing.T, links *wadl.LinkSchema) *wadl.Target {
	t.Helper()

	srv := csiptest.BootServer(t)
	if len(srv.MountedPatterns) == 0 {
		t.Fatal("the booted server reported no mounted patterns, so the route cross-check would be vacuous")
	}
	return &wadl.Target{
		BaseURL:         srv.BaseURL,
		Client:          srv.HTTPClient(),
		MountedPatterns: srv.MountedPatterns,
		Links:           links,
		Stores:          srv.Stores,
		ManagerClient:   srv.NewManagerClient(t),
	}
}

// TestWADLConformanceSweep walks the booted server against every method the
// normative WADL declares.
//
// What this test GATES on is deliberately narrow: the integrity of the sweep
// itself, plus failures that are wrong under any reading of the standard (a
// 5xx, a transport failure). It does NOT fail on every non-conformant row,
// because the conformance gap is a known, tracked body of work and a test that
// is red by design gets muted. The gap is reported in full instead, and the
// ratchet below keeps it from silently widening.
func TestWADLConformanceSweep(t *testing.T) {
	model := wadl.MustLoadModel(t)
	target := bootTarget(t, wadl.SchemaForTest(t))

	res := wadl.Sweep(target, model)

	for _, line := range res.Summary() {
		t.Log(line)
	}

	if path := os.Getenv(EnvArtifact); path != "" {
		writeArtifact(t, path, res)
	}

	// 1. Harness integrity. The two unrouted discriminators are independent
	//    on purpose; when they disagree, the harness or a middleware is
	//    wrong, and every routing verdict in the run is suspect. This is
	//    checked BEFORE any conformance number is believed.
	if bad := res.RoutingDisagreements(); len(bad) > 0 {
		for _, row := range bad {
			t.Errorf("routing disagreement on %s %s: wire verdict %q but the router's own enumeration says mounted=%v",
				row.Method, row.Path, row.Verdict, *row.MountedByRouter)
		}
		t.Fatal("the wire evidence and the router's route enumeration disagree, so the routing verdicts in this run cannot be trusted")
	}

	// 1b. The crawl itself. A walk from /dcap that reaches no resource has
	//     found nothing, and every row after it measures sample paths only.
	if res.CrawlReached == 0 || res.LinksFollowed() == 0 {
		t.Fatalf("the link crawl from /dcap followed %d links and reached %d WADL resources, so the sweep scored sample paths and not what a client finds",
			res.LinksFollowed(), res.CrawlReached)
	}

	// 2. Seed failures. A refused seed leaves the subtree hanging off it
	//    unwalked, which shrinks the sweep without failing anything else.
	for _, row := range res.Filter(func(r wadl.Row) bool {
		return r.Phase == wadl.PhaseSeed && r.Verdict != wadl.VerdictSeeded
	}) {
		t.Errorf("seed failed: %s %s (%s) returned status %d, verdict %q: %s",
			row.Method, row.Path, row.Element, row.Status, row.Verdict, row.TransportError)
	}

	// 2b. Unseeded probes. A routed row that answers 404 means the sweep
	//     probed a resource nothing created, so it tested the store and not
	//     the handler. Every probed resource has a seed in seedSet; a new
	//     fixture goes there.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.Verdict == wadl.VerdictRoutedNotFound }) {
		t.Errorf("routed but not found: %s %s (WADL path %s, mode %s) has no seeded instance",
			row.Method, row.Path, row.WADLPath, row.Mode)
	}

	// 3. Transport failures. A request that never completed is not a
	//    conformance observation, it is a broken run.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.TransportError != "" }) {
		t.Errorf("transport failure on %s %s: %s", row.Method, row.Path, row.TransportError)
	}

	// 4. Server errors. A 5xx is a defect under every wx:mode, including
	//    Error mode, where the standard asks for a 400 or a 405 and never
	//    for a panic.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.Status >= 500 }) {
		t.Errorf("server error: %s %s returned %d (WADL path %s, mode %s)",
			row.Method, row.Path, row.Status, row.WADLPath, row.Mode)
	}
}

// TestMethodModePolicyRatchet keeps the count of methods outside the method-mode
// policy from widening without failing on the gap that exists today.
//
// The count is a ceiling, not an equality: a fix that lowers it passes, and
// the test says so when the ceiling is beaten and worth re-pinning. A regression
// that raises it fails.
//
// This is a count, deliberately not a list. A committed list of which methods
// are outside the policy would be a partial transcription of the WADL, which is
// not distributed with this project.
func TestMethodModePolicyRatchet(t *testing.T) {
	model := wadl.MustLoadModel(t)
	res := wadl.Sweep(bootTarget(t, wadl.SchemaForTest(t)), model)

	outside := res.OutsidePolicy(model)
	for _, line := range wadl.PolicyBreakdown(outside) {
		t.Log("outside policy: " + line)
	}
	t.Logf("methods outside the method-mode policy: %d of %d declared", len(outside), len(model.Methods))

	if len(outside) > outsidePolicyCeiling {
		t.Errorf("methods outside the method-mode policy = %d, which exceeds the ceiling of %d: "+
			"a route or a link that used to be served is gone, or a method now answers outside its mode",
			len(outside), outsidePolicyCeiling)
	}
	if len(outside) < outsidePolicyCeiling {
		t.Logf("methods outside the method-mode policy = %d, below the ceiling of %d; "+
			"lower outsidePolicyCeiling to %d to lock the improvement in",
			len(outside), outsidePolicyCeiling, len(outside))
	}

	byMode := map[wadl.Mode]int{}
	for _, row := range outside {
		byMode[row.Mode]++
	}
	for mode, ceiling := range outsidePolicyByMode {
		if byMode[mode] > ceiling {
			t.Errorf("methods outside the policy in mode %s = %d, which exceeds its ceiling of %d",
				mode, byMode[mode], ceiling)
		}
		if byMode[mode] < ceiling {
			t.Logf("mode %s = %d, below its ceiling of %d; lower outsidePolicyByMode[%s] to lock the improvement in",
				mode, byMode[mode], ceiling, mode)
		}
	}
	for mode, n := range byMode {
		if _, pinned := outsidePolicyByMode[mode]; !pinned {
			t.Errorf("%d methods outside the policy in mode %q, which has no ceiling in outsidePolicyByMode", n, mode)
		}
	}
}

// TestRestrictedWritesForbidDevice sweeps with a manager certificate and shows what
// an ordinary device is answered on each write the server restricts to a manager.
// The manager's own answer is scored by the policy ratchet like any other row.
func TestRestrictedWritesForbidDevice(t *testing.T) {
	model := wadl.MustLoadModel(t)
	res := wadl.Sweep(bootTarget(t, wadl.SchemaForTest(t)), model)

	asManager := len(res.Filter(func(r wadl.Row) bool { return r.AsManager }))
	probed := len(res.Filter(func(r wadl.Row) bool { return r.Phase == wadl.PhaseRestricted }))
	if asManager == 0 || asManager != probed {
		t.Fatalf("%d restricted writes were made as the manager and %d probed as the device: "+
			"the two must pair, or the 403 assertion is vacuous", asManager, probed)
	}

	notForbidden := res.DeviceProbedNotForbidden()
	t.Logf("restricted writes: %d declared, %d probed as manager and device, %d of those without a device 403",
		wadl.RestrictedWrites(model), probed, notForbidden)

	if probed < restrictedProbedFloor {
		t.Errorf("restricted writes probed = %d, below the floor of %d: a pair the sweep used to reach is no longer probed",
			probed, restrictedProbedFloor)
	}
	if probed > restrictedProbedFloor {
		t.Logf("restricted writes probed = %d, above the floor of %d; raise restrictedProbedFloor to %d to lock the new reach in",
			probed, restrictedProbedFloor, probed)
	}
	if notForbidden > restrictedNotForbiddenCeiling {
		t.Errorf("probed restricted writes without a device 403 = %d, which exceeds the ceiling of %d",
			notForbidden, restrictedNotForbiddenCeiling)
	}
	if notForbidden < restrictedNotForbiddenCeiling {
		t.Logf("probed restricted writes without a device 403 = %d, below the ceiling of %d; "+
			"lower restrictedNotForbiddenCeiling to %d to lock the improvement in",
			notForbidden, restrictedNotForbiddenCeiling, notForbidden)
	}
}

// TestCrawlReachRatchet keeps the crawl from finding fewer resources, and the
// schema-named resources it misses from growing. Without a schema the missed set
// is not comparable, so the ceiling is checked only when one is supplied.
func TestCrawlReachRatchet(t *testing.T) {
	model := wadl.MustLoadModel(t)
	links := wadl.SchemaForTest(t)
	res := wadl.Sweep(bootTarget(t, links), model)

	missed := len(res.Filter(func(r wadl.Row) bool { return r.Verdict == wadl.VerdictNotReached }))
	t.Logf("crawl reached %d resources, %d not reached", res.CrawlReached, missed)

	if res.CrawlReached < crawlReachedFloor {
		t.Errorf("crawl reached %d resources, below the floor of %d: a link the server used to emit is gone",
			res.CrawlReached, crawlReachedFloor)
	}
	if res.CrawlReached > crawlReachedFloor {
		t.Logf("crawl reached %d, above the floor of %d; raise crawlReachedFloor to %d to lock the improvement in",
			res.CrawlReached, crawlReachedFloor, res.CrawlReached)
	}
	if links == nil {
		t.Skip("no sep.xsd: the not_reached ceiling needs the schema to tell unlinked resources from missed ones")
	}
	if missed > notReachedCeiling {
		t.Errorf("not_reached resources = %d, which exceeds the ceiling of %d", missed, notReachedCeiling)
	}
	if missed < notReachedCeiling {
		t.Logf("not_reached resources = %d, below the ceiling of %d; lower notReachedCeiling to %d to lock the improvement in",
			missed, notReachedCeiling, missed)
	}
}

// writeArtifact records the sweep row by row as JSON Lines.
func writeArtifact(t *testing.T, path string, res *wadl.Results) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s=%s: %v", EnvArtifact, path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close sweep artifact %s: %v", path, cerr)
		}
	}()

	enc := json.NewEncoder(f)
	for _, row := range res.Rows {
		if err := enc.Encode(row); err != nil {
			t.Fatalf("write sweep artifact %s: %v", path, err)
		}
	}
	t.Logf("sweep artifact written: %s (%d rows)", path, len(res.Rows))
}
