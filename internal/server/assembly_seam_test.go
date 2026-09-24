// Package server_test: assembly seam tests.
//
// Phase 1 added TestCoreRouterPatternEquivalence, which compared the
// in-tree BuildProtocolRouter against assembly.BuildProtocolRouter and
// confirmed 58 patterns matched. That test is removed in Phase 2: the
// in-tree router is gone, so there is no comparand.
//
// Phase 2 replaces the equivalence test with TestProtocolRouteSurface,
// which pins the 58-route canonical surface against the live
// BuildProtocolRouter (which now delegates unconditionally to
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
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// canonicalProtocolRoutes is the pinned list of 71 SEP2 protocol-listener
// patterns that assembly.BuildProtocolRouter must mount. Confirmed
// identical to the in-tree BuildProtocolRouter output by Phase 1
// (TestCoreRouterPatternEquivalence). Any addition or deletion from this
// set is a wire-level change and must be deliberate.
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
//
// core v0.12.0 added five more routes, verified the same way:
//   - "GET /edev/{id}/der/{derId}" and "PUT /edev/{id}/der/{derId}": the
//     DER instance itself. Every DERList member already carried this
//     href; before this route existed, following it 404'd. PUT is on the
//     SunSpec CTP CORE-014/CORE-016 certified path.
//   - "GET /edev/{id}/fsa/{fsaId}/derp/{derpId}": a DERProgram's own
//     href, so the FSA-to-DERProgramList-to-member link walk CSIP v2.0
//     s5.2.3.1 requires actually resolves. Present in the same v0.12.0
//     assembly.go diff; verified directly against core's source.
//   - "GET /rsps/{rspsId}" and "GET /rsps/{rspsId}/rsp/{rspId}": the
//     ResponseSet and single Response read routes. Every served
//     DERControl now carries a replyTo into this set, alongside a new
//     responseRequired field on DERControl itself; both fields are
//     wire-visible and covered by TestSingleDERControlBytesMatchListMember
//     below.
//
// A later core bump moved the LogEvent function set:
//   - "GET /edev/{id}/log" and "POST /edev/{id}/log" are REMOVED. The
//     WADL declares the list at /edev/{id}/lel (sep_wadl.xml:1358, 2018
//     A.3.5.1), not /log; core served the data at an address no
//     conforming client looked for, and nothing advertised /log at all.
//   - "GET /edev/{id}/lel" and "POST /edev/{id}/lel" (the list, mode M
//     both methods) plus "GET /edev/{id}/lel/{lelId}" and
//     "DELETE /edev/{id}/lel/{lelId}" (the instance, mode M both
//     methods; 2018 A.3.5.2) are ADDED. Net +2 routes: four added, two
//     removed. Verified directly against core's assembly.go at commit
//     bd8d0e3.
//
// Pinning core v0.13.0 (tag dereferences to fecafbe) surfaced two more
// catch-up items, confirmed pre-existing on core main rather than
// introduced by the LogEvent bump above:
//   - "GET /edev/{id}/frp/{frpId}" and "GET /edev/{id}/frq/{frqId}"
//     (FlowReservationResponse and FlowReservationRequest instance
//     routes) and "GET /msg/{msgId}/tm/{tmId}" (TextMessage instance
//     route) are ADDED. core commit c407a1e, "mount the FlowReservation
//     and TextMessage instance routes", mounted these earlier; this
//     repo's canonical set had not caught up.
//
// v0.13.0 itself also mounts PUT and DELETE on the MirrorUsagePoint
// instance:
//   - "PUT /mup/{id}" and "DELETE /mup/{id}" are ADDED. Genuinely new
//     with this bump, not pre-existing drift; confirmed by running
//     TestProtocolRouteSurface against the bumped go.mod and reading the
//     diverges-from-canonical report, not assumed from the core changelog.
//
// Net across both catch-up items: five routes added, none removed. 67 -> 72.
//
// PUT on the DefaultDERControl route is REMOVED (#456): the field is
// utility-set, and the route let a protocol client overwrite its own copy.
// 72 -> 71.
var canonicalProtocolRoutes = []string{
	"DELETE /edev/{id}",
	"DELETE /edev/{id}/lel/{lelId}",
	"DELETE /edev/{id}/sub/{subId}",
	"DELETE /mup/{id}",
	"GET /dc",
	"GET /dcap",
	"GET /edev",
	"GET /edev/{id}",
	"GET /edev/{id}/cfg",
	"GET /edev/{id}/der",
	"GET /edev/{id}/der/{derId}",
	"GET /edev/{id}/der/{derId}/dera",
	"GET /edev/{id}/der/{derId}/dercap",
	"GET /edev/{id}/der/{derId}/derg",
	"GET /edev/{id}/der/{derId}/ders",
	"GET /edev/{id}/dstat",
	"GET /edev/{id}/frp",
	"GET /edev/{id}/frp/{frpId}",
	"GET /edev/{id}/frq",
	"GET /edev/{id}/frq/{frqId}",
	"GET /edev/{id}/fsa",
	"GET /edev/{id}/fsa/{fsaId}",
	"GET /edev/{id}/fsa/{fsaId}/derp",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc",
	"GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc/{dercId}",
	"GET /edev/{id}/lel",
	"GET /edev/{id}/lel/{lelId}",
	"GET /edev/{id}/ps",
	"GET /edev/{id}/rg",
	"GET /edev/{id}/sub",
	"GET /msg",
	"GET /msg/{msgId}",
	"GET /msg/{msgId}/tm",
	"GET /msg/{msgId}/tm/{tmId}",
	"GET /mup",
	"GET /mup/{id}",
	"GET /rsps",
	"GET /rsps/{rspsId}",
	"GET /rsps/{rspsId}/rsp",
	"GET /rsps/{rspsId}/rsp/{rspId}",
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
	"POST /edev/{id}/lel",
	"POST /edev/{id}/sub",
	"POST /msg/{msgId}/tm",
	"POST /mup",
	"POST /mup/{id}",
	"POST /mup/{id}/mr",
	"POST /rsps/{rspsId}/rsp",
	"POST /upt",
	"PUT /edev/{id}",
	"PUT /edev/{id}/cfg",
	"PUT /edev/{id}/der/{derId}",
	"PUT /edev/{id}/der/{derId}/dera",
	"PUT /edev/{id}/der/{derId}/dercap",
	"PUT /edev/{id}/der/{derId}/derg",
	"PUT /edev/{id}/der/{derId}/ders",
	"PUT /edev/{id}/dstat",
	"PUT /edev/{id}/ps",
	"PUT /mup/{id}",
}

// TestProtocolRouteSurface asserts that BuildProtocolRouter (which now
// delegates unconditionally to assembly.BuildProtocolRouter) produces
// exactly the 71 canonical SEP2 protocol routes, sorted, with no
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
	// RegistrationPolicy (core v0.13.0) is the first non-pointer,
	// non-interface field on assembly.Stores: its zero value is a
	// legitimate, fail-closed production state (see the doc comment on
	// Stores.RegistrationPolicy in router.go), not a sign the copy was
	// forgotten. newTestStores() deliberately leaves it at the zero value
	// because that fixture is shared by every test in this package, and a
	// non-nil PIN resolver there would start minting Registrations for
	// every EndDevice every other test creates. Overriding it on this
	// src alone, after newTestStores() returns, keeps that blast radius
	// at zero while still giving THIS test a non-zero value to prove the
	// copy itself works.
	src.RegistrationPolicy = memory.RegistrationPolicy{
		PIN:      func(lfdi string) (uint32, bool) { return 1, true },
		PollRate: 900,
	}
	// newTestStores already wires an empty EndDeviceManagementStore (#440);
	// re-set it here anyway so this test does not silently depend on that
	// default and stays a self-contained proof that the copy works.
	src.EndDeviceManagers = memory.NewEndDeviceManagementStore()
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
			// Scalar and struct-valued fields: RegistrationPolicy today.
			// src sets it to a non-zero value above specifically so this
			// check is meaningful; a zero value here means NewCoreStores
			// dropped the field on the way to the destination struct.
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
	// into SFDIPrefix. A swap here would route #13 guard to the wrong
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
