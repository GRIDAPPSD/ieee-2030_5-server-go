package server

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A Stores with no origin index skips the copy rebuild instead of wrapping
// a nil *ProgramOriginStore in an interface and panicking on it.
func TestSeedStoresWithoutAnOriginIndexSkipsTheCopyRebuild(t *testing.T) {
	cfg := &config.Config{}
	s, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.AdminFSAs.Create(ctx, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminFSAs.AssignDevice(ctx, "fsa-A", "1"); err != nil {
		t.Fatal(err)
	}
	tmpl := memory.ProgramTemplate{ID: "derp-1", MRID: "0000000000000000000000000000A001", DefaultMRID: "0000000000000000000000000000D001"}
	if err := s.AdminFSAs.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	s.ProgramOrigins = nil
	if err := seedStores(ctx, cfg, s); err != nil {
		t.Fatalf("seedStores: %v", err)
	}
	if _, err := s.FSAs.Get(ctx, "1", "fsa-A"); err != nil {
		t.Errorf("device FSA not rebuilt: %v", err)
	}
}
