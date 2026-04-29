package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestEndDeviceGetBySFDI(t *testing.T) {
	s := memory.NewEndDeviceStore()
	ctx := context.Background()

	dev := sep2.EndDevice{
		SFDI: "123456789012",
		LFDI: "AABBCCDD00112233445566778899AABBCCDDEEFF",
	}
	dev.Href = "/edev/1"

	_ = s.Create(ctx, "1", dev)

	got, err := s.GetBySFDI(ctx, "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if got.SFDI != "123456789012" {
		t.Errorf("SFDI = %q", got.SFDI)
	}
}

func TestEndDeviceGetByLFDI(t *testing.T) {
	s := memory.NewEndDeviceStore()
	ctx := context.Background()

	dev := sep2.EndDevice{
		SFDI: "123456789012",
		LFDI: "AABBCCDD00112233445566778899AABBCCDDEEFF",
	}
	_ = s.Create(ctx, "1", dev)

	got, err := s.GetByLFDI(ctx, "AABBCCDD00112233445566778899AABBCCDDEEFF")
	if err != nil {
		t.Fatal(err)
	}
	if got.LFDI != "AABBCCDD00112233445566778899AABBCCDDEEFF" {
		t.Errorf("LFDI = %q", got.LFDI)
	}
}

func TestEndDeviceIndexNotFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	ctx := context.Background()

	_, err := s.GetBySFDI(ctx, "nonexistent")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}

	_, err = s.GetByLFDI(ctx, "nonexistent")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestEndDeviceIndexUpdateReindexes(t *testing.T) {
	s := memory.NewEndDeviceStore()
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "111111111111", LFDI: "AAAA"}
	_ = s.Create(ctx, "1", dev)

	// Update with new SFDI
	updated := sep2.EndDevice{SFDI: "222222222222", LFDI: "BBBB"}
	updated.Href = "/edev/1"
	_ = s.Update(ctx, "1", updated)

	// Old SFDI should not find anything
	_, err := s.GetBySFDI(ctx, "111111111111")
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("old SFDI should not be indexed after update")
	}

	// New SFDI should work
	got, err := s.GetBySFDI(ctx, "222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if got.SFDI != "222222222222" {
		t.Errorf("SFDI = %q after update", got.SFDI)
	}
}

func TestEndDeviceIndexDeleteRemoves(t *testing.T) {
	s := memory.NewEndDeviceStore()
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "333333333333", LFDI: "CCCC"}
	_ = s.Create(ctx, "1", dev)
	_ = s.Delete(ctx, "1")

	_, err := s.GetBySFDI(ctx, "333333333333")
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("SFDI index should be removed after delete")
	}
}
