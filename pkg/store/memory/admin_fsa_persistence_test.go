package memory_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// AdminFSAStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165): pins
// both the FSA record AND the denormalized program-attachments and
// device-assignments. Links live next to the FSA itself
// (GRIDAPPSD/ieee-2030_5-server-go#163); persistence has to round-trip
// both.

func newPersistedAdminFSAStore(t *testing.T) (*memory.AdminFSAStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fsas.json")
	store, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewAdminFSAStoreWithPersistence: %v", err)
	}
	return store, path
}

func mkFSA(mRID, description string, _ uint8) sep2.FunctionSetAssignments {
	return sep2.FunctionSetAssignments{
		MRID:        mRID,
		Description: description,
	}
}

func TestAdminFSAPersistence_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	store, err := memory.NewAdminFSAStoreWithPersistence("")
	if err != nil {
		t.Fatalf("empty path: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store for empty path")
	}
	if err := store.Create(context.Background(), "fsa-1", mkFSA("MR1", "first", 1)); err != nil {
		t.Fatalf("Create on in-memory: %v", err)
	}
}

func TestAdminFSAPersistence_ColdBootMissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "subdir", "fsas.json")
	if _, err := memory.NewAdminFSAStoreWithPersistence(missing); err != nil {
		t.Fatalf("cold boot must tolerate missing file: %v", err)
	}
}

func TestAdminFSAPersistence_CreateThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	fsa := mkFSA("MR-abc", "primary", 1)
	if err := store.Create(ctx, "fsa-A", fsa); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("snapshot not written: stat=%v size=%d", err, info.Size())
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	got, err := revived.Get(ctx, "fsa-A")
	if err != nil {
		t.Fatalf("Get after revive: %v", err)
	}
	if got.MRID != fsa.MRID || got.Description != fsa.Description {
		t.Errorf("revived = %+v, want %+v", got, fsa)
	}
}

func TestAdminFSAPersistence_ProgramAttachmentRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	if err := store.Create(ctx, "fsa-A", mkFSA("MRa", "fa", 1)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.AttachProgram(ctx, "fsa-A", "/edev/1/fsa/A/derp/1"); err != nil {
		t.Fatalf("AttachProgram 1: %v", err)
	}
	if err := store.AttachProgram(ctx, "fsa-A", "/edev/1/fsa/A/derp/2"); err != nil {
		t.Fatalf("AttachProgram 2: %v", err)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	progs := revived.Programs(ctx, "fsa-A")
	sort.Strings(progs)
	want := []string{"/edev/1/fsa/A/derp/1", "/edev/1/fsa/A/derp/2"}
	if !equalSlices(progs, want) {
		t.Errorf("revived programs = %v, want %v", progs, want)
	}
}

func TestAdminFSAPersistence_DeviceAssignmentRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	if err := store.Create(ctx, "fsa-A", mkFSA("MRa", "fa", 1)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.AssignDevice(ctx, "fsa-A", "dev-1"); err != nil {
		t.Fatalf("AssignDevice dev-1: %v", err)
	}
	if err := store.AssignDevice(ctx, "fsa-A", "dev-2"); err != nil {
		t.Fatalf("AssignDevice dev-2: %v", err)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	devs := revived.Devices(ctx, "fsa-A")
	want := []string{"dev-1", "dev-2"}
	if !equalSlices(devs, want) {
		t.Errorf("revived devices = %v, want %v", devs, want)
	}
}

func TestAdminFSAPersistence_DeleteThenReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	_ = store.Create(ctx, "fsa-A", mkFSA("MRa", "a", 1))
	_ = store.Create(ctx, "fsa-B", mkFSA("MRb", "b", 2))
	if err := store.Delete(ctx, "fsa-A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := revived.Get(ctx, "fsa-A"); err == nil {
		t.Error("fsa-A should be absent after Delete+revive")
	}
	if _, err := revived.Get(ctx, "fsa-B"); err != nil {
		t.Errorf("fsa-B should be present: %v", err)
	}
}

func TestAdminFSAPersistence_DetachUnassignRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	_ = store.Create(ctx, "fsa-A", mkFSA("MRa", "fa", 1))
	_ = store.AttachProgram(ctx, "fsa-A", "/p/1")
	_ = store.AttachProgram(ctx, "fsa-A", "/p/2")
	_ = store.AssignDevice(ctx, "fsa-A", "d-1")
	_ = store.AssignDevice(ctx, "fsa-A", "d-2")

	if err := store.DetachProgram(ctx, "fsa-A", "/p/1"); err != nil {
		t.Fatalf("DetachProgram: %v", err)
	}
	if err := store.UnassignDevice(ctx, "fsa-A", "d-1"); err != nil {
		t.Fatalf("UnassignDevice: %v", err)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if got := revived.Programs(ctx, "fsa-A"); !equalSlices(got, []string{"/p/2"}) {
		t.Errorf("programs after detach+revive = %v, want [/p/2]", got)
	}
	if got := revived.Devices(ctx, "fsa-A"); !equalSlices(got, []string{"d-2"}) {
		t.Errorf("devices after unassign+revive = %v, want [d-2]", got)
	}
}

func TestAdminFSAPersistence_CorruptJSONRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fsas.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := memory.NewAdminFSAStoreWithPersistence(path); err == nil {
		t.Fatal("expected error on corrupt JSON")
	}
}

func TestAdminFSAPersistence_ConcurrentMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path := newPersistedAdminFSAStore(t)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("fsa-%02d", i)
			_ = store.Create(ctx, id, mkFSA(fmt.Sprintf("MR%d", i), "x", uint8(i)))
			_ = store.AttachProgram(ctx, id, fmt.Sprintf("/p/%d", i))
			_ = store.AssignDevice(ctx, id, fmt.Sprintf("d-%d", i))
		}(i)
	}
	wg.Wait()

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("fsa-%02d", i)
		if _, err := revived.Get(ctx, id); err != nil {
			t.Errorf("%s missing: %v", id, err)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
