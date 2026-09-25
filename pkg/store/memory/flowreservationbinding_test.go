package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Flow reservation link derivation, mirroring logeventbinding_test.go.
//
// The router-level assertions live in pkg/sep2srv/assembly, where a served
// EndDevice is inspected on the wire and both constructor arms are exercised
// against the mount gate. What is covered HERE is the derivation itself on
// the read paths a router does not exercise in the happy case, and the
// UNSERVED arm this decorator's sibling does not have.

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

	s := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore())
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
// both links are DERIVED, not stored, on Create and Update.
func TestFlowReservationLinkedEndDeviceStore_DiscardsAClientSuppliedLink(t *testing.T) {
	t.Parallel()

	s := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore())
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
	got, err = s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.FlowReservationRequestListLink == nil || got.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Errorf("after Update, FlowReservationRequestListLink = %v, want href %q: a client must not be able to point "+
			"its own record at another device's list", got.FlowReservationRequestListLink, "/edev/1/frq")
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

	s := memory.NewFlowReservationLinkedEndDeviceStore(inner)
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
