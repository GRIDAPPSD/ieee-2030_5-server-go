package server_test

// TestCoreRouterPatternEquivalence is the Phase 1 proof for IEEESRV-001.
// It calls both the in-tree BuildProtocolRouter and assembly.BuildProtocolRouter
// with identical stores, config, and a nil notifier, then asserts that the
// two sorted pattern lists are set-equal.
//
// A pattern-list difference here would be a real wire-level divergence:
// a route present in one router but absent in the other, which means the
// two implementations are NOT equivalent. This test must stay green until
// Phase 2 (IEEESRV-002) deletes the in-tree router.
//
// Per the workspace data-invariants rule, the test asserts the actual
// pattern VALUES (not just that both calls return without error).
import (
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/assembly"
)

// TestCoreRouterPatternEquivalence calls the in-tree and core routers with
// identical inputs and asserts their pattern lists match exactly.
func TestCoreRouterPatternEquivalence(t *testing.T) {
	t.Parallel()

	svc := newScopeTestCertService(t)
	cfg := &config.Config{AdminKey: "test-admin-key"}
	stores := newTestStores()

	// In-tree router (the live default).
	_, inTreePatterns := server.BuildProtocolRouter(cfg, stores, svc, "test-sfdi", "test-lfdi", nil)

	// Core router (the Phase 1 parallel path).
	coreStores := server.NewCoreStores(stores)
	authPolicy := server.NewCoreAuthPolicy()
	routerCfg := server.NewCoreRouterConfig(cfg)
	_, corePatterns := assembly.BuildProtocolRouter(routerCfg, coreStores, authPolicy, "test-sfdi", "test-lfdi", nil)

	// Both lists must be sorted (the contracts guarantee this). Verify
	// they are so the element-by-element comparison below is valid.
	if !sort.StringsAreSorted(inTreePatterns) {
		t.Fatal("in-tree pattern list is not sorted (contract violated)")
	}
	if !sort.StringsAreSorted(corePatterns) {
		t.Fatal("core pattern list is not sorted (contract violated)")
	}

	// Build lookup maps for clear differential reporting.
	inTreeSet := make(map[string]struct{}, len(inTreePatterns))
	for _, p := range inTreePatterns {
		inTreeSet[p] = struct{}{}
	}
	coreSet := make(map[string]struct{}, len(corePatterns))
	for _, p := range corePatterns {
		coreSet[p] = struct{}{}
	}

	// Patterns in in-tree but missing from core are wire-level regressions:
	// the core router would fail to serve those routes.
	var missingFromCore []string
	for p := range inTreeSet {
		if _, ok := coreSet[p]; !ok {
			missingFromCore = append(missingFromCore, p)
		}
	}
	// Patterns in core but absent from in-tree are unexpected additions.
	var extraInCore []string
	for p := range coreSet {
		if _, ok := inTreeSet[p]; !ok {
			extraInCore = append(extraInCore, p)
		}
	}

	if len(missingFromCore) > 0 || len(extraInCore) > 0 {
		sort.Strings(missingFromCore)
		sort.Strings(extraInCore)
		t.Errorf(
			"router pattern lists diverge (WIRE-LEVEL MISMATCH):\n"+
				"  missing from core (%d): %s\n"+
				"  extra in core (%d):     %s\n"+
				"--- in-tree (%d patterns) ---\n%s\n"+
				"--- core (%d patterns) ---\n%s",
			len(missingFromCore), strings.Join(missingFromCore, ", "),
			len(extraInCore), strings.Join(extraInCore, ", "),
			len(inTreePatterns), strings.Join(inTreePatterns, "\n"),
			len(corePatterns), strings.Join(corePatterns, "\n"),
		)
	}

	// Also assert the count matches for a fast diagnostic.
	if len(inTreePatterns) != len(corePatterns) {
		t.Errorf("pattern count: in-tree=%d core=%d (set diff above identifies specifics)",
			len(inTreePatterns), len(corePatterns))
	}

	// Log the full matched list at verbose level so `go test -v` shows
	// the actual pattern values for review.
	t.Logf("equivalence confirmed: %d patterns", len(inTreePatterns))
	for _, p := range inTreePatterns {
		t.Logf("  %s", p)
	}
}
