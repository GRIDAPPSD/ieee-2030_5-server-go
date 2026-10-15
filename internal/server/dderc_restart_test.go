package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

const ddercRestartFixture = `
end_devices:
  - id: e1
    sfdi: "222222222222"
    lfdi: "D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1"
    enabled: true
    changed_time: 100
fsas:
  - end_device_id: e1
    id: f1
der_programs:
  - end_device_id: e1
    fsa_id: f1
    id: p1
    mrid: "D2D2D2D2D2D2D2D2"
    primacy: 3
default_der_controls:
  - end_device_id: e1
    fsa_id: f1
    der_program_id: p1
    mrid: "D4D4D4D4D4D4D4D4"
    der_control_base:
      op_mod_fixed_w: 2000
`

func ddercFixedW(t *testing.T, s *Stores) int {
	t.Helper()
	dc, err := s.DefaultDERControls.Get(context.Background(), "e1/f1/p1", "default")
	if err != nil {
		t.Fatalf("DefaultDERControl e1/f1/p1: %v", err)
	}
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatalf("DefaultDERControl has no OpModFixedW: %+v", dc)
	}
	return int(*dc.DERControlBase.OpModFixedW)
}

func ddercRestartConfig(t *testing.T, dataDir string) *config.Config {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(fixture, []byte(ddercRestartFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return &config.Config{DataDir: dataDir, BootFixtureFile: fixture}
}

// TestDefaultDERControl_EditSurvivesARestart drives the boot sequence Run
// performs: an operator edit of a fixture default wins over the fixture.
func TestDefaultDERControl_EditSurvivesARestart(t *testing.T) {
	cfg := ddercRestartConfig(t, t.TempDir())

	boot1 := bootFSA(t, cfg)
	if got := ddercFixedW(t, boot1); got != 2000 {
		t.Fatalf("OpModFixedW after boot 1 = %d, want the fixture's 2000", got)
	}
	dc, err := boot1.DefaultDERControls.Get(context.Background(), "e1/f1/p1", "default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	edited := dc.Copy()
	v := sep2.SignedPerCent(500)
	edited.DERControlBase.OpModFixedW = &v
	if err := boot1.DefaultDERControls.Update(context.Background(), "e1/f1/p1", "default", edited); err != nil {
		t.Fatalf("edit: %v", err)
	}

	boot2 := bootFSA(t, cfg)
	if got := ddercFixedW(t, boot2); got != 500 {
		t.Errorf("OpModFixedW after boot 2 = %d, want the edited 500", got)
	}
}

// TestDefaultDERControl_NoDataDirReloadsFixture: without a data dir an edit
// is lost and the fixture applies again, as before.
func TestDefaultDERControl_NoDataDirReloadsFixture(t *testing.T) {
	cfg := ddercRestartConfig(t, "")

	boot1 := bootFSA(t, cfg)
	dc, err := boot1.DefaultDERControls.Get(context.Background(), "e1/f1/p1", "default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	edited := dc.Copy()
	v := sep2.SignedPerCent(500)
	edited.DERControlBase.OpModFixedW = &v
	if err := boot1.DefaultDERControls.Update(context.Background(), "e1/f1/p1", "default", edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := ddercFixedW(t, boot1); got != 500 {
		t.Fatalf("OpModFixedW after edit = %d, want 500", got)
	}

	boot2 := bootFSA(t, cfg)
	if got := ddercFixedW(t, boot2); got != 2000 {
		t.Errorf("OpModFixedW after boot 2 = %d, want the fixture's 2000", got)
	}
}
