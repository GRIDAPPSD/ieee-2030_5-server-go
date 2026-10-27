package server

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// The DER stores are memory-only, so a device that came back from disk has no
// DER after a restart until the boot pass seeds one, and a device that already
// has its own is left alone.
func TestSeedStoresSeedsOneDERPerDeviceAndLeavesOwnDERs(t *testing.T) {
	cfg := &config.Config{}
	s, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		dev := sep2.EndDevice{SFDI: "100000000" + id, LFDI: "AA0" + id}
		dev.Href = "/edev/" + id
		if err := s.EndDevices.Create(ctx, id, dev); err != nil {
			t.Fatalf("create device %s: %v", id, err)
		}
	}
	own := sep2.DER{}
	own.Href = "/edev/2/der/7"
	if err := s.DERs.Create(ctx, "2", "7", own); err != nil {
		t.Fatalf("create own DER: %v", err)
	}

	if err := seedStores(ctx, cfg, s); err != nil {
		t.Fatalf("seedStores: %v", err)
	}
	if err := seedStores(ctx, cfg, s); err != nil {
		t.Fatalf("seedStores again: %v", err)
	}

	got, err := s.DERs.Get(ctx, "1", "1")
	if err != nil {
		t.Fatalf("device 1 seeded DER: %v", err)
	}
	if got.Href != "/edev/1/der/1" {
		t.Errorf("seeded DER href = %q, want /edev/1/der/1", got.Href)
	}
	if n, _ := s.DERs.Count(ctx, "1"); n != 1 {
		t.Errorf("device 1 DER count = %d, want 1", n)
	}
	if n, _ := s.DERs.Count(ctx, "2"); n != 1 {
		t.Errorf("device 2 DER count = %d, want 1 (its own, no seed beside it)", n)
	}
	if _, err := s.DERs.Get(ctx, "2", "1"); err == nil {
		t.Error("device 2 was given DER 1 beside its own DER 7")
	}
}
