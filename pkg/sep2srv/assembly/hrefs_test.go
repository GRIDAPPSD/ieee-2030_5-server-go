package assembly_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Mintable-href routing assertion.
//
// These tests are the CI half of the mechanism described in hrefs.go. The boot
// half fails a misconfigured deployment; this half fails a regression before
// anything is deployed, and additionally enforces the direction the boot check
// deliberately does not: that the accepted set shrinks.

// fullyWiredPatterns builds the router every store wired and returns its own
// pattern list. Taking the patterns as a return value from the same call that
// built the handler is the point: there is no second description of the routes
// that could disagree with the one being served.
func fullyWiredPatterns(t *testing.T) []string {
	t.Helper()
	_, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		testStores(),
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	if len(patterns) == 0 {
		t.Fatal("BuildProtocolRouter returned no patterns; the probe would pass vacuously")
	}
	return patterns
}

// TestAssertMintableHrefs_FullyWiredServerStarts is the invariant that startup
// behaviour is unchanged for a correctly configured server: with every store
// wired, the assertion tolerates exactly the known instances and nothing else,
// so adding the boot call cannot stop a working server from starting.
func TestAssertMintableHrefs_FullyWiredServerStarts(t *testing.T) {
	t.Parallel()
	if err := assembly.AssertMintableHrefs(fullyWiredPatterns(t)); err != nil {
		t.Fatalf("a fully wired router must start clean, got:\n%v", err)
	}
}

// TestKnownUnroutedHrefs_RatchetIsExact is the ratchet. It asserts set equality
// in BOTH directions against a fully wired router:
//
//   - an unrouted shape missing from the accepted set is a new instance of the
//     defect class, which is what the boot assertion catches;
//   - an accepted entry that now resolves is a FIXED instance whose entry was
//     not removed, which is how a ratchet quietly stops ratcheting.
//
// The second direction is enforced here rather than at boot on purpose: a
// deployment must not fail because somebody repaired a route.
func TestKnownUnroutedHrefs_RatchetIsExact(t *testing.T) {
	t.Parallel()

	unrouted, err := assembly.CheckMintableHrefs(fullyWiredPatterns(t))
	if err != nil {
		t.Fatalf("CheckMintableHrefs: %v", err)
	}

	actual := make(map[string]assembly.UnroutedHref, len(unrouted))
	for _, u := range unrouted {
		key := u.Href.Method + " " + u.Href.Shape
		if prev, dup := actual[key]; dup {
			t.Errorf("two mintable entries share key %q (%s and %s); keys must be unique or the ratchet cannot be exact",
				key, prev.Href.Mint, u.Href.Mint)
		}
		actual[key] = u
	}
	known := assembly.KnownUnroutedHrefs()

	for key, u := range actual {
		if _, ok := known[key]; !ok {
			t.Errorf("NEW unrouted mintable href %q (%s), minted by %s.\n"+
				"Route it in BuildProtocolRouter, stop minting it, or add it to knownUnroutedHrefs with the card that will fix it.",
				key, u.Reason, u.Href.Mint)
		}
	}
	for key, reason := range known {
		if _, ok := actual[key]; !ok {
			t.Errorf("knownUnroutedHrefs entry %q now RESOLVES; delete the entry so the ratchet keeps shrinking.\nrecorded reason: %s",
				key, reason)
		}
	}

	// The count is asserted so that a wholesale change to either side is
	// visible in the diff even when the two sides were edited to agree.
	if len(actual) != len(known) {
		t.Errorf("unrouted=%d known=%d", len(actual), len(known))
	}
	t.Logf("mintable shapes=%d unrouted=%d", len(assembly.MintableHrefs()), len(actual))
}

// TestKnownUnroutedHrefs_EntriesAreDeclaredAndReasoned guards the ratchet set
// itself. An entry for a shape nobody mints is dead weight that makes the set
// look worse than the code is, and an entry with no recorded reason is a
// suppression rather than a ratchet.
func TestKnownUnroutedHrefs_EntriesAreDeclaredAndReasoned(t *testing.T) {
	t.Parallel()

	declared := make(map[string]bool)
	for _, h := range assembly.MintableHrefs() {
		declared[h.Method+" "+h.Shape] = true
	}
	for key, reason := range assembly.KnownUnroutedHrefs() {
		if !declared[key] {
			t.Errorf("knownUnroutedHrefs has %q but no MintableHrefs entry mints it", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("knownUnroutedHrefs entry %q has no reason; a bare suppression is not a ratchet", key)
		}
	}
}

// TestKnownUnroutedHrefs_ReturnsACopy asserts the accepted set cannot be
// widened through the exported accessor. A consumer that could add a key at
// startup would silence the assertion for exactly the shape it was built to
// catch.
func TestKnownUnroutedHrefs_ReturnsACopy(t *testing.T) {
	t.Parallel()

	before := len(assembly.KnownUnroutedHrefs())
	got := assembly.KnownUnroutedHrefs()
	got["GET /injected/{}"] = "should not stick"
	delete(got, "GET /mup/{}/mr/{}")

	after := assembly.KnownUnroutedHrefs()
	if len(after) != before {
		t.Fatalf("mutating the returned map changed the accepted set: before=%d after=%d", before, len(after))
	}
	if _, ok := after["GET /injected/{}"]; ok {
		t.Error("an injected key survived; the accessor must return a copy")
	}
	if _, ok := after["GET /mup/{}/mr/{}"]; !ok {
		t.Error("a deleted key stayed deleted; the accessor must return a copy")
	}
}

// TestAssertMintableHrefs_NewUnroutedHrefFailsLoudly is the proof that a new
// instance of the defect class fails, and fails with enough detail to fix.
//
// A new instance is simulated by removing a route from the pattern list rather
// than by adding a fake shape, because that is the shape the real regression
// takes: the href stays minted and the route stops existing. The two cases
// cover the two reasons a shape can fail to resolve, and the "method refused"
// case is the one a naive "does any pattern contain this path" check would miss
// entirely.
func TestAssertMintableHrefs_NewUnroutedHrefFailsLoudly(t *testing.T) {
	t.Parallel()

	patterns := fullyWiredPatterns(t)

	cases := []struct {
		name string
		// drop is the pattern removed to simulate the regression.
		drop string
		// wantShape and wantMint must both appear in the error: the href
		// alone leaves the reader grepping for who minted it.
		wantShape  string
		wantMint   string
		wantReason string
	}{
		{
			// The only route on this path, so removing it leaves nothing
			// matching and the failure is a 404.
			name:       "sole route deleted leaves the href unrouted",
			drop:       "GET /edev/{id}/fsa/{fsaId}",
			wantShape:  "/edev/{}/fsa/{}",
			wantMint:   "handlers/fsa.HandleFSA",
			wantReason: string(assembly.ReasonNoRoute),
		},
		{
			// POST /mup/{id} survives, so the path still matches and the
			// failure is a 405. This is the exact shape of the Subscription
			// instance in the ratchet, and the case a "does any pattern
			// contain this path" check would call a pass.
			name:       "route narrowed to another method leaves the href method-refused",
			drop:       "GET /mup/{id}",
			wantShape:  "/mup/{}",
			wantMint:   "handlers/metering.MirrorHref",
			wantReason: string(assembly.ReasonMethodRefused),
		},
		{
			name:       "top-level collection deleted",
			drop:       "GET /rt",
			wantShape:  "/rt",
			wantMint:   "assembly.topLevelMounts",
			wantReason: string(assembly.ReasonNoRoute),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reduced, dropped := without(patterns, tc.drop)
			if !dropped {
				t.Fatalf("pattern %q is not registered; this test is asserting against a route that moved", tc.drop)
			}

			err := assembly.AssertMintableHrefs(reduced)
			if err == nil {
				t.Fatalf("removing %q must fail the assertion, got nil", tc.drop)
			}
			msg := err.Error()
			for _, want := range []string{tc.wantShape, tc.wantMint, tc.wantReason} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not name %q; got:\n%s", want, msg)
				}
			}
		})
	}
}

// TestCheckMintableHrefs_ReportsProbeAndReason asserts the report carries the
// concrete path that was requested, not only the shape. A finding that cannot
// be reproduced with curl gets argued with instead of fixed.
func TestCheckMintableHrefs_ReportsProbeAndReason(t *testing.T) {
	t.Parallel()

	unrouted, err := assembly.CheckMintableHrefs(fullyWiredPatterns(t))
	if err != nil {
		t.Fatalf("CheckMintableHrefs: %v", err)
	}
	if len(unrouted) == 0 {
		t.Skip("nothing unrouted; the ratchet has been emptied and this test has no subject")
	}

	for _, u := range unrouted {
		if strings.Contains(u.Probe, assembly.HrefWildcard) {
			t.Errorf("%s: probe %q still contains a wildcard and is not a requestable path", u.Href.Shape, u.Probe)
		}
		if !strings.HasPrefix(u.Probe, "/") {
			t.Errorf("%s: probe %q is not an absolute path", u.Href.Shape, u.Probe)
		}
		switch u.Reason {
		case assembly.ReasonNoRoute, assembly.ReasonMethodRefused:
		default:
			t.Errorf("%s: unrecognised reason %q", u.Href.Shape, u.Reason)
		}
		if !strings.Contains(u.String(), u.Href.Mint) {
			t.Errorf("%s: String() omits the mint site", u.Href.Shape)
		}
	}
}

// TestCheckMintableHrefs_ReportIsOrdered asserts two runs are comparable.
// An unordered report turns "did this get worse" into a diff of noise.
func TestCheckMintableHrefs_ReportIsOrdered(t *testing.T) {
	t.Parallel()

	unrouted, err := assembly.CheckMintableHrefs(fullyWiredPatterns(t))
	if err != nil {
		t.Fatalf("CheckMintableHrefs: %v", err)
	}
	keys := make([]string, len(unrouted))
	for i, u := range unrouted {
		keys[i] = u.Href.Method + " " + u.Href.Shape
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("report is not sorted: %v", keys)
	}
}

// TestCheckMintableHrefs_MalformedPatternIsAnErrorNotAPanic asserts the
// assertion is not more dangerous than its absence. A panic escaping a
// boot-time check would take down a server that a missing check would have let
// run.
func TestCheckMintableHrefs_MalformedPatternIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	_, err := assembly.CheckMintableHrefs([]string{"GET /edev/{id}", "GET /edev/{id"})
	if err == nil {
		t.Fatal("a malformed pattern must return an error")
	}
	if !strings.Contains(err.Error(), "href probe router") {
		t.Errorf("error does not identify the probe router as the source: %v", err)
	}
}

// TestMintableHrefs_ShapesAreWellFormed asserts the registry itself is usable
// as a probe input. A shape that is not an absolute path, or that carries a fmt
// verb somebody forgot to normalise, would silently probe the wrong URL and
// report a clean result for an href nobody checked.
func TestMintableHrefs_ShapesAreWellFormed(t *testing.T) {
	t.Parallel()

	seen := make(map[string]string)
	for _, h := range assembly.MintableHrefs() {
		what := fmt.Sprintf("%s %s", h.Method, h.Shape)
		if !strings.HasPrefix(h.Shape, "/") {
			t.Errorf("%s: shape is not an absolute path", what)
		}
		if strings.HasSuffix(h.Shape, "/") && h.Shape != "/" {
			t.Errorf("%s: shape has a trailing slash; a prefix is not an href", what)
		}
		if strings.ContainsRune(h.Shape, '%') {
			t.Errorf("%s: shape still contains a fmt verb; wildcards must be %s", what, assembly.HrefWildcard)
		}
		if h.Method != strings.ToUpper(h.Method) || h.Method == "" {
			t.Errorf("%s: method must be a non-empty upper-case verb", what)
		}
		if strings.TrimSpace(h.Mint) == "" {
			t.Errorf("%s: no mint site recorded", what)
		}
		if strings.TrimSpace(h.Why) == "" {
			t.Errorf("%s: no reason recorded for why a client follows it", what)
		}
		if prev, dup := seen[what]; dup {
			t.Errorf("%s declared twice (%s and %s); duplicate keys break the ratchet", what, prev, h.Mint)
		}
		seen[what] = h.Mint
	}
}

// without returns patterns with one entry removed, and whether it was present.
func without(patterns []string, drop string) ([]string, bool) {
	out := make([]string, 0, len(patterns))
	found := false
	for _, p := range patterns {
		if p == drop {
			found = true
			continue
		}
		out = append(out, p)
	}
	return out, found
}
