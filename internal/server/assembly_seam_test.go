// Package server_test: assembly seam tests for IEEESRV-001 and IEEESRV-002.
//
// Phase 1 (IEEESRV-001) added TestCoreRouterPatternEquivalence, which compared
// the in-tree BuildProtocolRouter against assembly.BuildProtocolRouter and
// confirmed 58 patterns matched. That test is removed in Phase 2: the in-tree
// router is gone, so there is no comparand.
//
// Phase 2 (IEEESRV-002) replaces the equivalence test with
// TestProtocolRouteSurface, which pins the 58-route canonical surface against
// the live BuildProtocolRouter (which now delegates unconditionally to
// assembly.BuildProtocolRouter). Route-surface coverage is preserved: any
// addition or deletion from the 58 canonical patterns fails this test.
//
// TestNewCoreRouterConfig pins the five-field mapping from *config.Config
// to assembly.RouterConfig so a transposed field is caught immediately.
//
// TestNewCoreAuthPolicyIdentity pins the Identity closure field order and
// asserts Wrap and SFDIPrefix are non-nil.
//
// TestNewCoreStoresCopiesAllFields asserts every field in assembly.Stores
// is non-nil after NewCoreStores. Reflection-driven so a new field added
// to assembly.Stores that NewCoreStores forgets fails here rather than
// nil-panicking at request time.
package server_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// canonicalProtocolRoutes is the pinned list of 60 SEP2 protocol-listener
// patterns that assembly.BuildProtocolRouter must mount. Confirmed
// identical to the in-tree BuildProtocolRouter output by Phase 1
// (IEEESRV-001 TestCoreRouterPatternEquivalence). Any addition or
// deletion from this set is a wire-level change and must be deliberate.
//
// core v0.10.0 (the ieee-2030_5-core-go bump that closed the four-release
// gap since v0.6.0) added two routes, verified against
// pkg/sep2srv/assembly/assembly.go before pinning here:
//   - "GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc/{dercId}": a
//     per-DERControl item GET. Core's comment there explains the CSIP gap
//     it closes: a client polling a single control by id 404s without
//     this route and tears the event down, capping delivery at one event.
//   - "POST /mup/{id}": wired to the same coremetering.HandlePostMirrorMeterReading
//     handler as the existing "POST /mup/{id}/mr", alongside core's
//     MirrorUsagePoint rework (MirrorMeterReadingListLink replaced by an
//     inline MirrorMeterReading list). test/csip/basic_029_meter_reading_test.go
//     and internal/handler/mirror_test.go still need the matching consumer
//     update for that rework; tracked as a follow-up, out of scope for this
//     route-surface pin.
var canonicalProtocolRoutes = []string{
	"DELETE /edev/{id}",
	"DELETE /edev/{id}/sub/{subId}",
	"GET /dc",
	"GET /dcap",
	"GET /edev",
	"GET /edev/{id}",
	"GET /edev/{id}/cfg",
	"GET /edev/{id}/der",
	"GET /edev/{id}/der/{derId}/dera",
	"GET /edev/{id}/der/{derId}/dercap",
	"GET /edev/{id}/der/{derId}/derg",
	"GET /edev/{id}/der/{derId}/ders",
	"GET /edev/{id}/dstat",
	"GET /edev/{id}/frp",
	"GET /edev/{id}/frq",
	"GET /edev/{id}/fsa",
	"GET /edev/{id}/fsa/{fsaId}",
	"GET /edev/{id}/fsa/{fsaId}/derp",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc/{dercId}",
	"GET /edev/{id}/log",
	"GET /edev/{id}/ps",
	"GET /edev/{id}/rg",
	"GET /edev/{id}/sub",
	"GET /msg",
	"GET /msg/{msgId}",
	"GET /msg/{msgId}/tm",
	"GET /mup",
	"GET /mup/{id}",
	"GET /rsps",
	"GET /rsps/{rspsId}/rsp",
	"GET /rt",
	"GET /rt/{id}",
	"GET /sdev",
	"GET /sdev/sdi",
	"GET /tm",
	"GET /upt",
	"GET /upt/{uptId}",
	"GET /upt/{uptId}/mr",
	"GET /upt/{uptId}/mr/{mrId}/r",
	"POST /edev",
	"POST /edev/{id}/frq",
	"POST /edev/{id}/log",
	"POST /edev/{id}/sub",
	"POST /msg/{msgId}/tm",
	"POST /mup",
	"POST /mup/{id}",
	"POST /mup/{id}/mr",
	"POST /rsps/{rspsId}/rsp",
	"POST /upt",
	"PUT /edev/{id}",
	"PUT /edev/{id}/cfg",
	"PUT /edev/{id}/der/{derId}/dera",
	"PUT /edev/{id}/der/{derId}/dercap",
	"PUT /edev/{id}/der/{derId}/derg",
	"PUT /edev/{id}/der/{derId}/ders",
	"PUT /edev/{id}/dstat",
	"PUT /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
	"PUT /edev/{id}/ps",
}

// TestProtocolRouteSurface asserts that BuildProtocolRouter (which now
// delegates unconditionally to assembly.BuildProtocolRouter) produces
// exactly the 58 canonical SEP2 protocol routes, sorted, with no
// additions or deletions. This replaces TestCoreRouterPatternEquivalence
// from Phase 1: there is no longer an in-tree router to compare against,
// so we pin the live surface directly.
func TestProtocolRouteSurface(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	stores := newTestStores()

	_, got := server.BuildProtocolRouter(cfg, stores, nil, "test-sfdi", "test-lfdi", nil)

	if !sort.StringsAreSorted(got) {
		t.Fatal("BuildProtocolRouter returned an unsorted pattern list (contract violated)")
	}
	if !sort.StringsAreSorted(canonicalProtocolRoutes) {
		t.Fatal("canonicalProtocolRoutes is not sorted (test bug)")
	}

	gotSet := make(map[string]struct{}, len(got))
	for _, p := range got {
		gotSet[p] = struct{}{}
	}
	wantSet := make(map[string]struct{}, len(canonicalProtocolRoutes))
	for _, p := range canonicalProtocolRoutes {
		wantSet[p] = struct{}{}
	}

	var missing []string
	for p := range wantSet {
		if _, ok := gotSet[p]; !ok {
			missing = append(missing, p)
		}
	}
	var extra []string
	for p := range gotSet {
		if _, ok := wantSet[p]; !ok {
			extra = append(extra, p)
		}
	}

	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Errorf(
			"protocol route surface diverges from canonical set:\n"+
				"  missing (%d): %s\n"+
				"  extra (%d):   %s\n"+
				"--- got (%d patterns) ---\n%s",
			len(missing), strings.Join(missing, ", "),
			len(extra), strings.Join(extra, ", "),
			len(got), strings.Join(got, "\n"),
		)
	}

	if len(got) != len(canonicalProtocolRoutes) {
		t.Errorf("pattern count: got=%d want=%d", len(got), len(canonicalProtocolRoutes))
	}

	t.Logf("protocol route surface confirmed: %d patterns", len(got))
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
// used so a field added in a future phase that the copy forgets will fail this
// test rather than nil-panicking at request time. All fields in assembly.Stores
// are pointers or interfaces, so nil is the only dangerous zero value here.
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
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
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
