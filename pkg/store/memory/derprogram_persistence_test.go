package memory_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// DERProgram persistence (GRIDAPPSD/ieee-2030_5-server-go#165). FSA
// records (GRIDAPPSD/ieee-2030_5-server-go#163) reference program hrefs
// (e.g. "/edev/1/fsa/A/derp/1"). For those hrefs to resolve after a
// restart, the DERProgramStore has to survive too.
//
// DERPrograms live in a ScopedStore[*Store[sep2.DERProgram]] keyed by
// device ID. The persistence wrapper flattens the (parent, id, program)
// tuples into a single snapshot.

func newPersistedDERProgramStore(t *testing.T) (*memory.DERProgramStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "derprograms.json")
	store, err := memory.NewDERProgramStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewDERProgramStoreWithPersistence: %v", err)
	}
	return store, path
}

func mkProgram(mRID, description string, primacy uint8) sep2.DERProgram {
	return sep2.DERProgram{
		MRID:        mRID,
		Description: description,
		Primacy:     primacy,
	}
}

func TestDERProgramPersistence_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	store, err := memory.NewDERProgramStoreWithPersistence("")
	if err != nil {
		t.Fatalf("empty path: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store for empty path")
	}
	if err := store.Create(context.Background(), "dev-1", "p1", mkProgram("MR1", "first", 1)); err != nil {
		t.Fatalf("Create on in-memory: %v", err)
	}
}

func TestDERProgramPersistence_ColdBootMissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "subdir", "derprograms.json")
	if _, err := memory.NewDERProgramStoreWithPersistence(missing); err != nil {
		t.Fatalf("cold boot must tolerate missing: %v", err)
	}
}

func TestDERProgramPersistence_CreateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedDERProgramStore(t)

	prog := mkProgram("MRabc", "primary", 2)
	if err := store.Create(ctx, "dev-1", "prog-A", prog); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("snapshot not written: stat=%v size=%d", err, info.Size())
	}

	revived, err := memory.NewDERProgramStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "dev-1", "prog-A")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.MRID != prog.MRID || got.Primacy != prog.Primacy {
		t.Errorf("revived = %+v, want %+v", got, prog)
	}
}

func TestDERProgramPersistence_MultiDeviceRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedDERProgramStore(t)

	_ = store.Create(ctx, "dev-1", "p1", mkProgram("M1", "x", 1))
	_ = store.Create(ctx, "dev-1", "p2", mkProgram("M2", "y", 2))
	_ = store.Create(ctx, "dev-2", "p1", mkProgram("M3", "z", 3))

	revived, err := memory.NewDERProgramStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "dev-2", "p1")
	if err != nil {
		t.Fatalf("dev-2/p1 missing: %v", err)
	}
	if got.MRID != "M3" {
		t.Errorf("dev-2/p1 MRID = %q, want M3", got.MRID)
	}
	// Verify both devices' stores are populated.
	hasDev1, err := revived.HasParent(ctx, "dev-1")
	if err != nil {
		t.Fatalf("HasParent(dev-1): %v", err)
	}
	hasDev2, err := revived.HasParent(ctx, "dev-2")
	if err != nil {
		t.Fatalf("HasParent(dev-2): %v", err)
	}
	if !hasDev1 || !hasDev2 {
		t.Error("revived store missing one of the parents")
	}
}

func TestDERProgramPersistence_DeleteThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedDERProgramStore(t)

	_ = store.Create(ctx, "dev-1", "p1", mkProgram("M1", "x", 1))
	_ = store.Create(ctx, "dev-1", "p2", mkProgram("M2", "y", 2))
	if err := store.Delete(ctx, "dev-1", "p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	revived, err := memory.NewDERProgramStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := revived.Get(ctx, "dev-1", "p1"); err == nil {
		t.Error("dev-1/p1 should be absent after Delete+revive")
	}
	if _, err := revived.Get(ctx, "dev-1", "p2"); err != nil {
		t.Errorf("dev-1/p2 should be present: %v", err)
	}
}

func TestDERProgramPersistence_CorruptJSONRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "derprograms.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := memory.NewDERProgramStoreWithPersistence(path); err == nil {
		t.Fatal("expected error on corrupt JSON")
	}
}

func TestDERProgramPersistence_ConcurrentMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedDERProgramStore(t)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			parent := fmt.Sprintf("dev-%02d", i%4)
			id := fmt.Sprintf("p-%02d", i)
			_ = store.Create(ctx, parent, id, mkProgram(fmt.Sprintf("M%d", i), "x", uint8(i)))
		}(i)
	}
	wg.Wait()

	revived, err := memory.NewDERProgramStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	for i := 0; i < N; i++ {
		parent := fmt.Sprintf("dev-%02d", i%4)
		id := fmt.Sprintf("p-%02d", i)
		if _, err := revived.Get(ctx, parent, id); err != nil {
			t.Errorf("%s/%s missing: %v", parent, id, err)
		}
	}
}
