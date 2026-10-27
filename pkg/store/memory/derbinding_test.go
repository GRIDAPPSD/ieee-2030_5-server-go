package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The wire-level walk from EndDevice to DERStatus is asserted in
// pkg/sep2srv/assembly. Covered here: every read path derives the link per
// device, a client-supplied link is not served, Create seeds exactly one DER
// and leaves a device that has its own alone, and the boot pass seeds devices
// that were stored without going through the decorator.

func newDERLinked(t *testing.T) (*memory.DERLinkedEndDeviceStore, *memory.EndDeviceStore, store.ScopedStore[sep2.DER]) {
	t.Helper()
	devs := memory.NewEndDeviceStore()
	ders := memory.NewScopedStore[sep2.DER]()
	return memory.NewDERLinkedEndDeviceStore(devs, ders), devs, ders
}

func createDevice(t *testing.T, s store.EndDeviceStore, id, sfdi, lfdi string) {
	t.Helper()
	dev := sep2.EndDevice{SFDI: sfdi, LFDI: lfdi}
	dev.Href = "/edev/" + id
	dev.DERListLink = &sep2.ListLink{Href: "/elsewhere", All: 9}
	if err := s.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("create %q: %v", id, err)
	}
}

func TestDERLinkedEndDeviceStore_EveryReadPathDerivesTheLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, ders := newDERLinked(t)
	createDevice(t, s, "1", "1111111111", "AAAA")
	createDevice(t, s, "2", "2222222222", "BBBB")

	// A second DER under device 2 only, so all distinguishes the devices.
	extra := sep2.DER{}
	extra.Href = "/edev/2/der/2"
	if err := ders.Create(ctx, "2", "2", extra); err != nil {
		t.Fatalf("create extra DER: %v", err)
	}

	assertLink := func(path string, dev sep2.EndDevice, key string, all uint32) {
		t.Helper()
		if dev.DERListLink == nil {
			t.Errorf("%s: DERListLink is nil", path)
			return
		}
		if want := "/edev/" + key + "/der"; dev.DERListLink.Href != want {
			t.Errorf("%s: DERListLink.Href = %q, want %q", path, dev.DERListLink.Href, want)
		}
		if dev.DERListLink.All != all {
			t.Errorf("%s: DERListLink.All = %d, want %d", path, dev.DERListLink.All, all)
		}
	}

	for _, tc := range []struct {
		key, sfdi, lfdi string
		all             uint32
	}{{"1", "1111111111", "AAAA", 1}, {"2", "2222222222", "BBBB", 2}} {
		dev, err := s.Get(ctx, tc.key)
		if err != nil {
			t.Fatalf("Get(%q): %v", tc.key, err)
		}
		assertLink("Get", dev, tc.key, tc.all)
		dev, err = s.GetBySFDI(ctx, tc.sfdi)
		if err != nil {
			t.Fatalf("GetBySFDI(%q): %v", tc.sfdi, err)
		}
		assertLink("GetBySFDI", dev, tc.key, tc.all)
		dev, err = s.GetByLFDI(ctx, tc.lfdi)
		if err != nil {
			t.Fatalf("GetByLFDI(%q): %v", tc.lfdi, err)
		}
		assertLink("GetByLFDI", dev, tc.key, tc.all)
	}

	page, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, dev := range page.Items {
		key := dev.Href[len("/edev/"):]
		all := uint32(1)
		if key == "2" {
			all = 2
		}
		assertLink("List", dev, key, all)
	}
}

func TestDERLinkedEndDeviceStore_CreateSeedsOneDER(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, ders := newDERLinked(t)
	createDevice(t, s, "1", "1111111111", "AAAA")

	got, err := ders.Get(ctx, "1", memory.DefaultDERID)
	if err != nil {
		t.Fatalf("seeded DER: %v", err)
	}
	if want := "/edev/1/der/" + memory.DefaultDERID; got.Href != want {
		t.Errorf("seeded DER href = %q, want %q", got.Href, want)
	}
	if n, _ := ders.Count(ctx, "1"); n != 1 {
		t.Errorf("DER count = %d, want 1", n)
	}
}

func TestSeedDefaultDER_LeavesADeviceWithItsOwnDERAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ders := memory.NewScopedStore[sep2.DER]()
	own := sep2.DER{}
	own.Href = "/edev/1/der/7"
	if err := ders.Create(ctx, "1", "7", own); err != nil {
		t.Fatalf("create own DER: %v", err)
	}
	if err := memory.SeedDefaultDER(ctx, ders, "1"); err != nil {
		t.Fatalf("SeedDefaultDER: %v", err)
	}
	if n, _ := ders.Count(ctx, "1"); n != 1 {
		t.Errorf("DER count = %d, want 1 (no seed beside the device's own)", n)
	}
	if _, err := ders.Get(ctx, "1", memory.DefaultDERID); err == nil {
		t.Errorf("DER %s was seeded beside the device's own DER", memory.DefaultDERID)
	}
}

func TestSeedDefaultDERs_SeedsDevicesStoredBeneathTheDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, devs, ders := newDERLinked(t)
	// Stored on the undecorated store, as a reload from disk or a boot
	// fixture does, so Create seeded nothing.
	createDevice(t, devs, "1", "1111111111", "AAAA")
	createDevice(t, devs, "2", "2222222222", "BBBB")

	if err := memory.SeedDefaultDERs(ctx, devs, ders); err != nil {
		t.Fatalf("SeedDefaultDERs: %v", err)
	}
	if err := memory.SeedDefaultDERs(ctx, devs, ders); err != nil {
		t.Fatalf("SeedDefaultDERs again: %v", err)
	}
	for _, key := range []string{"1", "2"} {
		if n, _ := ders.Count(ctx, key); n != 1 {
			t.Errorf("device %s: DER count = %d, want 1", key, n)
		}
	}
}
