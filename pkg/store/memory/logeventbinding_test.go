package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// LogEventListLink derivation.
//
// The router-level assertions live in pkg/sep2srv/assembly, where a served
// EndDevice is inspected on the wire. What is covered HERE is the derivation
// itself on the read paths a router does not exercise in the happy case: the
// list, the SFDI and LFDI lookups, and the malformed-href fail-closed branch.
// Those paths hand back a device without its store key, so they recover the key
// from the device's own Href, and a wrong recovery there advertises one device's
// list on another device's record.

func seedDevice(t *testing.T, s *memory.LogEventLinkedEndDeviceStore, id, sfdi, lfdi string) {
	t.Helper()
	dev := sep2.EndDevice{SFDI: sfdi, LFDI: lfdi}
	dev.Href = "/edev/" + id
	if err := s.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("create %q: %v", id, err)
	}
}

// TestLogEventLinkedEndDeviceStore_EveryReadPathDerivesTheLink covers all four
// read paths against TWO devices.
//
// One device cannot distinguish a correct per-device derivation from a
// derivation that returns the same constant href for everyone: with a single
// device both produce an identical result, and a client reading the wrong
// device's list would look exactly like a client reading the right one.
func TestLogEventLinkedEndDeviceStore_EveryReadPathDerivesTheLink(t *testing.T) {
	t.Parallel()

	s := memory.NewLogEventLinkedEndDeviceStore(memory.NewEndDeviceStore())
	seedDevice(t, s, "1", "1111111111", "AAAA")
	seedDevice(t, s, "2", "2222222222", "BBBB")

	ctx := context.Background()

	assertLink := func(path string, dev sep2.EndDevice, wantKey string) {
		t.Helper()
		want := "/edev/" + wantKey + "/lel"
		if dev.LogEventListLink == nil {
			t.Errorf("%s: LogEventListLink is nil, want href %q", path, want)
			return
		}
		if dev.LogEventListLink.Href != want {
			t.Errorf("%s: LogEventListLink href = %q, want %q", path, dev.LogEventListLink.Href, want)
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
		assertLink("Get("+tc.key+")", got, tc.key)

		got, err = s.GetBySFDI(ctx, tc.sfdi)
		if err != nil {
			t.Fatalf("GetBySFDI(%q): %v", tc.sfdi, err)
		}
		assertLink("GetBySFDI("+tc.sfdi+")", got, tc.key)

		got, err = s.GetByLFDI(ctx, tc.lfdi)
		if err != nil {
			t.Fatalf("GetByLFDI(%q): %v", tc.lfdi, err)
		}
		assertLink("GetByLFDI("+tc.lfdi+")", got, tc.key)
	}

	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("List returned %d devices, want 2", len(result.Items))
	}
	for _, dev := range result.Items {
		// The key is recovered from the device's own Href on this path, so
		// deriving it from the href being asserted would be circular. Assert
		// against the SFDI, which is stored data the derivation never reads.
		wantKey := map[string]string{"1111111111": "1", "2222222222": "2"}[dev.SFDI]
		if wantKey == "" {
			t.Fatalf("List returned an unexpected device with SFDI %q", dev.SFDI)
		}
		assertLink("List(sfdi="+dev.SFDI+")", dev, wantKey)
	}
}

// TestLogEventLinkedEndDeviceStore_DiscardsAClientSuppliedLink asserts the link
// is DERIVED, not stored. The link states where this server serves the list; a
// value a client PUT into its own record would otherwise be served back to
// every other reader, pointing them at an address this server does not answer.
func TestLogEventLinkedEndDeviceStore_DiscardsAClientSuppliedLink(t *testing.T) {
	t.Parallel()

	s := memory.NewLogEventLinkedEndDeviceStore(memory.NewEndDeviceStore())
	ctx := context.Background()

	forged := sep2.EndDevice{
		SFDI:             "1111111111",
		LogEventListLink: &sep2.ListLink{Href: "http://attacker.example/lel"},
	}
	forged.Href = "/edev/1"
	if err := s.Create(ctx, "1", forged); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LogEventListLink == nil || got.LogEventListLink.Href != "/edev/1/lel" {
		t.Fatalf("after Create, LogEventListLink = %v, want href %q", got.LogEventListLink, "/edev/1/lel")
	}

	forged.LogEventListLink = &sep2.ListLink{Href: "/edev/2/lel"}
	if err := s.Update(ctx, "1", forged); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.LogEventListLink == nil || got.LogEventListLink.Href != "/edev/1/lel" {
		t.Errorf("after Update, LogEventListLink = %v, want href %q: a client must not be able to point "+
			"its own record at another device's list", got.LogEventListLink, "/edev/1/lel")
	}
}

// TestLogEventLinkedEndDeviceStore_MalformedHrefStripsTheLink pins the
// fail-closed direction on the paths that recover the key from the href.
//
// An href that does not follow the /edev/{key} addressing invariant yields no
// key, and the derivation then STRIPS the link rather than guessing one. An
// unadvertised list is recoverable by a client re-reading the device under its
// canonical href; a link derived from a malformed href would point at a list
// this server does not serve, which is the whole defect class.
func TestLogEventLinkedEndDeviceStore_MalformedHrefStripsTheLink(t *testing.T) {
	t.Parallel()

	inner := memory.NewEndDeviceStore()
	ctx := context.Background()

	// Written through the UNDECORATED store so the malformed href survives:
	// the decorator's own Create would have overwritten the link, but not the
	// href, and the read paths are what is under test.
	for _, href := range []string{"", "/edev/", "/edev/1/extra", "edev/1"} {
		dev := sep2.EndDevice{SFDI: "sfdi" + href, LFDI: "lfdi" + href}
		dev.Href = href
		if err := inner.Create(ctx, "k"+href, dev); err != nil {
			t.Fatalf("seed %q: %v", href, err)
		}
	}

	s := memory.NewLogEventLinkedEndDeviceStore(inner)
	result, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("List returned %d devices, want 4", len(result.Items))
	}
	for _, dev := range result.Items {
		if dev.LogEventListLink != nil {
			t.Errorf("device with href %q was served LogEventListLink %q; a key that cannot be recovered "+
				"must strip the link, not guess one", dev.Href, dev.LogEventListLink.Href)
		}
	}
}

// TestNewLogEventLinkedEndDeviceStore_RejectsANilStore asserts the mis-wiring
// fails at construction, which happens once when the server is assembled, and
// not at request time inside net/http's per-request recover where it would
// surface as a silent 500 for every caller.
func TestNewLogEventLinkedEndDeviceStore_RejectsANilStore(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("a nil decorated store must panic at construction")
		}
	}()
	memory.NewLogEventLinkedEndDeviceStore(nil)
}
