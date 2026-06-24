// Package server_test: assembly seam tests for IEEESRV-001.
//
// TestCoreRouterPatternEquivalence is the Phase 1 proof: both routers
// produce the same sorted pattern list (58 patterns, confirmed identical).
//
// TestCoreRouterEnabled pins the toggle logic for coreRouterEnabled().
//
// TestSelectRouterToggle exercises selectRouter() under both env states,
// confirming a non-nil handler and the expected 58-pattern list are
// returned regardless of which router is selected. This also exercises
// adaptNotifier and notifierAdapter so no symbols are unused.
package server_test

import (
	"context"
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

// TestCoreRouterEnabled pins the toggle logic via t.Setenv. Subtests must
// not be parallel because t.Setenv is incompatible with t.Parallel.
func TestCoreRouterEnabled(t *testing.T) {
	cases := []struct {
		envVal string
		want   bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"", false},
		{"0", false},
		{"no", false},
		{"false", false},
		{"garbage", false},
		{"TRUE", false}, // case-sensitive by design: only lowercase accepted
		{"YES", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("SEP2_USE_CORE_ROUTER="+tc.envVal, func(t *testing.T) {
			t.Setenv("SEP2_USE_CORE_ROUTER", tc.envVal)
			if got := server.CoreRouterEnabled(); got != tc.want {
				t.Errorf("CoreRouterEnabled() = %v, want %v (env=%q)", got, tc.want, tc.envVal)
			}
		})
	}
}

// TestSelectRouterToggle calls SelectRouter under both env states and
// asserts: (1) handler is non-nil, (2) pattern count is 58 (the proven
// equivalent set), (3) a representative set of canonical patterns is
// present. This exercises SelectRouter, adaptNotifier, and
// notifierAdapter so they are not unused symbols. Subtests must not be
// parallel because t.Setenv is incompatible with t.Parallel.
func TestSelectRouterToggle(t *testing.T) {
	svc := newScopeTestCertService(t)
	cfg := &config.Config{AdminKey: "test-admin-key"}
	stores := newTestStores()

	// Canonical patterns that must be present under BOTH router selections.
	canonicalPatterns := []string{
		"GET /dcap",
		"GET /tm",
		"GET /edev",
		"POST /edev",
		"DELETE /edev/{id}",
		"GET /edev/{id}/rg",
		"GET /edev/{id}/sub",
		"POST /mup",
	}

	// nop is a no-op ResourceNotifier so adaptNotifier and notifierAdapter
	// are exercised through the non-nil notifier path.
	nop := &nopNotifier{}

	for _, tc := range []struct {
		name   string
		envVal string
	}{
		{"in-tree (default)", ""},
		{"core (SEP2_USE_CORE_ROUTER=1)", "1"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEP2_USE_CORE_ROUTER", tc.envVal)

			h, patterns := server.SelectRouter(cfg, stores, svc, "test-sfdi", "test-lfdi", nop)

			if h == nil {
				t.Fatal("selectRouter returned nil handler")
			}
			if len(patterns) != 58 {
				t.Errorf("pattern count = %d, want 58\n--- patterns ---\n%s",
					len(patterns), strings.Join(patterns, "\n"))
			}
			if !sort.StringsAreSorted(patterns) {
				t.Error("pattern list is not sorted")
			}
			for _, want := range canonicalPatterns {
				found := false
				for _, p := range patterns {
					if p == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("canonical pattern %q missing from pattern list", want)
				}
			}
		})
	}
}

// nopNotifier satisfies handler.ResourceNotifier with a no-op Notify so
// TestSelectRouterToggle can pass a non-nil notifier and exercise the
// adaptNotifier/notifierAdapter path without importing handler directly.
type nopNotifier struct{}

func (n *nopNotifier) Notify(_ context.Context, _ string, _ uint8) {}
