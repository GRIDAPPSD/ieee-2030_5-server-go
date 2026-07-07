// CSIP V1.2 §5.7 — Basic Time. Chains GET /dcap → dcap.TimeLink → GET
// /tm. Asserts the Time resource is well-formed and that currentTime
// is within ±5s of the test process's wallclock.
//
// Procedure (paraphrased; full text in V1.2 PDF §5.7):
//
//	Step 1: GET /dcap.
//	Step 2: Follow dcap.TimeLink (must be present per IEEE-2030.5 §10
//	        and CSIP §5.7).
//	Step 3: GET /tm.
//	Step 4: Assert the response is a Time resource with a non-zero
//	        CurrentTime within ±5s of the test wallclock.
//	Step 5: Assert Quality is a well-known TimeQualityType value.
//
// Spec/code quality-metric note: the IEEE-060 ticket says "assert
// qualityMetric == 7 (NTP-synchronized)". The codebase's
// pkg/sep2/time.go uses TimeQualityNTP = 4 (per spec §9.2 table —
// lower numbers indicate better synchronization), and the helper's
// defaultServerConfig boots with TimeQualityNTP. Asserting == 7 would
// fail against the harness's own defaults. Per the ticket's escape
// hatch ("if the existing handler doesn't say 7, document and skip
// the strict assertion, ship the test ungated"), this test asserts
// (a) Quality is in the well-known set {3..7} and (b) CurrentTime is
// within ±5s of the test wallclock — both proving the chained-GET
// pattern works end-to-end without bikeshedding the constant.
//
// This test also validates Phase 1's WalkLink helper end-to-end
// against the real server: handshake_test.go covers WalkLink against
// /edev, CORE-005 covers WalkLink against /tm.
package csip_test

import (
	"context"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCORE_005_BasicTime exercises CSIP V1.2 §5.7.
func TestCORE_005_BasicTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Boot a fresh server. The default config carries
	// TimeQuality = TimeQualityNTP (see csiptest.defaultServerConfig).
	// Time-related fields fall through to the harness defaults.
	srv := csiptest.BootServer(t)

	// Step 1: GET /dcap.
	dcap, err := srv.Client().GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}

	// Step 2: Follow dcap.TimeLink. The link MUST be advertised per
	// CSIP §5.7 — its absence is itself a conformance failure.
	if dcap.TimeLink == nil {
		t.Fatal("dcap.TimeLink is nil; CSIP §5.7 requires Time to be advertised")
	}
	if dcap.TimeLink.Href == "" {
		t.Fatal("dcap.TimeLink.Href is empty; CSIP §5.7 requires a usable URL")
	}

	// Record wallclock immediately before issuing the GET so the ±5s
	// envelope is measured against the closest possible reference.
	before := time.Now().Unix()

	// Step 3: GET /tm via the chained-GET helper (Phase 1 IEEE-056).
	var tm sep2.Time
	if err := srv.Client().WalkLink(ctx, *dcap.TimeLink, &tm); err != nil {
		t.Fatalf("WalkLink(dcap.TimeLink=%q): %v", dcap.TimeLink.Href, err)
	}

	after := time.Now().Unix()

	// Step 4: assert CurrentTime is within ±5s of test wallclock.
	// Use the (before, after) bracket plus a 5s slack on each side
	// so a slow CI runner does not flake the assertion.
	const slack = int64(5)
	if tm.CurrentTime < before-slack {
		t.Errorf("Time.CurrentTime = %d, want >= %d (wallclock %d - %ds slack)",
			tm.CurrentTime, before-slack, before, slack)
	}
	if tm.CurrentTime > after+slack {
		t.Errorf("Time.CurrentTime = %d, want <= %d (wallclock %d + %ds slack)",
			tm.CurrentTime, after+slack, after, slack)
	}

	// Step 5: assert Quality is a well-known TimeQualityType per spec §9.2.
	// Range {3..7} covers GPS, NTP, NoTimeSource, Level6, Uncoordinated.
	// See the spec/code quality-metric note in the file doc comment.
	switch tm.Quality {
	case sep2.TimeQualityGPS,
		sep2.TimeQualityNTP,
		sep2.TimeQualityNoTimeSource,
		sep2.TimeQualityLevel6,
		sep2.TimeQualityIntentionallyUncoordinated:
		// well-known — pass
	default:
		t.Errorf("Time.Quality = %d, want one of {3 GPS, 4 NTP, 5 NoTimeSource, 6 Level6, 7 IntentionallyUncoordinated}",
			tm.Quality)
	}

	// Server's identity check: the Time resource must self-advertise
	// at /tm. Without this we cannot tell whether the chained-GET
	// helper accidentally returned a resource from a different
	// endpoint with overlapping XML shape.
	if tm.Href != "/tm" {
		t.Errorf("Time.Href = %q, want %q", tm.Href, "/tm")
	}
}
