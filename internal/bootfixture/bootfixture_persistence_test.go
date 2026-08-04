package bootfixture_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
)

// IEEESRV-038.
//
// Target.DERPrograms is declared as the store.ScopedStore contract. The type
// the server actually owns and passes is *memory.DERProgramStore, the disk
// persistence wrapper, and the assertion below is what keeps that assignment
// legal: a future narrowing of the field back to a concrete
// *memory.ScopedStore fails to compile here rather than at the one call site
// in server.Run.
//
// This is not a hypothetical. Until IEEESRV-038, server.Run reached past the
// wrapper for its embedded collection and handed the loader the inner store,
// so every program a boot fixture created went in behind the wrapper's back
// and was never flushed. Core IEEECORE-085 withdrew that reach-through, and
// IEEECORE-106 recorded the dropped flush it had been hiding.
var _ store.ScopedStore[sep2.DERProgram] = (*memory.DERProgramStore)(nil)

// derProgramSnapshotEnvelope and derProgramSnapshotRecord mirror the
// wrapper's on-disk shape, `{"version":N,"records":[...]}`. Both are
// declared here rather than imported because the production types are
// unexported: reading the bytes back through an independent declaration is
// the point, since a test that round-trips through the writer's own type
// cannot tell a correct snapshot from a self-consistent wrong one.
type derProgramSnapshotEnvelope struct {
	Version int                        `json:"version"`
	Records []derProgramSnapshotRecord `json:"records"`
}

type derProgramSnapshotRecord struct {
	ParentID string          `json:"parent"`
	ID       string          `json:"id"`
	Program  sep2.DERProgram `json:"program"`
}

const persistenceFixtureYAML = `
end_devices:
  - id: dev-1
    sfdi: "123456789012"
    lfdi: "0123456789ABCDEF0123456789ABCDEF01234567"
    changed_time: 1700000000
der_programs:
  - end_device_id: dev-1
    id: prog-1
    mrid: "ABCDEF0123456789"
    description: persisted program
    primacy: 3
`

// TestBootFixtureDERProgramsReachDiskThroughWrapper drives the production
// loader against the persistence wrapper and asserts on both halves of the
// contract: the values readable from the store, and the bytes on disk.
//
// Asserting only the readback would pass just as happily against the old
// inner-store wiring, because the inner store is where a read lands either
// way. The snapshot file is the half that distinguishes them.
func TestBootFixtureDERProgramsReachDiskThroughWrapper(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	snapshot := filepath.Join(dir, "derprograms.json")

	programs, err := memory.NewDERProgramStoreWithPersistence(snapshot)
	if err != nil {
		t.Fatalf("NewDERProgramStoreWithPersistence: %v", err)
	}

	target := freshTarget()
	// Swap in the wrapper the server owns, in place of the bare scoped store
	// the other tests use. Everything else about the Target is unchanged.
	target.DERPrograms = programs

	path := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(path, []byte(persistenceFixtureYAML), 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	ctx := context.Background()
	if err := bootfixture.Load(ctx, target, path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := programs.Get(ctx, "dev-1", "prog-1")
	if err != nil {
		t.Fatalf("Get(dev-1, prog-1) after fixture load: %v", err)
	}
	if got.MRID != "ABCDEF0123456789" {
		t.Errorf("stored MRID = %q, want %q", got.MRID, "ABCDEF0123456789")
	}
	if got.Description != "persisted program" {
		t.Errorf("stored Description = %q, want %q", got.Description, "persisted program")
	}
	if got.Primacy != 3 {
		t.Errorf("stored Primacy = %d, want 3", got.Primacy)
	}
	// The href is what a client dereferences, and the loader is the only
	// thing that stamps it, so an empty or mis-scoped value here is an
	// unroutable program rather than a cosmetic defect.
	if want := "/edev/dev-1/derp/prog-1"; got.Href != want {
		t.Errorf("stored Href = %q, want %q", got.Href, want)
	}

	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("read snapshot %s: %v (a fixture-loaded program that never "+
			"reaches disk is the IEEECORE-106 failure this test exists for)", snapshot, err)
	}

	var env derProgramSnapshotEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	// The version is what rehydration checks before trusting the records, so
	// a snapshot written under an unexpected one would not reload.
	if env.Version != memory.PersistenceVersion {
		t.Errorf("snapshot version = %d, want %d", env.Version, memory.PersistenceVersion)
	}
	if len(env.Records) != 1 {
		t.Fatalf("snapshot holds %d records, want 1", len(env.Records))
	}

	rec := env.Records[0]
	// The (parent, id) pair is the addressing scheme rehydration re-Creates
	// from. A snapshot carrying the right program under the wrong parent
	// reloads into a program no device can reach.
	if rec.ParentID != "dev-1" {
		t.Errorf("snapshot record parent = %q, want %q", rec.ParentID, "dev-1")
	}
	if rec.ID != "prog-1" {
		t.Errorf("snapshot record id = %q, want %q", rec.ID, "prog-1")
	}
	if rec.Program.Primacy != 3 {
		t.Errorf("snapshot record Primacy = %d, want 3", rec.Program.Primacy)
	}
	if rec.Program.MRID != "ABCDEF0123456789" {
		t.Errorf("snapshot record MRID = %q, want %q", rec.Program.MRID, "ABCDEF0123456789")
	}
}

// TestBootFixtureDERProgramsRehydrateFromSnapshot closes the loop: a second
// store pointed at the same snapshot must come up holding the program the
// first one wrote. A snapshot that is written but not readable back is worth
// as little as no snapshot at all, and only a separate store instance can
// tell the two apart.
func TestBootFixtureDERProgramsRehydrateFromSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	snapshot := filepath.Join(dir, "derprograms.json")

	programs, err := memory.NewDERProgramStoreWithPersistence(snapshot)
	if err != nil {
		t.Fatalf("NewDERProgramStoreWithPersistence: %v", err)
	}
	target := freshTarget()
	target.DERPrograms = programs

	path := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(path, []byte(persistenceFixtureYAML), 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	ctx := context.Background()
	if err := bootfixture.Load(ctx, target, path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	reloaded, err := memory.NewDERProgramStoreWithPersistence(snapshot)
	if err != nil {
		t.Fatalf("reopen store on snapshot: %v", err)
	}

	got, err := reloaded.Get(ctx, "dev-1", "prog-1")
	if err != nil {
		t.Fatalf("Get(dev-1, prog-1) on reloaded store: %v", err)
	}
	if got.Primacy != 3 {
		t.Errorf("rehydrated Primacy = %d, want 3", got.Primacy)
	}
	if got.MRID != "ABCDEF0123456789" {
		t.Errorf("rehydrated MRID = %q, want %q", got.MRID, "ABCDEF0123456789")
	}
	if want := "/edev/dev-1/derp/prog-1"; got.Href != want {
		t.Errorf("rehydrated Href = %q, want %q", got.Href, want)
	}
}
