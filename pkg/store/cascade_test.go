package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// plainScoped hides every optional capability of the store it embeds.
type plainScoped struct {
	store.ScopedStore[sep2.Reading]
}

func seedReadings(t *testing.T) store.ScopedStore[sep2.Reading] {
	t.Helper()
	s := memory.NewScopedStore[sep2.Reading]()
	for _, k := range [][2]string{{"a/1", "r1"}, {"a/1", "r2"}, {"a/2", "r1"}, {"b/1", "r1"}} {
		if err := s.Create(context.Background(), k[0], k[1], sep2.Reading{}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestCascade_RemovesAndCountsWhatItRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedReadings(t)
	if n, err := store.CascadePrefix(ctx, s, "a/"); err != nil || n != 3 {
		t.Fatalf("CascadePrefix = %d, %v, want 3 removed", n, err)
	}
	if n, err := store.CascadeParent(ctx, s, "b/1"); err != nil || n != 1 {
		t.Fatalf("CascadeParent = %d, %v, want 1 removed", n, err)
	}
}

func TestCascade_StoreWithoutCapabilityIsRefusedAndNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := plainScoped{seedReadings(t)}
	for name, err := range map[string]error{
		"CascadeParent": func() error { _, e := store.CascadeParent(ctx, s, "a/1"); return e }(),
		"CascadePrefix": func() error { _, e := store.CascadePrefix(ctx, s, "a/"); return e }(),
		"ProbeParent":   store.ProbeParent(ctx, s, "a/1"),
		"ProbePrefix":   store.ProbePrefix(ctx, s, "a/"),
	} {
		if err == nil || !strings.Contains(err.Error(), "plainScoped") {
			t.Errorf("%s error = %v, want a refusal naming the store type", name, err)
		}
	}
}

func TestProbeParent_UnreachableStoreIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fault := &storetest.Fault{}
	s := storetest.NewFaultyScopedStore(seedReadings(t), fault)
	if err := store.ProbeParent(ctx, s, "a/1"); err != nil {
		t.Fatalf("ProbeParent on a healthy store = %v", err)
	}
	boom := errors.New("backend down")
	fault.Arm(boom)
	if err := store.ProbeParent(ctx, s, "a/1"); !errors.Is(err, boom) {
		t.Errorf("ProbeParent on an unreachable store = %v, want it to wrap %v", err, boom)
	}
}
