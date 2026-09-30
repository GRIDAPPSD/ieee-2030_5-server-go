package dercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#714 slice S2: the target power
// control type, the grant link on the lifecycle record, the commitment
// check hook, Relink, and CancelLive.

const linkScopeKey = "dev1/0/p1"

func activePower(value int16, mult int8) *sep2.ActivePower {
	return &sep2.ActivePower{Value: value, Multiplier: mult}
}

func allowCheck(context.Context, Proposal) error { return nil }

func newLinkHarness(t *testing.T) *testHarness {
	t.Helper()
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	return h
}

func targetRequest(start int64, duration uint32, target *sep2.ActivePower, grant string) CreateRequest {
	return CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            TargetW,
		TargetW:         target,
		Start:           &start,
		DurationSeconds: duration,
		ExecutesGrant:   grant,
	}
}

func TestBuildBase_TargetW(t *testing.T) {
	cases := []struct {
		name    string
		req     CreateRequest
		refusal RefusalCode
	}{
		{"missing target", CreateRequest{Type: TargetW}, RefusalMissingValue},
		{"maxLimW set", CreateRequest{Type: TargetW, TargetW: activePower(1, 0), MaxLimW: uint16ptr(1)}, RefusalUnexpectedValue},
		{"power factor set", CreateRequest{Type: TargetW, TargetW: activePower(1, 0), PowerFactor: &PowerFactorValue{Displacement: 1}}, RefusalUnexpectedValue},
		{"multiplier above 9", CreateRequest{Type: TargetW, TargetW: activePower(1, 10)}, RefusalValueOutOfRange},
		{"multiplier below -9", CreateRequest{Type: TargetW, TargetW: activePower(1, -10)}, RefusalValueOutOfRange},
		{"connect carrying a target", CreateRequest{Type: Connect, TargetW: activePower(1, 0)}, RefusalUnexpectedValue},
		{"disconnect carrying a target", CreateRequest{Type: Disconnect, TargetW: activePower(1, 0)}, RefusalUnexpectedValue},
		{"maxLimW carrying a target", CreateRequest{Type: MaxLimW, MaxLimW: uint16ptr(1), TargetW: activePower(1, 0)}, RefusalUnexpectedValue},
		{"fixedPF carrying a target", CreateRequest{Type: FixedPFInjectW, PowerFactor: &PowerFactorValue{Displacement: 1, Excitation: new(bool)}, TargetW: activePower(1, 0)}, RefusalUnexpectedValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildBase(tc.req)
			assertRefusal(t, err, tc.refusal)
		})
	}

	accepted := []*sep2.ActivePower{activePower(-2000, 0), activePower(2000, 0), activePower(0, 0), activePower(32767, 9), activePower(-32768, -9)}
	for _, target := range accepted {
		base, err := buildBase(CreateRequest{Type: TargetW, TargetW: target})
		if err != nil {
			t.Fatalf("buildBase(target %+v) error = %v", *target, err)
		}
		if base.OpModTargetW == nil || *base.OpModTargetW != *target {
			t.Fatalf("OpModTargetW = %v, want %+v", base.OpModTargetW, *target)
		}
		if base.OpModTargetW == target {
			t.Fatal("OpModTargetW aliases the request's value; a later change to the request would rewrite the control")
		}
		want := sep2.DERControlBase{OpModTargetW: base.OpModTargetW}
		if *base != want {
			t.Fatalf("base = %+v, want only OpModTargetW set", *base)
		}
	}
}

func TestControlShape_TargetWSupersedesOnlyTargetW(t *testing.T) {
	h := newLinkHarness(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 1000

	limit, err := h.issuer.Issue(ctx, CreateRequest{DERProgramHref: programHref("dev1", "0", "p1"), Type: MaxLimW, MaxLimW: uint16ptr(5000), Start: &start, DurationSeconds: 600})
	if err != nil {
		t.Fatalf("issue maxLimW: %v", err)
	}
	first, err := h.issuer.Issue(ctx, targetRequest(start, 600, activePower(1000, 0), ""))
	if err != nil {
		t.Fatalf("issue first target: %v", err)
	}
	if len(first.Supersedes) != 0 {
		t.Fatalf("target superseded %v, want nothing: a maxLimW control is a different control set", first.Supersedes)
	}
	second, err := h.issuer.Issue(ctx, targetRequest(start+60, 600, activePower(500, 0), ""))
	if err != nil {
		t.Fatalf("issue second target: %v", err)
	}
	if len(second.Supersedes) != 1 || second.Supersedes[0] != first.Control.MRID {
		t.Fatalf("second target superseded %v, want exactly [%s]", second.Supersedes, first.Control.MRID)
	}
	lc, err := h.lifecycles.Get(ctx, linkScopeKey, limit.ID)
	if err != nil {
		t.Fatalf("load maxLimW lifecycle: %v", err)
	}
	if lc.SupersededAt != nil {
		t.Fatalf("maxLimW SupersededAt = %d, want nil", *lc.SupersededAt)
	}
}

func TestIssueInFleet_StoresLinkOnLifecycleRecord(t *testing.T) {
	cases := []struct {
		name  string
		grant string
	}{
		{"execution", "0123456789ABCDEF0123456789ABCDEF"},
		{"plain dispatch", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newLinkHarness(t)
			start := sep2time.Now().Unix() + 1000
			res, err := h.issuer.IssueInFleet(context.Background(), targetRequest(start, 600, activePower(-2000, 0), tc.grant),
				Fleet{Key: "LFDI-AGG", Reach: 3, Check: allowCheck})
			if err != nil {
				t.Fatalf("IssueInFleet() error = %v", err)
			}
			lc, err := h.lifecycles.Get(context.Background(), linkScopeKey, res.ID)
			if err != nil {
				t.Fatalf("load lifecycle: %v", err)
			}
			want := LifecycleRecord{GrantMRID: tc.grant, FleetKey: "LFDI-AGG", Reach: 3}
			if lc != want {
				t.Fatalf("lifecycle = %+v, want %+v", lc, want)
			}
			stored, err := h.controls.Get(context.Background(), linkScopeKey, res.ID)
			if err != nil {
				t.Fatalf("load control: %v", err)
			}
			if stored.DERControlBase.OpModTargetW == nil || stored.DERControlBase.OpModTargetW.Value != -2000 || stored.DERControlBase.OpModTargetW.Multiplier != 0 {
				t.Fatalf("stored OpModTargetW = %v, want -2000 W", stored.DERControlBase.OpModTargetW)
			}
		})
	}
}

func TestIssue_PlainPathWritesNoLink(t *testing.T) {
	h := newLinkHarness(t)
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})
	lc, err := h.lifecycles.Get(context.Background(), linkScopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle: %v", err)
	}
	if lc != (LifecycleRecord{}) {
		t.Fatalf("lifecycle = %+v, want the zero record", lc)
	}
}

// A link written with no check would carry out a grant nobody bounded, so
// both entry points refuse it before writing.
func TestIssue_GrantLinkWithoutCheckStoresNothing(t *testing.T) {
	start := sep2time.Now().Unix() + 1000
	req := targetRequest(start, 600, activePower(-2000, 0), "0123456789ABCDEF0123456789ABCDEF")
	cases := []struct {
		name  string
		issue func(*Issuer) error
	}{
		{"Issue", func(i *Issuer) error { _, err := i.Issue(context.Background(), req); return err }},
		{"IssueInFleet nil check", func(i *Issuer) error {
			_, err := i.IssueInFleet(context.Background(), req, Fleet{Key: "LFDI-AGG", Reach: 1})
			return err
		}},
		{"IssueInFleet empty fleet key", func(i *Issuer) error {
			_, err := i.IssueInFleet(context.Background(), req, Fleet{Reach: 1, Check: allowCheck})
			return err
		}},
		{"IssueInFleet zero reach", func(i *Issuer) error {
			_, err := i.IssueInFleet(context.Background(), req, Fleet{Key: "LFDI-AGG", Check: allowCheck})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newLinkHarness(t)
			err := tc.issue(h.issuer)
			if !errors.Is(err, ErrUncheckedCommitment) {
				t.Fatalf("error = %v, want ErrUncheckedCommitment", err)
			}
			assertNoNewControl(t, h)
			assertNoLifecycle(t, h)
		})
	}
}

func assertNoLifecycle(t *testing.T, h *testHarness) {
	t.Helper()
	parents, err := h.lifecycles.Parents(context.Background())
	if err != nil {
		t.Fatalf("Parents() error = %v", err)
	}
	for _, p := range parents {
		n, err := h.lifecycles.Count(context.Background(), p)
		if err != nil {
			t.Fatalf("Count(%q) error = %v", p, err)
		}
		if n != 0 {
			t.Fatalf("scope %q holds %d lifecycle records, want 0", p, n)
		}
	}
}

func TestIssueInFleet_CheckReceivesProposal(t *testing.T) {
	h := newLinkHarness(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 1000

	older, err := h.issuer.Issue(ctx, targetRequest(start, 600, activePower(1000, 0), ""))
	if err != nil {
		t.Fatalf("issue overlapping target: %v", err)
	}
	if _, err := h.issuer.Issue(ctx, targetRequest(start+5000, 600, activePower(1000, 0), "")); err != nil {
		t.Fatalf("issue disjoint target: %v", err)
	}

	var got []Proposal
	check := func(ctx context.Context, p Proposal) error {
		n, err := h.controls.Count(ctx, linkScopeKey)
		if err != nil {
			t.Errorf("count controls inside check: %v", err)
		}
		if n != 2 {
			t.Errorf("controls stored when the check runs = %d, want 2: the check must run before the first write", n)
		}
		got = append(got, p)
		return nil
	}
	grant := "0123456789ABCDEF0123456789ABCDEF"
	target := activePower(-1500, 1)
	res, err := h.issuer.IssueInFleet(ctx, targetRequest(start+300, 900, target, grant), Fleet{Key: "LFDI-AGG", Reach: 2, Check: check})
	if err != nil {
		t.Fatalf("IssueInFleet() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("check called %d times, want 1", len(got))
	}
	p := got[0]
	if p.Scope != res.Scope {
		t.Errorf("Scope = %+v, want %+v", p.Scope, res.Scope)
	}
	if p.FleetKey != "LFDI-AGG" || p.Reach != 2 || p.GrantMRID != grant {
		t.Errorf("FleetKey, Reach, GrantMRID = %q, %d, %q; want LFDI-AGG, 2, %s", p.FleetKey, p.Reach, p.GrantMRID, grant)
	}
	if p.Window != (commitment.Window{Start: start + 300, Duration: 900}) {
		t.Errorf("Window = %+v, want {%d 900}", p.Window, start+300)
	}
	if p.TargetW == nil || *p.TargetW != *target {
		t.Errorf("TargetW = %v, want %+v", p.TargetW, *target)
	}
	if len(p.Supersedes) != 1 || p.Supersedes[0] != older.Control.MRID {
		t.Errorf("Supersedes = %v, want exactly [%s]", p.Supersedes, older.Control.MRID)
	}
	if len(res.Supersedes) != 1 || res.Supersedes[0] != older.Control.MRID {
		t.Errorf("Result.Supersedes = %v, want the candidates the check was shown", res.Supersedes)
	}
}

// storedState is every field of every record in both stores, keyed by
// scope and id, so a refusal can be shown to have changed nothing.
type storedState struct {
	controls   map[string]sep2.DERControl
	lifecycles map[string]LifecycleRecord
}

func captureState(t *testing.T, h *testHarness) storedState {
	t.Helper()
	ctx := context.Background()
	s := storedState{controls: map[string]sep2.DERControl{}, lifecycles: map[string]LifecycleRecord{}}
	parents, err := h.controls.Parents(ctx)
	if err != nil {
		t.Fatalf("control Parents: %v", err)
	}
	for _, p := range parents {
		list, err := h.controls.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatalf("list controls: %v", err)
		}
		for _, c := range list.Items {
			s.controls[c.Href] = c
		}
	}
	for href := range s.controls {
		id, ok := derhref.ControlID(href)
		if !ok {
			t.Fatalf("control href %q has no id", href)
		}
		rec, err := h.lifecycles.Get(ctx, linkScopeKey, id)
		if err != nil {
			t.Fatalf("load lifecycle %s: %v", id, err)
		}
		s.lifecycles[id] = rec
	}
	// Every lifecycle record must belong to a control, or a refused call
	// could leave an orphan the map above never reads.
	lparents, err := h.lifecycles.Parents(ctx)
	if err != nil {
		t.Fatalf("lifecycle Parents: %v", err)
	}
	total := 0
	for _, p := range lparents {
		n, err := h.lifecycles.Count(ctx, p)
		if err != nil {
			t.Fatalf("count lifecycles: %v", err)
		}
		total += int(n)
	}
	if total != len(s.lifecycles) {
		t.Fatalf("lifecycle records = %d, controls with a record = %d", total, len(s.lifecycles))
	}
	return s
}

func assertStateUnchanged(t *testing.T, before, after storedState) {
	t.Helper()
	if len(after.controls) != len(before.controls) {
		t.Fatalf("controls stored = %d, want %d", len(after.controls), len(before.controls))
	}
	for href, b := range before.controls {
		a, ok := after.controls[href]
		if !ok {
			t.Fatalf("control %s missing after refusal", href)
		}
		if a.MRID != b.MRID || a.CreationTime != b.CreationTime || a.Description != b.Description {
			t.Errorf("control %s identity = (%s, %d, %q), want (%s, %d, %q)", href, a.MRID, a.CreationTime, a.Description, b.MRID, b.CreationTime, b.Description)
		}
		if *a.Interval != *b.Interval {
			t.Errorf("control %s interval = %+v, want %+v", href, *a.Interval, *b.Interval)
		}
		if (a.DERControlBase.OpModTargetW == nil) != (b.DERControlBase.OpModTargetW == nil) || (a.DERControlBase.OpModTargetW != nil && *a.DERControlBase.OpModTargetW != *b.DERControlBase.OpModTargetW) {
			t.Errorf("control %s OpModTargetW = %v, want %v", href, a.DERControlBase.OpModTargetW, b.DERControlBase.OpModTargetW)
		}
	}
	if len(after.lifecycles) != len(before.lifecycles) {
		t.Fatalf("lifecycle records = %d, want %d", len(after.lifecycles), len(before.lifecycles))
	}
	for key, b := range before.lifecycles {
		a, ok := after.lifecycles[key]
		if !ok {
			t.Fatalf("lifecycle %s missing after refusal", key)
		}
		if !int64PtrEqual(a.CancelledAt, b.CancelledAt) || a.CancelReason != b.CancelReason {
			t.Errorf("lifecycle %s cancel = (%v, %q), want (%v, %q)", key, a.CancelledAt, a.CancelReason, b.CancelledAt, b.CancelReason)
		}
		if !int64PtrEqual(a.SupersededAt, b.SupersededAt) || a.SupersededBy != b.SupersededBy {
			t.Errorf("lifecycle %s supersede = (%v, %q), want (%v, %q)", key, a.SupersededAt, a.SupersededBy, b.SupersededAt, b.SupersededBy)
		}
		if a.GrantMRID != b.GrantMRID || a.FleetKey != b.FleetKey || a.Reach != b.Reach {
			t.Errorf("lifecycle %s link = (%q, %q, %d), want (%q, %q, %d)", key, a.GrantMRID, a.FleetKey, a.Reach, b.GrantMRID, b.FleetKey, b.Reach)
		}
	}
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestIssueInFleet_CheckRefusalStoresNothing(t *testing.T) {
	h := newLinkHarness(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 1000

	// An overlapping same-shape control makes the refused call a supersede
	// in waiting, so a hook placed after the marks would change it.
	older, err := h.issuer.IssueInFleet(ctx, targetRequest(start, 600, activePower(-1000, 0), "AAAA0000AAAA0000AAAA0000AAAA0000"),
		Fleet{Key: "LFDI-AGG", Reach: 1, Check: allowCheck})
	if err != nil {
		t.Fatalf("issue older execution: %v", err)
	}
	before := captureState(t, h)
	if len(before.controls) != 1 || len(before.lifecycles) != 1 {
		t.Fatalf("seeded state = %d controls, %d lifecycles; want 1 and 1", len(before.controls), len(before.lifecycles))
	}

	refusal := &commitment.ConflictError{Code: commitment.ConflictPower, MRID: older.Control.MRID}
	_, err = h.issuer.IssueInFleet(ctx, targetRequest(start+60, 600, activePower(-9000, 0), "AAAA0000AAAA0000AAAA0000AAAA0000"),
		Fleet{Key: "LFDI-AGG", Reach: 1, Check: func(context.Context, Proposal) error { return refusal }})
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict != refusal {
		t.Fatalf("error = %v, want the check's *ConflictError unchanged", err)
	}

	assertStateUnchanged(t, before, captureState(t, h))
}

func TestLifecycleRecord_LinkSurvivesPersistenceRoundTrip(t *testing.T) {
	s, path := newPersistedLifecycleStore(t)
	ctx := context.Background()
	cancelled := int64(1700000000)
	rec := LifecycleRecord{CancelledAt: &cancelled, CancelReason: "stop", GrantMRID: "0123456789ABCDEF0123456789ABCDEF", FleetKey: "LFDI-AGG", Reach: 4}
	if err := s.Create(ctx, linkScopeKey, "c1", rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Create(ctx, linkScopeKey, "c2", LifecycleRecord{}); err != nil {
		t.Fatalf("Create plain: %v", err)
	}

	reloaded, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.Get(ctx, linkScopeKey, "c1")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.GrantMRID != rec.GrantMRID || got.FleetKey != rec.FleetKey || got.Reach != rec.Reach {
		t.Fatalf("link after reload = (%q, %q, %d), want (%q, %q, %d)", got.GrantMRID, got.FleetKey, got.Reach, rec.GrantMRID, rec.FleetKey, rec.Reach)
	}
	if got.CancelledAt == nil || *got.CancelledAt != cancelled || got.CancelReason != "stop" {
		t.Fatalf("cancel after reload = (%v, %q), want (%d, stop)", got.CancelledAt, got.CancelReason, cancelled)
	}

	// A plain record writes no link keys, so a binary that predates them
	// reads the file exactly as before.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var env struct {
		Records []struct {
			ID     string                     `json:"id"`
			Record map[string]json.RawMessage `json:"record"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	seen := 0
	for _, r := range env.Records {
		for _, key := range []string{"GrantMRID", "FleetKey", "Reach"} {
			_, present := r.Record[key]
			if r.ID == "c2" && present {
				t.Errorf("plain record carries key %s on disk, want it omitted", key)
			}
			if r.ID == "c1" && !present {
				t.Errorf("execution record lacks key %s on disk", key)
			}
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("snapshot holds %d records, want 2", seen)
	}
}

func TestLifecycleRecord_VersionOneSnapshotWithoutLinkLoadsAsPlain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dercontrol-lifecycles.json")
	snapshot := `{"version":1,"records":[{"parent":"dev1/0/p1","id":"c1","record":{"CancelledAt":null,"CancelReason":"","SupersededAt":1700000500,"SupersededBy":"BBBB"}}]}`
	if !strings.Contains(snapshot, `"version":1`) || strings.Contains(snapshot, "GrantMRID") {
		t.Fatal("fixture must be a version-1 snapshot without the link fields")
	}
	if err := os.WriteFile(path, []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	s, err := NewLifecycleStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("load version-1 snapshot: %v", err)
	}
	got, err := s.Get(context.Background(), linkScopeKey, "c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GrantMRID != "" || got.FleetKey != "" || got.Reach != 0 {
		t.Fatalf("link = (%q, %q, %d), want empty: a record with no link is a plain dispatch", got.GrantMRID, got.FleetKey, got.Reach)
	}
	if got.SupersededAt == nil || *got.SupersededAt != 1700000500 || got.SupersededBy != "BBBB" {
		t.Fatalf("supersede = (%v, %q), want (1700000500, BBBB)", got.SupersededAt, got.SupersededBy)
	}
}

func TestRelink_ChangesOnlyGrantMRID(t *testing.T) {
	h := newLinkHarness(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 1000
	res, err := h.issuer.IssueInFleet(ctx, targetRequest(start, 600, activePower(-1000, 0), "AAAA0000AAAA0000AAAA0000AAAA0000"),
		Fleet{Key: "LFDI-AGG", Reach: 2, Check: allowCheck})
	if err != nil {
		t.Fatalf("issue execution: %v", err)
	}
	superseded := start + 100
	before, err := h.lifecycles.Get(ctx, linkScopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle: %v", err)
	}
	before.SupersededAt, before.SupersededBy, before.CancelReason = &superseded, "CCCC", "kept"
	if err := h.lifecycles.Update(ctx, linkScopeKey, res.ID, before); err != nil {
		t.Fatalf("seed lifecycle: %v", err)
	}

	got, err := h.issuer.Relink(ctx, res.Scope, res.ID, "BBBB0000BBBB0000BBBB0000BBBB0000")
	if err != nil {
		t.Fatalf("Relink() error = %v", err)
	}
	stored, err := h.lifecycles.Get(ctx, linkScopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle after Relink: %v", err)
	}
	want := before
	want.GrantMRID = "BBBB0000BBBB0000BBBB0000BBBB0000"
	for name, rec := range map[string]LifecycleRecord{"returned": got, "stored": stored} {
		if rec.GrantMRID != want.GrantMRID || rec.FleetKey != want.FleetKey || rec.Reach != want.Reach ||
			rec.CancelReason != want.CancelReason || rec.SupersededBy != want.SupersededBy ||
			!int64PtrEqual(rec.SupersededAt, want.SupersededAt) || !int64PtrEqual(rec.CancelledAt, want.CancelledAt) {
			t.Errorf("%s record = %+v, want %+v", name, rec, want)
		}
	}
}

func TestRelink_Refusals(t *testing.T) {
	ctx := context.Background()
	start := sep2time.Now().Unix() + 1000

	t.Run("unknown control", func(t *testing.T) {
		h := newLinkHarness(t)
		_, err := h.issuer.Relink(ctx, Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}, "missing", "BBBB")
		assertRefusal(t, err, RefusalControlNotFound)
	})

	t.Run("plain dispatch", func(t *testing.T) {
		h := newLinkHarness(t)
		res, err := h.issuer.IssueInFleet(ctx, targetRequest(start, 600, activePower(-1000, 0), ""), Fleet{Key: "LFDI-AGG", Reach: 1, Check: allowCheck})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		before := captureState(t, h)
		if _, err := h.issuer.Relink(ctx, res.Scope, res.ID, "BBBB"); !errors.Is(err, ErrNotExecution) {
			t.Fatalf("Relink() error = %v, want ErrNotExecution", err)
		}
		assertStateUnchanged(t, before, captureState(t, h))
	})

	t.Run("empty grant", func(t *testing.T) {
		h := newLinkHarness(t)
		res, err := h.issuer.IssueInFleet(ctx, targetRequest(start, 600, activePower(-1000, 0), "AAAA"), Fleet{Key: "LFDI-AGG", Reach: 1, Check: allowCheck})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		before := captureState(t, h)
		if _, err := h.issuer.Relink(ctx, res.Scope, res.ID, ""); !errors.Is(err, ErrNotExecution) {
			t.Fatalf("Relink() error = %v, want ErrNotExecution", err)
		}
		assertStateUnchanged(t, before, captureState(t, h))
	})
}

func TestRelink_UpdateFailureRestoresRecord(t *testing.T) {
	errRelink := errors.New("relink store unavailable")
	errRestore := errors.New("restore failed")
	cases := []struct {
		name     string
		failAt   map[int]error
		modeAt   map[int]failMode
		wantUndo bool
	}{
		{"clean", map[int]error{1: errRelink}, nil, false},
		{"applied", map[int]error{1: errRelink}, map[int]failMode{1: failApplied}, false},
		{"applied and restore fails", map[int]error{1: errRelink, 2: errRestore}, map[int]failMode{1: failApplied}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycles := &failingLifecycles{ScopedStore: memory.NewScopedStore[LifecycleRecord](), log: &callLog{}}
			issuer, programs, _ := newFailingLifecycleHarness(t, lifecycles)
			seedWriteOrderProgram(t, programs, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			start := sep2time.Now().Unix() + 1000
			res, err := issuer.IssueInFleet(context.Background(), targetRequest(start, 600, activePower(-1000, 0), "AAAA"),
				Fleet{Key: "LFDI-AGG", Reach: 1, Check: allowCheck})
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			lifecycles.updateFailAt, lifecycles.updateModeAt = tc.failAt, tc.modeAt

			_, err = issuer.Relink(context.Background(), res.Scope, res.ID, "BBBB")
			if !errors.Is(err, errRelink) {
				t.Fatalf("Relink() error = %v, want wrapping %v", err, errRelink)
			}
			var undo *UndoError
			if gotUndo := errors.As(err, &undo); gotUndo != tc.wantUndo {
				t.Fatalf("Relink() error = %v, *UndoError = %v, want %v", err, gotUndo, tc.wantUndo)
			}
			if tc.wantUndo {
				if undo.Step != UndoStepRelink || undo.ID != res.ID || undo.MRID != res.Control.MRID || !undo.ControlKept || !undo.LifecycleKept {
					t.Fatalf("UndoError = %+v, want Step=%q naming %s with both kept", undo, UndoStepRelink, res.Control.MRID)
				}
				return
			}
			got, gerr := lifecycles.Get(context.Background(), linkScopeKey, res.ID)
			if gerr != nil {
				t.Fatalf("load lifecycle: %v", gerr)
			}
			if got.GrantMRID != "AAAA" {
				t.Fatalf("GrantMRID = %q, want AAAA restored", got.GrantMRID)
			}
		})
	}
}

func TestCancelLive(t *testing.T) {
	ctx := context.Background()
	scope := Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}

	t.Run("live control is cancelled", func(t *testing.T) {
		h := newLinkHarness(t)
		res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})
		done, err := h.issuer.CancelLive(ctx, res.Scope, res.ID, "grant cancelled")
		if err != nil || !done {
			t.Fatalf("CancelLive() = (%v, %v), want (true, nil)", done, err)
		}
		lc, err := h.lifecycles.Get(ctx, linkScopeKey, res.ID)
		if err != nil {
			t.Fatalf("load lifecycle: %v", err)
		}
		if lc.CancelledAt == nil || lc.CancelReason != "grant cancelled" {
			t.Fatalf("lifecycle = %+v, want cancelled with the reason", lc)
		}
	})

	past := sep2time.Now().Unix() - 10
	alreadyDone := []struct {
		name string
		mark func(LifecycleRecord) LifecycleRecord
	}{
		{"cancelled", func(r LifecycleRecord) LifecycleRecord { r.CancelledAt = &past; return r }},
		{"superseded", func(r LifecycleRecord) LifecycleRecord { r.SupersededAt, r.SupersededBy = &past, "DDDD"; return r }},
	}
	for _, tc := range alreadyDone {
		t.Run(tc.name+" counts as done", func(t *testing.T) {
			h := newLinkHarness(t)
			res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})
			lc, err := h.lifecycles.Get(ctx, linkScopeKey, res.ID)
			if err != nil {
				t.Fatalf("load lifecycle: %v", err)
			}
			if err := h.lifecycles.Update(ctx, linkScopeKey, res.ID, tc.mark(lc)); err != nil {
				t.Fatalf("seed lifecycle: %v", err)
			}
			before := captureState(t, h)
			done, err := h.issuer.CancelLive(ctx, res.Scope, res.ID, "grant cancelled")
			if err != nil || done {
				t.Fatalf("CancelLive() = (%v, %v), want (false, nil)", done, err)
			}
			assertStateUnchanged(t, before, captureState(t, h))
		})
	}

	t.Run("ended counts as done", func(t *testing.T) {
		h := newLinkHarness(t)
		endedStart := sep2time.Now().Unix() - 1000
		var ctrl sep2.DERControl
		ctrl.Interval = &sep2.DateTimeInterval{Start: endedStart, Duration: 60}
		ctrl.Href = controlListHref("dev1", "0", "p1") + "/old"
		if err := h.controls.Create(ctx, linkScopeKey, "old", ctrl); err != nil {
			t.Fatalf("seed control: %v", err)
		}
		if err := h.lifecycles.Create(ctx, linkScopeKey, "old", LifecycleRecord{}); err != nil {
			t.Fatalf("seed lifecycle: %v", err)
		}
		done, err := h.issuer.CancelLive(ctx, scope, "old", "")
		if err != nil || done {
			t.Fatalf("CancelLive() = (%v, %v), want (false, nil)", done, err)
		}
		lc, err := h.lifecycles.Get(ctx, linkScopeKey, "old")
		if err != nil {
			t.Fatalf("load lifecycle: %v", err)
		}
		if lc.CancelledAt != nil {
			t.Fatalf("CancelledAt = %d, want nil for an ended control", *lc.CancelledAt)
		}
	})

	t.Run("unknown control is an error", func(t *testing.T) {
		h := newLinkHarness(t)
		done, err := h.issuer.CancelLive(ctx, scope, "missing", "")
		if done {
			t.Fatal("CancelLive() done = true for an unknown control")
		}
		assertRefusal(t, err, RefusalControlNotFound)
	})
}
