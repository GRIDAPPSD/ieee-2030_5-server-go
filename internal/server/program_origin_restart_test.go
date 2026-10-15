package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestProgramStores_SurviveARestart: the template on an admin FSA and a
// device program's origin are both read back from the data dir by the
// stores Run builds.
func TestProgramStores_SurviveARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}

	boot1 := bootFSA(t, cfg)
	createFSAVia(t, boot1, "fsa-1", "east feeder")
	tmpl := memory.ProgramTemplate{ID: "derp-00000000000000a1", MRID: "0000000000000000000000000000A001", Description: "volt support", Primacy: 2, Version: 3}
	if err := boot1.AdminFSAs.CreateTemplate(ctx, "fsa-1", tmpl); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	origin := memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: "fsa-1", TemplateID: tmpl.ID}
	if err := boot1.ProgramOrigins.Create(ctx, "7", tmpl.ID, origin); err != nil {
		t.Fatalf("ProgramOrigins.Create: %v", err)
	}
	if !boot1.ProgramOrigins.Persists() {
		t.Error("ProgramOrigins.Persists() = false with a data dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "program-origins.json")); err != nil {
		t.Errorf("origin snapshot: %v", err)
	}

	boot2 := bootFSA(t, cfg)
	got, err := boot2.AdminFSAs.Template(ctx, "fsa-1", tmpl.ID)
	if err != nil {
		t.Fatalf("Template after restart: %v", err)
	}
	if got.MRID != tmpl.MRID || got.Description != tmpl.Description || got.Primacy != tmpl.Primacy || got.Version != tmpl.Version {
		t.Errorf("template after restart = %+v, want %+v", got, tmpl)
	}
	gotOrigin, err := boot2.ProgramOrigins.Get(ctx, "7", tmpl.ID)
	if err != nil {
		t.Fatalf("origin after restart: %v", err)
	}
	if gotOrigin != origin {
		t.Errorf("origin after restart = %+v, want %+v", gotOrigin, origin)
	}
}

// TestProgramOrigins_NoDataDirIsInMemory: without a data dir the index is
// built and holds nothing across boots.
func TestProgramOrigins_NoDataDirIsInMemory(t *testing.T) {
	s := bootFSA(t, &config.Config{})
	if s.ProgramOrigins == nil {
		t.Fatal("ProgramOrigins is nil")
	}
	if s.ProgramOrigins.Persists() {
		t.Error("ProgramOrigins.Persists() = true with no data dir")
	}
}
