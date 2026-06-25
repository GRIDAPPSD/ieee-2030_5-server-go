// Package server_test: assembly seam tests for IEEESRV-001.
//
// TestCoreRouterPatternEquivalence is the Phase 1 proof: both routers
// produce the same sorted pattern list (58 patterns, confirmed identical).
//
// TestNewCoreRouterConfig pins the five-field mapping from *config.Config
// to assembly.RouterConfig so a transposed field is caught immediately.
//
// TestNewCoreAuthPolicyIdentity pins the Identity closure field order and
// asserts Wrap and SFDIPrefix are non-nil.
//
// TestCoreRouterEnabled pins the toggle logic for CoreRouterEnabled().
//
// TestSelectRouterToggle exercises SelectRouter() under both env states,
// confirming a non-nil handler and a non-empty pattern list are returned
// regardless of which router is selected. This also exercises adaptNotifier
// and notifierAdapter so no symbols are unused.
package server_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
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

// TestNewCoreRouterConfig asserts that each of the five scalar fields maps
// to the correct destination field in assembly.RouterConfig. Distinct
// non-zero values are used so a transposed assignment (e.g. TZOffset
// written to DSTOffset) produces a test failure rather than passing on
// zero-value coincidence.
func TestNewCoreRouterConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TZOffset:    -28800, // UTC-8 in seconds
		DSTOffset:   3600,   // +1 hour DST
		DSTStart:    1711868400,
		DSTEnd:      1730617200,
		TimeQuality: 7,
	}

	got := server.NewCoreRouterConfig(cfg)

	if got.TZOffset != cfg.TZOffset {
		t.Errorf("TZOffset: got %d, want %d", got.TZOffset, cfg.TZOffset)
	}
	if got.DSTOffset != cfg.DSTOffset {
		t.Errorf("DSTOffset: got %d, want %d", got.DSTOffset, cfg.DSTOffset)
	}
	if got.DSTStart != cfg.DSTStart {
		t.Errorf("DSTStart: got %d, want %d", got.DSTStart, cfg.DSTStart)
	}
	if got.DSTEnd != cfg.DSTEnd {
		t.Errorf("DSTEnd: got %d, want %d", got.DSTEnd, cfg.DSTEnd)
	}
	if got.TimeQuality != cfg.TimeQuality {
		t.Errorf("TimeQuality: got %d, want %d", got.TimeQuality, cfg.TimeQuality)
	}
}

// TestNewCoreStoresCopiesAllFields asserts that every field in the
// assembly.Stores destination is non-nil after NewCoreStores. Reflection is
// used so a field added in Phase 2 that the copy forgets will fail this test
// rather than nil-panicking at request time. All fields in assembly.Stores are
// pointers or interfaces, so nil is the only dangerous zero value here.
func TestNewCoreStoresCopiesAllFields(t *testing.T) {
	t.Parallel()

	src := newTestStores()
	dst := server.NewCoreStores(src)

	if dst == nil {
		t.Fatal("NewCoreStores returned nil for a non-nil source")
	}

	dstVal := reflect.ValueOf(dst).Elem()
	dstType := dstVal.Type()
	for i := 0; i < dstVal.NumField(); i++ {
		f := dstVal.Field(i)
		name := dstType.Field(i).Name
		switch f.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			if f.IsNil() {
				t.Errorf("assembly.Stores.%s is nil after NewCoreStores: field was not copied", name)
			}
		default:
			// Scalar fields (int, bool, string, etc.) are not expected in
			// assembly.Stores today, but if one appears and the copy is missing
			// we catch the zero value here.
			if f.IsZero() {
				t.Errorf("assembly.Stores.%s is zero after NewCoreStores: field was not copied", name)
			}
		}
	}
}

// TestNewCoreAuthPolicyIdentity asserts that the Identity closure returns
// (lfdi, sfdi, true) in the correct field order when the context carries a
// known DeviceIdentity, and that Wrap and SFDIPrefix are non-nil.
//
// A full request-level integration test (real authenticated request through
// the core router asserting middleware, ACL execution, and notifier fan-out)
// is deferred to Phase 2 (IEEESRV-002), where the core router becomes the
// live one and end-to-end integration tests cover the wired path.
func TestNewCoreAuthPolicyIdentity(t *testing.T) {
	t.Parallel()

	const wantLFDI = "aabbcc001122ddeeff"
	const wantSFDI = "112233445566"

	policy := server.NewCoreAuthPolicy()

	// Wrap and SFDIPrefix must be non-nil: a nil Wrap disables ALL ACL
	// enforcement (core logs a warning but does not fail); a nil SFDIPrefix
	// causes 500 on the edev POST path (fail-closed per assembly contract).
	if policy.Wrap == nil {
		t.Error("AuthPolicy.Wrap is nil: no ACL enforcement would be applied")
	}
	if policy.SFDIPrefix == nil {
		t.Error("AuthPolicy.SFDIPrefix is nil: edev POST would return 500")
	}

	// Inject a known DeviceIdentity into a context using the exported key so
	// the Identity closure can retrieve it. The key type (auth.contextKey) is
	// unexported, but auth.IdentityContextKey() provides the value.
	ctx := context.WithValue(
		context.Background(),
		auth.IdentityContextKey(),
		auth.DeviceIdentity{LFDI: wantLFDI, SFDI: wantSFDI},
	)

	gotLFDI, gotSFDI, ok := policy.Identity(ctx)

	if !ok {
		t.Fatal("Identity returned ok=false; expected ok=true for a context with DeviceIdentity")
	}
	// Field order is load-bearing: assembly.AuthPolicy.Identity returns
	// (lfdi, sfdi, ok). The edev POST path feeds the second return (SFDI)
	// into SFDIPrefix. A swap here would route IEEE-014 guard to the wrong
	// value silently.
	if gotLFDI != wantLFDI {
		t.Errorf("Identity first return (lfdi): got %q, want %q", gotLFDI, wantLFDI)
	}
	if gotSFDI != wantSFDI {
		t.Errorf("Identity second return (sfdi): got %q, want %q", gotSFDI, wantSFDI)
	}

	// Also verify the empty-context path returns ok=false (no panic).
	_, _, okEmpty := policy.Identity(context.Background())
	if okEmpty {
		t.Error("Identity returned ok=true on an empty context; expected ok=false")
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
// asserts: (1) handler is non-nil, (2) the pattern list is non-empty,
// (3) a representative set of canonical patterns is present. This
// exercises SelectRouter, adaptNotifier, and notifierAdapter so they are
// not unused symbols. The exact count is owned by TestCoreRouterPatternEquivalence.
// Subtests must not be parallel because t.Setenv is incompatible with t.Parallel.
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
			if len(patterns) == 0 {
				t.Errorf("pattern list is empty; SelectRouter returned no routes")
			}
			if !sort.StringsAreSorted(patterns) {
				t.Error("pattern list is not sorted")
			}
			patternSet := make(map[string]struct{}, len(patterns))
			for _, p := range patterns {
				patternSet[p] = struct{}{}
			}
			for _, want := range canonicalPatterns {
				if _, ok := patternSet[want]; !ok {
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
