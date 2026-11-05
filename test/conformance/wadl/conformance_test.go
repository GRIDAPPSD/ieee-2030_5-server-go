package wadl_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// mandatoryUnroutedCeiling is the number of Mandatory (wx:mode="M") methods
// the sweep does not find at a routed handler: methods answered by no route,
// plus methods of a resource no link from /dcap leads to, which are not probed.
//
// It is a RATCHET, not a target. The conformance gap it records is real and
// tracked; pinning it here keeps the gap from widening unnoticed while route
// ownership moves between core and this repo. Lower it whenever a fix beats
// it: TestMandatoryRouteCoverageRatchet logs the new value to use.
//
// Measured against the server csiptest boots, with the route set core's
// assembly registers, on 2026-11-05.
const mandatoryUnroutedCeiling = 190

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

// TestMandatoryRouteCoverageRatchet keeps the mandatory-method gap from
// widening without failing on the gap that exists today.
//
// The count is a ceiling, not an equality: a fix that lowers it passes, and
// the test says so in its failure message when the ceiling is beaten by
// enough to be worth re-pinning. A regression that raises it fails.
//
// This is a count, deliberately not a list. A committed list of which
// mandatory routes are unrouted would be a partial transcription of the
// WADL, which is not distributed with this project.
func TestMandatoryRouteCoverageRatchet(t *testing.T) {
	model := wadl.MustLoadModel(t)
	target := bootTarget(t, wadl.SchemaForTest(t))

	res := wadl.Sweep(target, model)

	unrouted := res.CountMode(wadl.ModeMandatory, wadl.VerdictUnrouted) +
		res.NotReachedMethods(model, wadl.ModeMandatory)
	total := 0
	for _, m := range model.Methods {
		if m.Mode == wadl.ModeMandatory {
			total++
		}
	}

	t.Logf("mandatory methods: %d declared, %d unrouted or not reached, %d reached a handler",
		total, unrouted, total-unrouted)

	if unrouted > mandatoryUnroutedCeiling {
		t.Errorf("unrouted or not reached mandatory methods = %d, which exceeds the ceiling of %d: "+
			"a route or a link that used to be served is gone",
			unrouted, mandatoryUnroutedCeiling)
	}
	if unrouted < mandatoryUnroutedCeiling {
		t.Logf("unrouted or not reached mandatory methods = %d, below the ceiling of %d; "+
			"lower mandatoryUnroutedCeiling to %d to lock the improvement in",
			unrouted, mandatoryUnroutedCeiling, unrouted)
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
