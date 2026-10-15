package bootfixture_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const ddercSeedFixture = `
end_devices:
  - id: e1
    sfdi: "222222222222"
    lfdi: "D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1"
    enabled: true
    changed_time: 100
fsas:
  - end_device_id: e1
    id: "0"
der_programs:
  - end_device_id: e1
    fsa_id: "0"
    id: p1
    mrid: "D2D2D2D2D2D2D2D2"
    description: fixture program p1
    primacy: 3
default_der_controls:
  - end_device_id: e1
    fsa_id: "0"
    der_program_id: p1
    mrid: "D4D4D4D4D4D4D4D4"
    der_control_base:
      op_mod_fixed_w: 2000
`

const ddercScope = "e1/0/p1"

// ddercSeedTarget reopens every persisted store under dataDir as a restart
// would, DefaultDERControls included.
func ddercSeedTarget(t *testing.T, dataDir string) *bootfixture.Target {
	t.Helper()
	target := dercontrolSeedTarget(t, dataDir)
	dderc, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](
		filepath.Join(dataDir, "defaultdercontrols.json"), "DefaultDERControl",
	)
	if err != nil {
		t.Fatalf("open DefaultDERControl store: %v", err)
	}
	target.DefaultDERControls = dderc
	return target
}

func writeDdercFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(p, []byte(ddercSeedFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func fixedW(t *testing.T, dc sep2.DefaultDERControl) int {
	t.Helper()
	if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
		t.Fatalf("DefaultDERControl has no OpModFixedW: %+v", dc)
	}
	return int(*dc.DERControlBase.OpModFixedW)
}

// An operator edit of a fixture default control wins over the fixture file on
// the next boot (seed-once). Without seed tracking the second boot would
// retry Create on the persisted record and fail with ErrAlreadyExists.
func TestDefaultDERControlSeed_EditSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := writeDdercFixture(t)
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	dc, err := target1.DefaultDERControls.Get(ctx, ddercScope, "default")
	if err != nil {
		t.Fatalf("get default control after boot 1: %v", err)
	}
	if got := fixedW(t, dc); got != 2000 {
		t.Fatalf("OpModFixedW after boot 1 = %d, want the fixture's 2000", got)
	}
	edited := dc.Copy()
	v := sep2.SignedPerCent(500)
	edited.DERControlBase.OpModFixedW = &v
	if err := target1.DefaultDERControls.Update(ctx, ddercScope, "default", edited); err != nil {
		t.Fatalf("operator edit: %v", err)
	}

	target2 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	got, err := target2.DefaultDERControls.Get(ctx, ddercScope, "default")
	if err != nil {
		t.Fatalf("get default control after boot 2: %v", err)
	}
	if w := fixedW(t, got); w != 500 {
		t.Errorf("OpModFixedW after boot 2 = %d, want the operator's 500, not the fixture's 2000", w)
	}
	if got.MRID != "D4D4D4D4D4D4D4D4" {
		t.Errorf("MRID after boot 2 = %q, want D4D4D4D4D4D4D4D4", got.MRID)
	}
	if want := "/edev/e1/fsa/0/derp/p1/dderc"; got.Href != want {
		t.Errorf("Href after boot 2 = %q, want %q", got.Href, want)
	}
	n, err := target2.DefaultDERControls.Count(ctx, ddercScope)
	if err != nil {
		t.Fatalf("Count after boot 2: %v", err)
	}
	if n != 1 {
		t.Errorf("Count after boot 2 = %d, want 1", n)
	}
}

// A default control the operator deleted is not recreated from the fixture.
func TestDefaultDERControlSeed_DeletedStaysDeleted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := writeDdercFixture(t)
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if err := target1.DefaultDERControls.Delete(ctx, ddercScope, "default"); err != nil {
		t.Fatalf("operator delete: %v", err)
	}

	target2 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if _, err := target2.DefaultDERControls.Get(ctx, ddercScope, "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default control after boot 2 = %v, want ErrNotFound: a deleted fixture default must stay deleted", err)
	}
}

// An unseeded persisted default control (created at runtime, or by a server
// that predates seed tracking) is adopted, not duplicated or overwritten.
func TestDefaultDERControlSeed_PersistedUnseededIsAdopted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := writeDdercFixture(t)
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	dc, err := target1.DefaultDERControls.Get(ctx, ddercScope, "default")
	if err != nil {
		t.Fatalf("get after boot 1: %v", err)
	}
	edited := dc.Copy()
	v := sep2.SignedPerCent(700)
	edited.DERControlBase.OpModFixedW = &v
	if err := target1.DefaultDERControls.Update(ctx, ddercScope, "default", edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := os.Remove(seedPath); err != nil {
		t.Fatalf("remove seed record: %v", err)
	}

	target2 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v (want the persisted record adopted)", err)
	}
	got, err := target2.DefaultDERControls.Get(ctx, ddercScope, "default")
	if err != nil {
		t.Fatalf("get after boot 2: %v", err)
	}
	if w := fixedW(t, got); w != 700 {
		t.Errorf("OpModFixedW after boot 2 = %d, want the persisted 700", w)
	}
}

// Without a data dir the seed path is empty and the fixture creates its
// default control on every boot, as before.
func TestDefaultDERControlSeed_NoDataDirLoadsFixture(t *testing.T) {
	ctx := context.Background()
	fixturePath := writeDdercFixture(t)
	for boot := 1; boot <= 2; boot++ {
		target := dercontrolSeedTarget(t, t.TempDir())
		if err := bootfixture.Reconcile(ctx, target, fixturePath, "", t.Logf); err != nil {
			t.Fatalf("boot %d: %v", boot, err)
		}
		dc, err := target.DefaultDERControls.Get(ctx, ddercScope, "default")
		if err != nil {
			t.Fatalf("boot %d: get default control: %v", boot, err)
		}
		if got := fixedW(t, dc); got != 2000 {
			t.Errorf("boot %d: OpModFixedW = %d, want the fixture's 2000", boot, got)
		}
	}
}
