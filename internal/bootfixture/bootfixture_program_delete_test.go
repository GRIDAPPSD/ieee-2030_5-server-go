package bootfixture_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #922: a fixture program delete that fails partway keeps the program, so
// no default is orphaned, a retry after a restart with a data dir finishes
// it, and the seed record then keeps both deleted.

type failingDefaultDelete struct {
	fsaprogram.CopyDefaultStore
}

func (failingDefaultDelete) Delete(context.Context, string, string) error {
	return errors.New("default disk gone")
}

func fixtureCopies(t *testing.T, target *bootfixture.Target) fsaprogram.Copies {
	t.Helper()
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	return fsaprogram.Copies{Programs: target.DERPrograms, Defaults: target.DefaultDERControls, Origins: origins}
}

func TestFixtureProgramDelete_PartialIsFinishedAfterARestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fixturePath := writeDdercFixture(t)
	seedPath := filepath.Join(dataDir, "bootfixture-seed.json")

	target1 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target1, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	broken := fixtureCopies(t, target1)
	broken.Defaults = failingDefaultDelete{CopyDefaultStore: broken.Defaults}
	var part *fsaprogram.PartError
	if err := broken.RemoveProgram(ctx, "e1", "0", "p1"); !errors.As(err, &part) || part.Part != fsaprogram.PartDefault {
		t.Fatalf("partial delete err = %v, want a *PartError on the default", err)
	}

	target2 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target2, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if _, err := target2.DERPrograms.Get(ctx, "e1", "p1"); err != nil {
		t.Fatalf("program after boot 2: %v, want it kept for the retry", err)
	}
	if _, err := target2.DefaultDERControls.Get(ctx, ddercScope, "default"); err != nil {
		t.Fatalf("default after boot 2: %v, want it kept with its program", err)
	}
	if err := fixtureCopies(t, target2).RemoveProgram(ctx, "e1", "0", "p1"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	target3 := ddercSeedTarget(t, dataDir)
	if err := bootfixture.Reconcile(ctx, target3, fixturePath, seedPath, t.Logf); err != nil {
		t.Fatalf("boot 3: %v", err)
	}
	if _, err := target3.DERPrograms.Get(ctx, "e1", "p1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program after boot 3: %v, want it to stay deleted", err)
	}
	if _, err := target3.DefaultDERControls.Get(ctx, ddercScope, "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after boot 3: %v, want it to stay deleted", err)
	}
}
