package memory_test

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// Flow reservation link derivation, mirroring logeventbinding_test.go.
//
// The router-level assertions live in pkg/sep2srv/assembly, where a served
// EndDevice is inspected on the wire and both constructor arms are exercised
// against the mount gate. What is covered HERE is the derivation itself on
// the read paths a router does not exercise in the happy case, and the
// UNSERVED arm this decorator's sibling does not have.

// newFlowReservationScopedStores returns a fresh, empty pair for the served
// arm's constructor. Tests that need to inspect what got cascaded keep their
// own reference to one or both returned stores.
func newFlowReservationScopedStores() (store.ScopedStore[sep2.FlowReservationRequest], store.ScopedStore[sep2.FlowReservationResponse]) {
	return memory.NewScopedStore[sep2.FlowReservationRequest](), memory.NewScopedStore[sep2.FlowReservationResponse]()
}

func seedFlowReservationDevice(t *testing.T, s *memory.FlowReservationLinkedEndDeviceStore, id, sfdi, lfdi string) {
	t.Helper()
	dev := sep2.EndDevice{SFDI: sfdi, LFDI: lfdi}
	dev.Href = "/edev/" + id
	if err := s.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("create %q: %v", id, err)
	}
}

// TestFlowReservationLinkedEndDeviceStore_EveryReadPathDerivesTheLinks covers
// all four read paths against TWO devices on the served arm. One device
// cannot distinguish a correct per-device derivation from a derivation that
// returns the same constant hrefs for everyone.
func TestFlowReservationLinkedEndDeviceStore_EveryReadPathDerivesTheLinks(t *testing.T) {
	t.Parallel()

	reqs, resps := newFlowReservationScopedStores()
	s := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore(), reqs, resps)
	seedFlowReservationDevice(t, s, "1", "1111111111", "AAAA")
	seedFlowReservationDevice(t, s, "2", "2222222222", "BBBB")

	ctx := context.Background()

	assertLinks := func(path string, dev sep2.EndDevice, wantKey string) {
		t.Helper()
		wantReq, wantResp := "/edev/"+wantKey+"/frq", "/edev/"+wantKey+"/frp"
		if dev.FlowReservationRequestListLink == nil || dev.FlowReservationRequestListLink.Href != wantReq {
			t.Errorf("%s: FlowReservationRequestListLink = %v, want href %q", path, dev.FlowReservationRequestListLink, wantReq)
		}
		if dev.FlowReservationResponseListLink == nil || dev.FlowReservationResponseListLink.Href != wantResp {
			t.Errorf("%s: FlowReservationResponseListLink = %v, want href %q", path, dev.FlowReservationResponseListLink, wantResp)
		}
	}

	for _, tc := range []struct{ key, sfdi, lfdi string }{
		{"1", "1111111111", "AAAA"},
		{"2", "2222222222", "BBBB"},
	} {
		got, err := s.Get(ctx, tc.key)
		if err != nil {
			t.Fatalf("Get(%q): %v", tc.key, err)
		}
		assertLinks("Get("+tc.key+")", got, tc.key)

		got, err = s.GetBySFDI(ctx, tc.sfdi)
		if err != nil {
			t.Fatalf("GetBySFDI(%q): %v", tc.sfdi, err)
		}
		assertLinks("GetBySFDI("+tc.sfdi+")", got, tc.key)

		got, err = s.GetByLFDI(ctx, tc.lfdi)
		if err != nil {
			t.Fatalf("GetByLFDI(%q): %v", tc.lfdi, err)
		}
		assertLinks("GetByLFDI("+tc.lfdi+")", got, tc.key)
	}

	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("List returned %d devices, want 2", len(result.Items))
	}
	for _, dev := range result.Items {
		wantKey := map[string]string{"1111111111": "1", "2222222222": "2"}[dev.SFDI]
		if wantKey == "" {
			t.Fatalf("List returned an unexpected device with SFDI %q", dev.SFDI)
		}
		assertLinks("List(sfdi="+dev.SFDI+")", dev, wantKey)
	}
}

// TestFlowReservationLinkedEndDeviceStore_DiscardsAClientSuppliedLink asserts
// both links are DERIVED and PERSISTED on Create and Update: the record held
// in the undecorated inner store carries the derived href, not a client's
// forged one, rather than merely reading correctly back through the
// decorator (data-invariants.md rule 1: assert the persisted value, the way
// a consumer of the inner store reaches it).
func TestFlowReservationLinkedEndDeviceStore_DiscardsAClientSuppliedLink(t *testing.T) {
	t.Parallel()

	inner := memory.NewEndDeviceStore()
	reqs, resps := newFlowReservationScopedStores()
	s := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	ctx := context.Background()

	forged := sep2.EndDevice{
		SFDI:                            "1111111111",
		FlowReservationRequestListLink:  &sep2.ListLink{Href: "http://attacker.example/frq"},
		FlowReservationResponseListLink: &sep2.ListLink{Href: "http://attacker.example/frp"},
	}
	forged.Href = "/edev/1"
	if err := s.Create(ctx, "1", forged); err != nil {
		t.Fatalf("create: %v", err)
	}

	persisted, err := inner.Get(ctx, "1")
	if err != nil {
		t.Fatalf("read persisted record after Create: %v", err)
	}
	if persisted.FlowReservationRequestListLink == nil || persisted.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Fatalf("persisted record after Create: FlowReservationRequestListLink = %v, want href %q",
			persisted.FlowReservationRequestListLink, "/edev/1/frq")
	}
	if persisted.FlowReservationResponseListLink == nil || persisted.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Fatalf("persisted record after Create: FlowReservationResponseListLink = %v, want href %q",
			persisted.FlowReservationResponseListLink, "/edev/1/frp")
	}

	got, err := s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FlowReservationRequestListLink == nil || got.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Fatalf("after Create, FlowReservationRequestListLink = %v, want href %q", got.FlowReservationRequestListLink, "/edev/1/frq")
	}
	if got.FlowReservationResponseListLink == nil || got.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Fatalf("after Create, FlowReservationResponseListLink = %v, want href %q", got.FlowReservationResponseListLink, "/edev/1/frp")
	}

	forged.FlowReservationRequestListLink = &sep2.ListLink{Href: "/edev/2/frq"}
	forged.FlowReservationResponseListLink = &sep2.ListLink{Href: "/edev/2/frp"}
	if err := s.Update(ctx, "1", forged); err != nil {
		t.Fatalf("update: %v", err)
	}

	persisted, err = inner.Get(ctx, "1")
	if err != nil {
		t.Fatalf("read persisted record after Update: %v", err)
	}
	if persisted.FlowReservationRequestListLink == nil || persisted.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Errorf("persisted record after Update: FlowReservationRequestListLink = %v, want href %q: a client must not be able to "+
			"point its own record's PERSISTED link at another device's list", persisted.FlowReservationRequestListLink, "/edev/1/frq")
	}
	if persisted.FlowReservationResponseListLink == nil || persisted.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Errorf("persisted record after Update: FlowReservationResponseListLink = %v, want href %q",
			persisted.FlowReservationResponseListLink, "/edev/1/frp")
	}

	got, err = s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.FlowReservationRequestListLink == nil || got.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Errorf("after Update, FlowReservationRequestListLink = %v, want href %q", got.FlowReservationRequestListLink, "/edev/1/frq")
	}
	if got.FlowReservationResponseListLink == nil || got.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Errorf("after Update, FlowReservationResponseListLink = %v, want href %q", got.FlowReservationResponseListLink, "/edev/1/frp")
	}
}

// TestFlowReservationLinkedEndDeviceStore_MalformedHrefStripsTheLinks pins the
// fail-closed direction on the paths that recover the key from the href.
func TestFlowReservationLinkedEndDeviceStore_MalformedHrefStripsTheLinks(t *testing.T) {
	t.Parallel()

	inner := memory.NewEndDeviceStore()
	ctx := context.Background()

	// Written through the UNDECORATED store so the malformed href survives.
	for _, href := range []string{"", "/edev/", "/edev/1/extra", "edev/1"} {
		dev := sep2.EndDevice{SFDI: "sfdi" + href, LFDI: "lfdi" + href}
		dev.Href = href
		if err := inner.Create(ctx, "k"+href, dev); err != nil {
			t.Fatalf("seed %q: %v", href, err)
		}
	}

	reqs, resps := newFlowReservationScopedStores()
	s := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("List returned %d devices, want 4", len(result.Items))
	}
	for _, dev := range result.Items {
		if dev.FlowReservationRequestListLink != nil || dev.FlowReservationResponseListLink != nil {
			t.Errorf("device with href %q was served links %v / %v; a key that cannot be recovered "+
				"must strip both links, not guess one", dev.Href, dev.FlowReservationRequestListLink, dev.FlowReservationResponseListLink)
		}
	}
}

// TestFlowReservationLinkedEndDeviceStore_MalformedHrefWithALinkPresentLogs
// exercises the log branch in deriveByHref that
// TestFlowReservationLinkedEndDeviceStore_MalformedHrefStripsTheLinks cannot:
// that test's fixtures all seed nil links, so the guard behind the log is
// never true. Here the malformed-href record already carries a link (as an
// older write path, or a direct store write, could leave one), so the strip
// has something to discard.
func TestFlowReservationLinkedEndDeviceStore_MalformedHrefWithALinkPresentLogs(t *testing.T) {
	t.Parallel()

	inner := memory.NewEndDeviceStore()
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "9999999999", LFDI: "FEED"}
	dev.Href = "/edev/1/extra" // malformed: keyFromEndDeviceHref rejects it
	dev.FlowReservationRequestListLink = &sep2.ListLink{Href: "/edev/1/frq"}
	dev.FlowReservationResponseListLink = &sep2.ListLink{Href: "/edev/1/frp"}
	if err := inner.Create(ctx, "k1", dev); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reqs, resps := newFlowReservationScopedStores()
	s := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List returned %d devices, want 1", len(result.Items))
	}
	got := result.Items[0]
	if got.FlowReservationRequestListLink != nil || got.FlowReservationResponseListLink != nil {
		t.Errorf("malformed href with a preexisting link: got req=%v resp=%v, want both nil",
			got.FlowReservationRequestListLink, got.FlowReservationResponseListLink)
	}
}

// TestFlowReservationUnservedEndDeviceStore_StripsBothLinksEverywhere is the
// unit-level pin for the departure from the sibling pattern: the unserved
// arm clears both links on every path, including a value a client supplied,
// because it stays in the chain rather than being omitted when the function
// set is not mounted.
func TestFlowReservationUnservedEndDeviceStore_StripsBothLinksEverywhere(t *testing.T) {
	t.Parallel()

	s := memory.NewFlowReservationUnservedEndDeviceStore(memory.NewEndDeviceStore())
	ctx := context.Background()

	forged := sep2.EndDevice{
		SFDI:                            "1111111111",
		LFDI:                            "AAAA",
		FlowReservationRequestListLink:  &sep2.ListLink{Href: "/evil/frq"},
		FlowReservationResponseListLink: &sep2.ListLink{Href: "/evil/frp"},
	}
	forged.Href = "/edev/1"
	if err := s.Create(ctx, "1", forged); err != nil {
		t.Fatalf("create: %v", err)
	}

	assertBothNil := func(path string, dev sep2.EndDevice) {
		t.Helper()
		if dev.FlowReservationRequestListLink != nil || dev.FlowReservationResponseListLink != nil {
			t.Errorf("%s: links = %v / %v, want both nil on the unserved arm",
				path, dev.FlowReservationRequestListLink, dev.FlowReservationResponseListLink)
		}
	}

	got, err := s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertBothNil("Get", got)

	got, err = s.GetBySFDI(ctx, "1111111111")
	if err != nil {
		t.Fatalf("GetBySFDI: %v", err)
	}
	assertBothNil("GetBySFDI", got)

	got, err = s.GetByLFDI(ctx, "AAAA")
	if err != nil {
		t.Fatalf("GetByLFDI: %v", err)
	}
	assertBothNil("GetByLFDI", got)

	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List returned %d devices, want 1", len(result.Items))
	}
	assertBothNil("List", result.Items[0])

	forged.FlowReservationRequestListLink = &sep2.ListLink{Href: "/evil2/frq"}
	forged.FlowReservationResponseListLink = &sep2.ListLink{Href: "/evil2/frp"}
	if err := s.Update(ctx, "1", forged); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	assertBothNil("Get after Update", got)
}

// TestFlowReservationLinkedEndDeviceStore_DeleteCascadesRequestsAndResponses
// pins GRIDAPPSD/ieee-2030_5-server-go#701: a device's flow reservation
// request and response records must not survive its own deletion, or a later
// device created at the same key inherits them.
func TestFlowReservationLinkedEndDeviceStore_DeleteCascadesRequestsAndResponses(t *testing.T) {
	t.Parallel()

	reqs, resps := newFlowReservationScopedStores()
	s := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore(), reqs, resps)
	ctx := context.Background()

	seedFlowReservationDevice(t, s, "1", "1111111111", "AAAA")
	if err := reqs.Create(ctx, "1", "req-1", sep2.FlowReservationRequest{MRID: "req-1"}); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if err := resps.Create(ctx, "1", "resp-1", sep2.FlowReservationResponse{Subject: "req-1"}); err != nil {
		t.Fatalf("seed response: %v", err)
	}

	// Control: prove the counts below can be non-zero before asserting they
	// are zero after, so a passing assertion means the cascade ran rather
	// than the seed above silently doing nothing.
	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: requests under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: responses under %q = %d, %v, want 1, nil", "1", n, err)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if n, err := reqs.Count(ctx, "1"); err != nil || n != 0 {
		t.Errorf("requests under the dead key %q = %d, %v, want 0, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 0 {
		t.Errorf("responses under the dead key %q = %d, %v, want 0, nil", "1", n, err)
	}
	if has, err := reqs.HasParent(ctx, "1"); err != nil || has {
		t.Errorf("HasParent(%q) on requests = %v, %v, want false, nil: the bucket must not linger for a reused key", "1", has, err)
	}
	if has, err := resps.HasParent(ctx, "1"); err != nil || has {
		t.Errorf("HasParent(%q) on responses = %v, %v, want false, nil", "1", has, err)
	}
}

// TestFlowReservationLinkedEndDeviceStore_DeleteFailsClosedWhenCascadeFails
// asserts the device survives when its records cannot be cascaded, rather
// than being deleted with the cascade half-done.
func TestFlowReservationLinkedEndDeviceStore_DeleteFailsClosedWhenCascadeFails(t *testing.T) {
	t.Parallel()

	fault := &storetest.Fault{}
	fault.Arm(storetest.ErrBackendUnavailable)
	reqs := storetest.NewFaultyScopedStore[sep2.FlowReservationRequest](memory.NewScopedStore[sep2.FlowReservationRequest](), fault)
	_, resps := newFlowReservationScopedStores()

	inner := memory.NewEndDeviceStore()
	s := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	ctx := context.Background()
	seedFlowReservationDevice(t, s, "1", "1111111111", "AAAA")

	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while the request store could not cascade; want an error and the device left in place")
	}

	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(%q) = %v, want the device still present", "1", err)
	}
}

// TestFlowReservationLinkedEndDeviceStore_DeleteLeavesRequestsUntouchedWhenResponsesCascadeFails
// pins GRIDAPPSD/ieee-2030_5-server-go#701: a layer that cannot cascade
// (responses, here) is refused before any other layer removes anything.
// Requests cascade cleanly on their own; before the probe-first
// restructuring, they were already gone by the time the responses cascade
// failed.
func TestFlowReservationLinkedEndDeviceStore_DeleteLeavesRequestsUntouchedWhenResponsesCascadeFails(t *testing.T) {
	t.Parallel()

	reqs := memory.NewScopedStore[sep2.FlowReservationRequest]()
	fault := &storetest.Fault{}
	resps := storetest.NewFaultyScopedStore[sep2.FlowReservationResponse](memory.NewScopedStore[sep2.FlowReservationResponse](), fault)

	inner := memory.NewEndDeviceStore()
	s := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	ctx := context.Background()
	seedFlowReservationDevice(t, s, "1", "1111111111", "AAAA")
	if err := reqs.Create(ctx, "1", "req-1", sep2.FlowReservationRequest{MRID: "req-1"}); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	// Control: the request exists, and responses is not yet armed, so a
	// delete right now would succeed; the fault below is what must make the
	// whole delete fail.
	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: requests under %q = %d, %v, want 1, nil", "1", n, err)
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while the response store could not cascade; want an error and everything left in place")
	}

	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("requests under %q = %d, %v, want 1, nil: a request cascade that ran before the failing response "+
			"cascade must not survive a failed DELETE", "1", n, err)
	}
	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(%q) = %v, want the device still present", "1", err)
	}
}

// TestFlowReservationUnservedEndDeviceStore_DeleteCascadesWhatTheInnerLayerOwns
// asserts the unserved arm still delegates a successful Delete through to
// whatever the inner layer owns, even though it owns no flow reservation
// records of its own. It kills the mutant that turns `if s.served` into
// `if true`: under that mutation this arm's nil reqs/resps make every
// Delete fail, including this one, which the test's want-no-error assertion
// catches.
func TestFlowReservationUnservedEndDeviceStore_DeleteCascadesWhatTheInnerLayerOwns(t *testing.T) {
	t.Parallel()

	events := memory.NewScopedStore[sep2.LogEvent]()
	inner := memory.NewEndDeviceStore()
	logStore := memory.NewLogEventLinkedEndDeviceStore(inner, events)
	s := memory.NewFlowReservationUnservedEndDeviceStore(logStore)
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := events.Create(ctx, "1", "evt-1", sep2.LogEvent{LogEventID: 1}); err != nil {
		t.Fatalf("seed log event: %v", err)
	}

	if n, err := events.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: log events under %q = %d, %v, want 1, nil", "1", n, err)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete on the unserved arm: %v, want success: the arm owns no flow reservation records "+
			"of its own but must still delegate the inner layer's cascade", err)
	}

	if n, err := events.Count(ctx, "1"); err != nil || n != 0 {
		t.Errorf("log events under the dead key %q = %d, %v, want 0, nil: the unserved arm must still "+
			"delegate to the inner LogEvent cascade", "1", n, err)
	}
}

// hrefRecords seeds one request and one response under edev whose hrefs end
// in the store key, the shape this server writes, and returns what it stored.
func hrefRecords(t *testing.T, reqs store.ScopedStore[sep2.FlowReservationRequest], resps store.ScopedStore[sep2.FlowReservationResponse], edev, key string) (sep2.FlowReservationRequest, sep2.FlowReservationResponse) {
	t.Helper()
	ctx := context.Background()
	frq := sep2.FlowReservationRequest{MRID: "MRID-" + key, Description: "request " + key, CreationTime: 100}
	frq.Href = "/edev/" + edev + "/frq/" + key
	frq.IntervalRequested = &sep2.DateTimeInterval{Start: 5000, Duration: 600}
	frp := sep2.FlowReservationResponse{Subject: "MRID-" + key}
	frp.Href = "/edev/" + edev + "/frp/" + key
	frp.MRID = "GRANT-" + key
	frp.Interval = &sep2.DateTimeInterval{Start: 5000, Duration: 600}
	if err := reqs.Create(ctx, edev, key, frq); err != nil {
		t.Fatal(err)
	}
	if err := resps.Create(ctx, edev, key, frp); err != nil {
		t.Fatal(err)
	}
	return frq, frp
}

// sabotageSnapshot makes the next write to path fail while leaving the
// store's memory state alone: the atomic write renames a temp file over
// path, which cannot replace a non-empty directory.
func sabotageSnapshot(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestFlowReservationLinkedEndDeviceStore_DeleteRestoresRequestsWhenOnlyTheResponsesSnapshotFails
// pins the cascade-order trap of #761: requests cascade first, so a
// responses snapshot failure used to leave them deleted and persisted while
// the device stayed.
func TestFlowReservationLinkedEndDeviceStore_DeleteRestoresRequestsWhenOnlyTheResponsesSnapshotFails(t *testing.T) {
	ctx := context.Background()
	reqs, resps, reqPath, respPath := newPersistentFlowStores(t)
	inner := memory.NewEndDeviceStore()
	devs := memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := devs.Create(ctx, "1", dev); err != nil {
		t.Fatal(err)
	}
	wantFrq, wantFrp := hrefRecords(t, reqs, resps, "1", "frq-1")

	sabotageSnapshot(t, respPath)
	// Control: the sabotage makes a responses write fail and a requests write
	// succeed, so only the responses snapshot is broken.
	if err := resps.Create(ctx, "9", "probe", sep2.FlowReservationResponse{}); err == nil {
		t.Fatal("control: a responses write succeeded after the sabotage")
	}
	if n, _ := resps.Count(ctx, "9"); n != 0 {
		t.Fatalf("control: failed probe write left %d records", n)
	}

	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded although the responses snapshot cannot be written")
	}

	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device after failed delete: %v, want still present", err)
	}
	gotFrq, err := reqs.Get(ctx, "1", "frq-1")
	if err != nil || !reflect.DeepEqual(gotFrq, wantFrq) {
		t.Errorf("request in memory = %+v, %v, want %+v", gotFrq, err, wantFrq)
	}
	gotFrp, err := resps.Get(ctx, "1", "frq-1")
	if err != nil || !reflect.DeepEqual(gotFrp, wantFrp) {
		t.Errorf("response in memory = %+v, %v, want %+v", gotFrp, err, wantFrp)
	}
	reloaded, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](reqPath, "frq")
	if err != nil {
		t.Fatal(err)
	}
	gotFrq, err = reloaded.Get(ctx, "1", "frq-1")
	if err != nil || !reflect.DeepEqual(gotFrq, wantFrq) {
		t.Errorf("request after reload = %+v, %v, want %+v: the failed delete left it deleted on disk", gotFrq, err, wantFrq)
	}
}

// failingDeleteDevices is an EndDeviceStore whose Delete fails after the
// cascade has already removed everything under the key. onDelete, when set,
// runs first.
type failingDeleteDevices struct {
	store.EndDeviceStore
	onDelete func()
}

func (f failingDeleteDevices) Delete(context.Context, string) error {
	if f.onDelete != nil {
		f.onDelete()
	}
	return errors.New("device snapshot failed")
}

// cascadeRig is persistent requests, responses and response-dependent
// "lifecycle" records (any record type keyed like the response will do), with
// the response store extended over the lifecycles.
type cascadeRig struct {
	reqs                      *memory.PersistentScopedStore[sep2.FlowReservationRequest]
	resps                     *memory.PersistentScopedStore[sep2.FlowReservationResponse]
	lcs                       *memory.PersistentScopedStore[storetest.Resource]
	reqPath, respPath, lcPath string
	wantFrq                   sep2.FlowReservationRequest
	wantFrp                   sep2.FlowReservationResponse
	wantLc                    storetest.Resource
}

func newCascadeRig(t *testing.T) *cascadeRig {
	t.Helper()
	r := &cascadeRig{}
	r.reqs, r.resps, r.reqPath, r.respPath = newPersistentFlowStores(t)
	r.lcPath = filepath.Join(filepath.Dir(r.reqPath), "lc.json")
	var err error
	if r.lcs, err = memory.NewPersistentScopedStore[storetest.Resource](r.lcPath, "lc"); err != nil {
		t.Fatal(err)
	}
	r.wantFrq, r.wantFrp = hrefRecords(t, r.reqs, r.resps, "1", "frq-1")
	r.wantLc = storetest.Resource{ID: "frq-1", Body: "cancelled", Tags: []string{"mark"}}
	if err := r.lcs.Create(context.Background(), "1", "frq-1", r.wantLc); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *cascadeRig) devices(inner store.EndDeviceStore) *memory.FlowReservationLinkedEndDeviceStore {
	return memory.NewFlowReservationLinkedEndDeviceStore(inner, r.reqs, memory.WithDependents(store.ScopedStore[sep2.FlowReservationResponse](r.resps), store.ScopedStore[storetest.Resource](r.lcs)))
}

// requireOnDisk reloads all three snapshots and requires the original records.
func (r *cascadeRig) requireOnDisk(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	reqs, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](r.reqPath, "frq")
	if err != nil {
		t.Fatal(err)
	}
	resps, err := memory.NewPersistentScopedStore[sep2.FlowReservationResponse](r.respPath, "frp")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reqs.Get(ctx, "1", "frq-1"); err != nil || !reflect.DeepEqual(got, r.wantFrq) {
		t.Errorf("request after reload = %+v, %v, want %+v", got, err, r.wantFrq)
	}
	if got, err := resps.Get(ctx, "1", "frq-1"); err != nil || !reflect.DeepEqual(got, r.wantFrp) {
		t.Errorf("response after reload = %+v, %v, want %+v", got, err, r.wantFrp)
	}
	// The lifecycle file may have been replaced by a sabotage, so it is read
	// from memory unless it is still a file.
	if fi, err := os.Stat(r.lcPath); err == nil && !fi.IsDir() {
		lcs, err := memory.NewPersistentScopedStore[storetest.Resource](r.lcPath, "lc")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := lcs.Get(ctx, "1", "frq-1"); err != nil || !reflect.DeepEqual(got, r.wantLc) {
			t.Errorf("lifecycle after reload = %+v, %v, want %+v", got, err, r.wantLc)
		}
	}
	if got, err := r.lcs.Get(ctx, "1", "frq-1"); err != nil || !reflect.DeepEqual(got, r.wantLc) {
		t.Errorf("lifecycle in memory = %+v, %v, want %+v", got, err, r.wantLc)
	}
}

func TestFlowReservationLinkedEndDeviceStore_DeleteRemovesDependentsWithTheResponses(t *testing.T) {
	ctx := context.Background()
	r := newCascadeRig(t)
	inner := memory.NewEndDeviceStore()
	devs := r.devices(inner)
	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := devs.Create(ctx, "1", dev); err != nil {
		t.Fatal(err)
	}
	if err := devs.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for name, count := range map[string]func() (uint32, error){
		"requests":   func() (uint32, error) { return r.reqs.Count(ctx, "1") },
		"responses":  func() (uint32, error) { return r.resps.Count(ctx, "1") },
		"lifecycles": func() (uint32, error) { return r.lcs.Count(ctx, "1") },
	} {
		if n, err := count(); err != nil || n != 0 {
			t.Errorf("%s under the dead key = %d, %v, want 0", name, n, err)
		}
	}
	lcs, err := memory.NewPersistentScopedStore[storetest.Resource](r.lcPath, "lc")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := lcs.Count(ctx, "1"); n != 0 {
		t.Errorf("lifecycles after reload = %d, want 0", n)
	}
}

// A failure partway through the cascade, in its last collection, restores the
// earlier ones.
func TestFlowReservationLinkedEndDeviceStore_DeleteUndoesRequestsAndResponsesWhenDependentsFail(t *testing.T) {
	ctx := context.Background()
	r := newCascadeRig(t)
	inner := memory.NewEndDeviceStore()
	devs := r.devices(inner)
	sabotageSnapshot(t, r.lcPath)
	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded although the lifecycle snapshot cannot be written")
	}
	r.requireOnDisk(t)
}

// The device removal failing after every collection cascaded restores the
// cancel marks too, so a cancelled grant does not come back live.
func TestFlowReservationLinkedEndDeviceStore_DeleteRestoresDependentsWhenTheDeviceRemovalFails(t *testing.T) {
	ctx := context.Background()
	r := newCascadeRig(t)
	devs := r.devices(failingDeleteDevices{EndDeviceStore: memory.NewEndDeviceStore()})
	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded although the device removal failed")
	}
	r.requireOnDisk(t)
}

// orderedTaker is a scoped store whose TakeParent records the order of
// undos and the state of the context each undo runs under.
type orderedTaker[T store.Copier[T]] struct {
	store.ScopedStore[T]
	name   string
	log    *[]string
	ctxErr *error
}

func (o orderedTaker[T]) DeleteParent(ctx context.Context, p string) (uint32, error) {
	return o.ScopedStore.(interface {
		DeleteParent(context.Context, string) (uint32, error)
	}).DeleteParent(ctx, p)
}

func (o orderedTaker[T]) TakeParent(context.Context, string) (func(context.Context) error, error) {
	return func(ctx context.Context) error {
		*o.log = append(*o.log, o.name)
		*o.ctxErr = errors.Join(*o.ctxErr, ctx.Err())
		return nil
	}, nil
}

func TestFlowReservationLinkedEndDeviceStore_DeleteUndoesInReverseOrderUnderAnUncancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var order []string
	var ctxErr error
	reqs := orderedTaker[sep2.FlowReservationRequest]{memory.NewScopedStore[sep2.FlowReservationRequest](), "requests", &order, &ctxErr}
	resps := orderedTaker[sep2.FlowReservationResponse]{memory.NewScopedStore[sep2.FlowReservationResponse](), "responses", &order, &ctxErr}
	devs := memory.NewFlowReservationLinkedEndDeviceStore(failingDeleteDevices{EndDeviceStore: memory.NewEndDeviceStore(), onDelete: cancel}, reqs, resps)

	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded although the device removal failed")
	}
	if !reflect.DeepEqual(order, []string{"responses", "requests"}) {
		t.Errorf("undo order = %v, want [responses requests]", order)
	}
	if ctxErr != nil {
		t.Errorf("an undo ran under a cancelled context: %v", ctxErr)
	}
}

// A record whose href names a different id than its store key is restored
// under the store key.
func TestFlowReservationLinkedEndDeviceStore_DeleteRestoresUnderTheStoreKeyNotTheHref(t *testing.T) {
	ctx := context.Background()
	reqs, resps, reqPath, _ := newPersistentFlowStores(t)
	devs := memory.NewFlowReservationLinkedEndDeviceStore(failingDeleteDevices{EndDeviceStore: memory.NewEndDeviceStore()}, reqs, resps)
	frq := sep2.FlowReservationRequest{MRID: "M"}
	frq.Href = "/edev/1/frq/other"
	if err := reqs.Create(ctx, "1", "frq-1", frq); err != nil {
		t.Fatal(err)
	}
	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded although the device removal failed")
	}
	reloaded, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](reqPath, "frq")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reloaded.Get(ctx, "1", "frq-1"); err != nil || !reflect.DeepEqual(got, frq) {
		t.Errorf("request under its store key after reload = %+v, %v, want %+v", got, err, frq)
	}
	if _, err := reloaded.Get(ctx, "1", "other"); err == nil {
		t.Error("request was restored under the key its href names")
	}
}

// An undo that cannot restore a record logs which record it lost.
func TestPersistentScopedStore_TakeParentUndoLogsWhatItCouldNotRestore(t *testing.T) {
	ctx := context.Background()
	reqs, _, reqPath, _ := newPersistentFlowStores(t)
	frq := sep2.FlowReservationRequest{MRID: "M"}
	frq.Href = "/edev/1/frq/frq-7"
	if err := reqs.Create(ctx, "1", "frq-7", frq); err != nil {
		t.Fatal(err)
	}
	undo, err := reqs.TakeParent(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := reqs.Count(ctx, "1"); n != 0 {
		t.Fatalf("TakeParent left %d records", n)
	}
	sabotageSnapshot(t, reqPath)
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := undo(ctx); err == nil {
		t.Fatal("undo succeeded although the snapshot cannot be written")
	}
	if !strings.Contains(buf.String(), "1/frq-7") {
		t.Errorf("log %q does not name the lost record 1/frq-7", buf.String())
	}
}

func TestPersistentScopedStore_KeysAreTheStoredIdsAscending(t *testing.T) {
	ctx := context.Background()
	reqs, _, _, _ := newPersistentFlowStores(t)
	for _, k := range []string{"b", "a", "c"} {
		if err := reqs.Create(ctx, "1", k, sep2.FlowReservationRequest{MRID: k}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := reqs.Keys(ctx, "1"); err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Keys = %v, %v, want [a b c]", got, err)
	}
	if got, err := reqs.Keys(ctx, "none"); err != nil || len(got) != 0 {
		t.Errorf("Keys of an unknown parent = %v, %v, want none", got, err)
	}
}
