package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1154: a fleet FSA belongs to one aggregator, and at most one exists per
// aggregator.

func TestFleetFSA_OnePerAggregatorAndLookupBothWays(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	if err := s.CreateFleet(ctx, "f1", mkFSA("f1", "fleet", 0), "AGG1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFleet(ctx, "f2", mkFSA("f2", "fleet", 0), "AGG1"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("second fleet FSA for AGG1 = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.Get(ctx, "f2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refused fleet FSA f2 was stored: %v", err)
	}
	if err := s.CreateFleet(ctx, "f3", mkFSA("f3", "fleet", 0), ""); !errors.Is(err, memory.ErrInvalidFleet) {
		t.Errorf("fleet FSA with no aggregator = %v, want ErrInvalidFleet", err)
	}
	if id, ok := s.FleetFSA("AGG1"); !ok || id != "f1" {
		t.Errorf("FleetFSA(AGG1) = %q, %v, want f1", id, ok)
	}
	if got := s.FleetAggregator("f1"); got != "AGG1" {
		t.Errorf("FleetAggregator(f1) = %q, want AGG1", got)
	}
	if err := s.Create(ctx, "plain", mkFSA("plain", "x", 0)); err != nil {
		t.Fatal(err)
	}
	if got := s.FleetAggregator("plain"); got != "" {
		t.Errorf("FleetAggregator(plain) = %q, want empty", got)
	}
}

func TestFleetFSA_SurvivesRestartAndDeleteDropsTheBinding(t *testing.T) {
	t.Parallel()
	s, path := newPersistedAdminFSAStore(t)
	ctx := context.Background()
	if err := s.CreateFleet(ctx, "f1", mkFSA("f1", "fleet", 0), "AGG1"); err != nil {
		t.Fatal(err)
	}
	re, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.FleetAggregator("f1"); got != "AGG1" {
		t.Errorf("binding after restart = %q, want AGG1", got)
	}
	if err := re.CreateFleet(ctx, "f2", mkFSA("f2", "fleet", 0), "AGG1"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("second fleet after restart = %v, want ErrAlreadyExists", err)
	}
	if err := re.Delete(ctx, "f1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := re.FleetFSA("AGG1"); ok {
		t.Error("binding outlived its FSA")
	}
	if err := re.CreateFleet(ctx, "f2", mkFSA("f2", "fleet", 0), "AGG1"); err != nil {
		t.Errorf("new fleet FSA after the delete = %v", err)
	}
}
