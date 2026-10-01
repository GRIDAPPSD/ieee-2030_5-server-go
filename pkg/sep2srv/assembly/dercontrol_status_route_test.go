package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// seedIssuedDERControl seeds a DERControl AND its lifecycle record under the
// same (scope, id), the shape only the admin issuer (internal/dercontrol)
// produces. lc is the lifecycle record to store; the control's own
// EventStatus is left nil, since the routes under test derive it.
func seedIssuedDERControl(t *testing.T, stores *assembly.Stores, edevID, dercID string, creationTime, start int64, duration uint32, lc dercontrol.LifecycleRecord) sep2.DERControl {
	t.Helper()

	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{
			OpModTargetW: &sep2.ActivePower{Value: 1000, Multiplier: 0},
		},
	}
	ctrl.Href = "/edev/" + edevID + "/fsa/1/derp/1/derc/" + dercID
	ctrl.MRID = "0123456789ABCDEF0123456789ABCDEF"
	ctrl.CreationTime = creationTime
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: duration}

	scopeKey := derControlScopeKey(edevID, "1", "1")
	if err := stores.DERControls.Create(context.Background(), scopeKey, dercID, ctrl); err != nil {
		t.Fatalf("seed DERControl: %v", err)
	}
	if err := stores.DERControlLifecycles.Create(context.Background(), scopeKey, dercID, lc); err != nil {
		t.Fatalf("seed lifecycle record: %v", err)
	}
	return ctrl
}

func ptrInt64ForTest(v int64) *int64 { return &v }

// TestDERControlRoutesDeriveScheduledBeforeStart asserts acceptance
// criterion 1's Scheduled status: an issued control whose start is in the
// future reads currentStatus 0 with dateTime equal to its creationTime, on
// both the list and the single-resource route.
func TestDERControlRoutesDeriveScheduledBeforeStart(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	creationTime := now - 3600
	start := now + 3600 // one hour in the future: still Scheduled

	stores := testStores()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "sched-0", creationTime, start, 900, dercontrol.LifecycleRecord{})
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single", single.EventStatus, sep2.EventStatusScheduled, creationTime)

	listResp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	var list sep2.DERControlList
	decodeXML(t, listResp, &list)
	if len(list.DERControl) != 1 {
		t.Fatalf("list served %d controls, want 1", len(list.DERControl))
	}
	assertEventStatus(t, "list member", list.DERControl[0].EventStatus, sep2.EventStatusScheduled, creationTime)
}

// TestDERControlRoutesDeriveActiveAfterStart asserts criterion 1's Active
// status: a started, non-cancelled, non-superseded control reads
// currentStatus 1 with dateTime equal to its start.
func TestDERControlRoutesDeriveActiveAfterStart(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	creationTime := now - 7200
	start := now - 3600 // one hour ago: Active

	stores := testStores()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "active-0", creationTime, start, 900, dercontrol.LifecycleRecord{})
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single", single.EventStatus, sep2.EventStatusActive, start)
}

// TestDERControlRoutesDeriveCancelled asserts criterion 1's Cancelled
// status: dateTime equals the recorded cancellation time, regardless of the
// control's own interval.
func TestDERControlRoutesDeriveCancelled(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	cancelledAt := now - 600
	lc := dercontrol.LifecycleRecord{CancelledAt: ptrInt64ForTest(cancelledAt)}

	stores := testStores()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "cancel-0", now-7200, now-3600, 900, lc)
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single", single.EventStatus, sep2.EventStatusCancelled, cancelledAt)
}

// TestDERControlRoutesDeriveSuperseded asserts criterion 1's Superseded
// status: dateTime equals the superseding control's recorded start, once
// that time is reached.
func TestDERControlRoutesDeriveSuperseded(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	supersededAt := now - 600
	lc := dercontrol.LifecycleRecord{SupersededAt: ptrInt64ForTest(supersededAt)}

	stores := testStores()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "super-0", now-7200, now-3600, 900, lc)
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single", single.EventStatus, sep2.EventStatusSuperseded, supersededAt)
}

// TestDERControlWithNoLifecycleRecordServedUnchanged asserts acceptance
// criterion 2: a control with no lifecycle record (a boot-fixture or
// CSIP-loaded control, simulated here by seeding straight into
// stores.DERControls with no matching lifecycle record) serves the
// EventStatus it was stored with, byte for byte, even though
// stores.DERControlLifecycles is wired. The stored status is deliberately
// what real timing would NOT derive (a far-future dateTime while the
// interval is already active), so this test fails if status derivation were
// ever applied to a control regardless of whether it has a record.
func TestDERControlWithNoLifecycleRecordServedUnchanged(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	stores := testStores()

	want := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{
			OpModTargetW: &sep2.ActivePower{Value: 500, Multiplier: 0},
		},
	}
	want.Href = "/edev/" + testLFDI + "/fsa/1/derp/1/derc/fixture-0"
	want.MRID = "FEDCBA9876543210FEDCBA9876543210"
	want.CreationTime = now - 3600
	want.Interval = &sep2.DateTimeInterval{Start: now - 3600, Duration: 900}
	// A status no timing-based derivation would ever produce for this
	// interval (already well past its end): if derivation were wrongly
	// applied to a record-less control it would compute Active with
	// dateTime = start, not this.
	want.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: now + 999999}

	scopeKey := derControlScopeKey(testLFDI, "1", "1")
	if err := stores.DERControls.Create(context.Background(), scopeKey, "fixture-0", want); err != nil {
		t.Fatalf("seed fixture DERControl: %v", err)
	}
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + want.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single (no lifecycle record)", single.EventStatus, want.EventStatus.CurrentStatus, want.EventStatus.DateTime)

	listResp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	var list sep2.DERControlList
	decodeXML(t, listResp, &list)
	if len(list.DERControl) != 1 {
		t.Fatalf("list served %d controls, want 1", len(list.DERControl))
	}
	assertEventStatus(t, "list member (no lifecycle record)", list.DERControl[0].EventStatus, want.EventStatus.CurrentStatus, want.EventStatus.DateTime)
}

// TestSingleDERControlBytesMatchListMemberWithLifecycleRecord extends
// TestSingleDERControlBytesMatchListMember (acceptance criterion 3) to an
// admin-issued control whose EventStatus IS derived, proving the two routes
// derive it identically rather than only proving it for the pass-through
// case.
func TestSingleDERControlBytesMatchListMemberWithLifecycleRecord(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	stores := testStores()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "match-0", now-7200, now-3600, 900, dercontrol.LifecycleRecord{})
	srv := derControlRouter(t, stores)

	singleResp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	var single sep2.DERControl
	decodeXML(t, singleResp, &single)
	if single.EventStatus == nil || single.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Fatalf("single: EventStatus not derived as Active: %+v", single.EventStatus)
	}

	listResp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	var list sep2.DERControlList
	decodeXML(t, listResp, &list)
	if len(list.DERControl) != 1 {
		t.Fatalf("list served %d controls, want 1", len(list.DERControl))
	}

	singleBytes, err := xml.Marshal(&single)
	if err != nil {
		t.Fatalf("marshal single: %v", err)
	}
	memberBytes, err := xml.Marshal(&list.DERControl[0])
	if err != nil {
		t.Fatalf("marshal list member: %v", err)
	}
	if string(singleBytes) != string(memberBytes) {
		t.Errorf("single-resource bytes differ from the list member's bytes\nsingle: %s\nmember: %s", singleBytes, memberBytes)
	}
}

// assertEventStatus is the shared boundary assertion for the tests above.
func assertEventStatus(t *testing.T, label string, got *sep2.EventStatus, wantStatus uint8, wantDateTime int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: EventStatus is nil", label)
	}
	if got.CurrentStatus != wantStatus {
		t.Errorf("%s: CurrentStatus = %d, want %d", label, got.CurrentStatus, wantStatus)
	}
	if got.DateTime != wantDateTime {
		t.Errorf("%s: DateTime = %d, want %d", label, got.DateTime, wantDateTime)
	}
}

// TestDERProgramControlListLinkAllReflectsLiveControls asserts acceptance
// criterion 4: DERControlListLink.all is the live count of controls in the
// scope the link names, on both the DERProgramList and single DERProgram
// routes, and a cancel (which keeps the control stored) does not change it.
func TestDERProgramControlListLinkAllReflectsLiveControls(t *testing.T) {
	t.Parallel()

	stores := testStores()
	prog := sep2.DERProgram{MRID: "AAAA0000AAAA0000AAAA0000AAAA0000"}
	prog.Href = coreder.DERProgramHref(testLFDI, "1", "1")
	prog.DERControlListLink = &sep2.ListLink{
		Href: "/edev/" + testLFDI + "/fsa/1/derp/1/derc",
		All:  99, // a stale stored count; the route must not serve this
	}
	if err := stores.DERPrograms.Create(context.Background(), testLFDI, "1", prog); err != nil {
		t.Fatalf("seed DERProgram: %v", err)
	}
	srv := derControlRouter(t, stores)

	getProgramAll := func(t *testing.T) uint32 {
		t.Helper()
		resp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1")
		if err != nil {
			t.Fatalf("GET single DERProgram: %v", err)
		}
		var got sep2.DERProgram
		decodeXML(t, resp, &got)
		if got.DERControlListLink == nil {
			t.Fatal("DERControlListLink is nil on the served DERProgram")
		}
		return got.DERControlListLink.All
	}
	getListAll := func(t *testing.T) uint32 {
		t.Helper()
		resp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp")
		if err != nil {
			t.Fatalf("GET DERProgramList: %v", err)
		}
		var got sep2.DERProgramList
		decodeXML(t, resp, &got)
		if len(got.DERProgram) != 1 || got.DERProgram[0].DERControlListLink == nil {
			t.Fatalf("DERProgramList did not serve one member with a DERControlListLink: %+v", got.DERProgram)
		}
		return got.DERProgram[0].DERControlListLink.All
	}

	if got := getProgramAll(t); got != 0 {
		t.Errorf("before any control exists: single DERProgram All = %d, want 0", got)
	}
	if got := getListAll(t); got != 0 {
		t.Errorf("before any control exists: DERProgramList All = %d, want 0", got)
	}

	now := time.Now().Unix()
	seedIssuedDERControl(t, stores, testLFDI, "c1", now-100, now-100, 900, dercontrol.LifecycleRecord{})
	seedIssuedDERControl(t, stores, testLFDI, "c2", now-100, now-100, 900, dercontrol.LifecycleRecord{})

	if got := getProgramAll(t); got != 2 {
		t.Errorf("with two controls: single DERProgram All = %d, want 2", got)
	}
	if got := getListAll(t); got != 2 {
		t.Errorf("with two controls: DERProgramList All = %d, want 2", got)
	}

	// Cancel one: it stays stored, so the count must not drop.
	scopeKey := derControlScopeKey(testLFDI, "1", "1")
	cancelledAt := now
	if err := stores.DERControlLifecycles.Update(context.Background(), scopeKey, "c2", dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
		t.Fatalf("cancel c2's lifecycle record: %v", err)
	}

	if got := getProgramAll(t); got != 2 {
		t.Errorf("after cancelling one control: single DERProgram All = %d, want 2 (cancelling does not remove it)", got)
	}
	if got := getListAll(t); got != 2 {
		t.Errorf("after cancelling one control: DERProgramList All = %d, want 2 (cancelling does not remove it)", got)
	}
}

// TestDERControlListTable50Order asserts acceptance criterion 5: admin-issued
// controls with equal starts and different creationTimes appear in IEEE
// 2030.5-2018 Table 50 order (interval.start ascending, creationTime
// descending), i.e. newest creationTime first. It issues through the real
// internal/dercontrol.Issuer rather than hand-building store ids, so the
// test exercises the production id scheme (internal/dercontrol/id.go)
// end to end rather than a re-implementation of it.
func TestDERControlListTable50Order(t *testing.T) {
	t.Parallel()

	stores := testStores()
	prog := sep2.DERProgram{MRID: "BBBB0000BBBB0000BBBB0000BBBB0000"}
	prog.Href = coreder.DERProgramHref(testLFDI, "1", "p1")
	prog.DERControlListLink = &sep2.ListLink{Href: "/edev/" + testLFDI + "/fsa/1/derp/p1/derc"}
	if err := stores.DERPrograms.Create(context.Background(), testLFDI, "p1", prog); err != nil {
		t.Fatalf("seed DERProgram: %v", err)
	}

	pen := uint32(12345)
	issuer, err := dercontrol.NewIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}

	start := time.Now().Unix() + 3600
	// Different control types so the second issue does not supersede the
	// first (computeSupersedes only matches controls of the same shape).
	first, err := issuer.Issue(context.Background(), dercontrol.CreateRequest{
		DERProgramHref: "/edev/" + testLFDI + "/fsa/1/derp/p1", Type: dercontrol.Connect, Start: &start, DurationSeconds: 900,
	})
	if err != nil {
		t.Fatalf("Issue first: %v", err)
	}
	maxLim := uint16(5000)
	second, err := issuer.Issue(context.Background(), dercontrol.CreateRequest{
		DERProgramHref: "/edev/" + testLFDI + "/fsa/1/derp/p1", Type: dercontrol.MaxLimW, MaxLimW: &maxLim, Start: &start, DurationSeconds: 900,
	})
	if err != nil {
		t.Fatalf("Issue second: %v", err)
	}
	if second.Control.CreationTime <= first.Control.CreationTime {
		t.Fatalf("second.CreationTime (%d) is not after first.CreationTime (%d); premise of this test is broken",
			second.Control.CreationTime, first.Control.CreationTime)
	}

	srv := derControlRouter(t, stores)
	resp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/p1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var list sep2.DERControlList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\nbody: %s", err, body)
	}
	if len(list.DERControl) != 2 {
		t.Fatalf("list served %d controls, want 2", len(list.DERControl))
	}
	if list.DERControl[0].Href != second.Href {
		t.Errorf("list[0].Href = %q, want %q (the newer, second-issued control first)", list.DERControl[0].Href, second.Href)
	}
	if list.DERControl[1].Href != first.Href {
		t.Errorf("list[1].Href = %q, want %q (the older, first-issued control second)", list.DERControl[1].Href, first.Href)
	}
}

// TestDERControlRoutesFailClosedOnLifecycleStoreError proves the fix for a
// MEDIUM finding on PR 726 (#564): both the single-resource and the list
// route answer 500 when the lifecycle store errors, rather than serving the
// control with no EventStatus. A stored, pre-issued EventStatus of Active
// is deliberately NOT what a failed-open derivation would ever produce for
// this interval, so this test would also fail if a failed-open path started
// stamping a wrong-but-present status instead of an absent one; the
// assertion is simply "not 200".
func TestDERControlRoutesFailClosedOnLifecycleStoreError(t *testing.T) {
	t.Parallel()

	stores := testStores()
	now := time.Now().Unix()
	ctrl := seedIssuedDERControl(t, stores, testLFDI, "faulty-0", now-7200, now-3600, 900, dercontrol.LifecycleRecord{})

	var fault storetest.Fault
	fault.Arm(storetest.ErrBackendUnavailable)
	stores.DERControlLifecycles = storetest.NewFaultyScopedStore[dercontrol.LifecycleRecord](
		memory.NewScopedStore[dercontrol.LifecycleRecord](), &fault,
	)
	srv := derControlRouter(t, stores)

	singleResp, err := srv.Client().Get(srv.URL + ctrl.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	_ = singleResp.Body.Close()
	if singleResp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET single = %d, want 500 (a broken lifecycle store must fail the request, not serve a control with no EventStatus)", singleResp.StatusCode)
	}

	listResp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	_ = listResp.Body.Close()
	if listResp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET list = %d, want 500 (a broken lifecycle store must fail the request, not serve a control with no EventStatus)", listResp.StatusCode)
	}
}

// TestDERControlRoutesServeUnchangedWhenLifecyclesUnwired asserts a MEDIUM
// finding on PR 726 (#564): with Stores.DERControlLifecycles left absent
// (the ordinary state before an admin issuer is wired), both routes must
// serve a control exactly as stored, not run it through the
// status-derivation decorator at all. The stored status is deliberately
// what real timing would not derive, the same control used by
// TestDERControlWithNoLifecycleRecordServedUnchanged, so a build that wraps
// the decorator unconditionally (the "if !store.IsAbsent(...)" guard in
// assembly.go weakened to always wrap) either serves a recomputed status
// here or panics reaching a nil lifecycle store; this test fails either
// way.
func TestDERControlRoutesServeUnchangedWhenLifecyclesUnwired(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	stores := testStores()
	stores.DERControlLifecycles = nil

	want := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{
			OpModTargetW: &sep2.ActivePower{Value: 500, Multiplier: 0},
		},
	}
	want.Href = "/edev/" + testLFDI + "/fsa/1/derp/1/derc/unwired-0"
	want.MRID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	want.CreationTime = now - 3600
	want.Interval = &sep2.DateTimeInterval{Start: now - 3600, Duration: 900}
	want.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: now + 999999}

	scopeKey := derControlScopeKey(testLFDI, "1", "1")
	if err := stores.DERControls.Create(context.Background(), scopeKey, "unwired-0", want); err != nil {
		t.Fatalf("seed DERControl: %v", err)
	}
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + want.Href)
	if err != nil {
		t.Fatalf("GET single: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET single = %d, want 200", resp.StatusCode)
	}
	var single sep2.DERControl
	decodeXML(t, resp, &single)
	assertEventStatus(t, "single (DERControlLifecycles unwired)", single.EventStatus, want.EventStatus.CurrentStatus, want.EventStatus.DateTime)

	listResp, err := srv.Client().Get(srv.URL + "/edev/" + testLFDI + "/fsa/1/derp/1/derc")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET list = %d, want 200", listResp.StatusCode)
	}
	var list sep2.DERControlList
	decodeXML(t, listResp, &list)
	if len(list.DERControl) != 1 {
		t.Fatalf("list served %d controls, want 1", len(list.DERControl))
	}
	assertEventStatus(t, "list member (DERControlLifecycles unwired)", list.DERControl[0].EventStatus, want.EventStatus.CurrentStatus, want.EventStatus.DateTime)
}
