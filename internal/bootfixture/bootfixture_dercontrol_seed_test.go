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

// dercontrolSeedFixture seeds one EndDevice, one DERProgram and one
// DERControl. Shared by the seed tests below.
const dercontrolSeedFixture = `
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
der_controls:
  - end_device_id: e1
    fsa_id: "0"
    der_program_id: p1
    id: c1
    mrid: "D3D3D3D3D3D3D3D3"
`

// dercontrolSeedTarget opens fresh, persisted store handles on the files
// under dataDir, exactly as a server restart would: a new process, the same
// data_dir. FSAs and DefaultDERControls stay in-memory-only, matching
// production (neither persists).
func dercontrolSeedTarget(t *testing.T, dataDir string) *bootfixture.Target {
	t.Helper()
	edevs, err := memory.NewEndDeviceStoreWithPersistence(filepath.Join(dataDir, "enddevices.json"))
	if err != nil {
		t.Fatalf("open EndDevice store: %v", err)
	}
	derps, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dataDir, "derprograms.json"))
	if err != nil {
		t.Fatalf("open DERProgram store: %v", err)
	}
	dercs, err := memory.NewDERControlStoreWithPersistence(filepath.Join(dataDir, "dercontrols.json"))
	if err != nil {
		t.Fatalf("open DERControl store: %v", err)
	}
	return &bootfixture.Target{
		EndDevices:         edevs,
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms:        derps,
		DERControls:        dercs,
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
	}
}

// TestDERControlSeed_TwoBootsNoDuplicateCreate is acceptance criterion 3:
// a boot fixture combined with a data directory seeds once and yields to
// persisted state on the second boot, with no duplicate-create failure.
//
// Before #565 wired DERControls into the same seed-tracking DERPrograms
// already uses, this reproduced ErrAlreadyExists on the second boot: the
// fixture's der_controls entry was replayed unconditionally every time,
// which only worked because DERControls had never persisted across a
// restart before.
func TestDERControlSeed_TwoBootsNoDuplicateCreate(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(dercontrolSeedFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := dercontrolSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	ctrl1, err := target1.DERControls.Get(ctx, "e1/0/p1", "c1")
	if err != nil {
		t.Fatalf("get control after boot 1: %v", err)
	}

	// Boot 2: fresh store instances (a new process) reading the same
	// data_dir files and the same fixture. Must not attempt to re-Create
	// the already-persisted control.
	target2 := dercontrolSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v (want no duplicate-create failure)", err)
	}
	ctrl2, err := target2.DERControls.Get(ctx, "e1/0/p1", "c1")
	if err != nil {
		t.Fatalf("get control after boot 2: %v", err)
	}
	if ctrl2.MRID != ctrl1.MRID {
		t.Errorf("control MRID after boot 2 = %q, want %q (the persisted record, not a re-created one)", ctrl2.MRID, ctrl1.MRID)
	}
	n, err := target2.DERControls.Count(ctx, "e1/0/p1")
	if err != nil {
		t.Fatalf("Count after boot 2: %v", err)
	}
	if n != 1 {
		t.Errorf("Count after boot 2 = %d, want 1 (the fixture's single control, not duplicated)", n)
	}
}

// TestDERControlSeed_DeletedStaysDeleted is GRIDAPPSD/ieee-2030_5-server-go#565
// fix round 1, item 4: the sibling test above never deletes a seeded
// control, so reconcile.go's `case wasSeeded:` branch for DERControl (the
// one that skips recreating a control an operator deleted since it was
// seeded) had no test that could catch it being disabled. This seeds the
// fixture's control, deletes it directly (mirroring how
// TestReconcileKeepsDeletesAndSkipsChildren simulates an operator delete
// for EndDevice and DERProgram), boots again over the same fixture and
// data_dir, and asserts the control was not recreated.
func TestDERControlSeed_DeletedStaysDeleted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(dercontrolSeedFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := dercontrolSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if _, err := target1.DERControls.Get(ctx, "e1/0/p1", "c1"); err != nil {
		t.Fatalf("control missing after boot 1: %v", err)
	}

	if err := target1.DERControls.Delete(ctx, "e1/0/p1", "c1"); err != nil {
		t.Fatalf("operator delete: %v", err)
	}

	target2 := dercontrolSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if _, err := target2.DERControls.Get(ctx, "e1/0/p1", "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DERControl after boot 2 = %v, want ErrNotFound: a control deleted since it was seeded must stay deleted", err)
	}
}
