package wadl_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// mandatoryUnroutedCeiling is the number of Mandatory (wx:mode="M") methods
// the WADL declares that this server does not route today.
//
// It is a RATCHET, not a target. The conformance gap it records is real and
// tracked; pinning it here keeps the gap from widening unnoticed while route
// ownership moves between core and this repo. Lower it whenever a fix beats
// it: TestMandatoryRouteCoverageRatchet logs the new value to use.
//
// Measured against the server csiptest boots, with the route set core's
// assembly registers, on 2026-08-02.
const mandatoryUnroutedCeiling = 192

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
func bootTarget(t *testing.T) *wadl.Target {
	t.Helper()

	srv := csiptest.BootServer(t)
	if len(srv.MountedPatterns) == 0 {
		t.Fatal("the booted server reported no mounted patterns, so the route cross-check would be vacuous")
	}
	return &wadl.Target{
		BaseURL:         srv.BaseURL,
		Client:          srv.HTTPClient(),
		MountedPatterns: srv.MountedPatterns,
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
	target := bootTarget(t)

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

	// 2. Transport failures. A request that never completed is not a
	//    conformance observation, it is a broken run.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.TransportError != "" }) {
		t.Errorf("transport failure on %s %s: %s", row.Method, row.Path, row.TransportError)
	}

	// 3. Server errors. A 5xx is a defect under every wx:mode, including
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
	target := bootTarget(t)

	res := wadl.Sweep(target, model)

	unrouted := res.CountMode(wadl.ModeMandatory, wadl.VerdictUnrouted)
	total := 0
	for _, m := range model.Methods {
		if m.Mode == wadl.ModeMandatory {
			total++
		}
	}

	t.Logf("mandatory methods: %d declared, %d unrouted, %d reached a handler",
		total, unrouted, total-unrouted)

	if unrouted > mandatoryUnroutedCeiling {
		t.Errorf("unrouted mandatory methods = %d, which exceeds the ceiling of %d: "+
			"a route that used to be mounted is no longer reachable",
			unrouted, mandatoryUnroutedCeiling)
	}
	if unrouted < mandatoryUnroutedCeiling {
		t.Logf("unrouted mandatory methods = %d, below the ceiling of %d; "+
			"lower mandatoryUnroutedCeiling to %d to lock the improvement in",
			unrouted, mandatoryUnroutedCeiling, unrouted)
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
