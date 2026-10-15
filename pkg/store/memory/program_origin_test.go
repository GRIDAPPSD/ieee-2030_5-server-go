package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func newOriginStore(t *testing.T) (*memory.ProgramOriginStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program-origins.json")
	s, err := memory.NewProgramOriginStore(path)
	if err != nil {
		t.Fatalf("NewProgramOriginStore: %v", err)
	}
	return s, path
}

func wantOrigin(t *testing.T, s *memory.ProgramOriginStore, dev, prog string, want memory.ProgramOrigin) {
	t.Helper()
	got, err := s.Get(context.Background(), dev, prog)
	if err != nil {
		t.Fatalf("Get(%s, %s): %v", dev, prog, err)
	}
	if got != want {
		t.Errorf("Get(%s, %s) = %+v, want %+v", dev, prog, got, want)
	}
}

func TestProgramOrigin_CreateReadAndReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newOriginStore(t)

	tmplCopy := memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: "fsa-A", TemplateID: "derp-00000000000000a1"}
	devProg := memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-B"}
	if err := s.Create(ctx, "3", "derp-00000000000000a1", tmplCopy); err != nil {
		t.Fatalf("Create template copy: %v", err)
	}
	if err := s.Create(ctx, "3", "derp-00000000000000d1", devProg); err != nil {
		t.Fatalf("Create device program: %v", err)
	}
	wantOrigin(t, s, "3", "derp-00000000000000a1", tmplCopy)
	wantOrigin(t, s, "3", "derp-00000000000000d1", devProg)

	// No entry means a fixture or external program.
	if _, err := s.Get(ctx, "3", "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get of a program with no origin: err = %v, want ErrNotFound", err)
	}

	revived, err := memory.NewProgramOriginStore(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	wantOrigin(t, revived, "3", "derp-00000000000000a1", tmplCopy)
	wantOrigin(t, revived, "3", "derp-00000000000000d1", devProg)
}

func TestProgramOrigin_UpdateDeleteAndReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newOriginStore(t)
	if err := s.Create(ctx, "3", "p1", memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}); err != nil {
		t.Fatalf("seed p1: %v", err)
	}
	if err := s.Create(ctx, "3", "p2", memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}); err != nil {
		t.Fatalf("seed p2: %v", err)
	}

	moved := memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-B"}
	if err := s.Update(ctx, "3", "p1", moved); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Delete(ctx, "3", "p2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, "3", "p2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Update(ctx, "3", "p9", moved); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update of absent: err = %v, want ErrNotFound", err)
	}

	revived, err := memory.NewProgramOriginStore(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	wantOrigin(t, revived, "3", "p1", moved)
	if _, err := revived.Get(ctx, "3", "p2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revived Get(p2): err = %v, want ErrNotFound", err)
	}
}

func TestProgramOrigin_InvalidOriginsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newOriginStore(t)
	valid := memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}
	if err := s.Create(ctx, "3", "p1", valid); err != nil {
		t.Fatalf("seed: %v", err)
	}

	bad := map[string]memory.ProgramOrigin{
		"unknown kind":             {Kind: "fixture", FSAID: "fsa-A"},
		"empty kind":               {FSAID: "fsa-A"},
		"no fsa":                   {Kind: memory.ProgramOriginDevice},
		"template without its id":  {Kind: memory.ProgramOriginTemplate, FSAID: "fsa-A"},
		"device naming a template": {Kind: memory.ProgramOriginDevice, FSAID: "fsa-A", TemplateID: "derp-1"},
	}
	for name, o := range bad {
		if err := s.Create(ctx, "3", "p2", o); !errors.Is(err, memory.ErrInvalidProgramOrigin) {
			t.Errorf("Create %s: err = %v, want ErrInvalidProgramOrigin", name, err)
		}
		if err := s.Update(ctx, "3", "p1", o); !errors.Is(err, memory.ErrInvalidProgramOrigin) {
			t.Errorf("Update %s: err = %v, want ErrInvalidProgramOrigin", name, err)
		}
	}
	if err := s.Create(ctx, "3", "p1", valid); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("duplicate Create: err = %v, want ErrAlreadyExists", err)
	}
	wantOrigin(t, s, "3", "p1", valid)
	if _, err := s.Get(ctx, "3", "p2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(p2) after refused creates: err = %v, want ErrNotFound", err)
	}
}

// A snapshot holding an origin the store would refuse to write fails the
// load rather than misclassifying a program.
func TestProgramOrigin_InvalidSnapshotRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unknown kind":            `{"version":1,"records":[{"parent":"3","id":"p1","value":{"kind":"fixture","fsaID":"fsa-A"}}]}`,
		"template without its id": `{"version":1,"records":[{"parent":"3","id":"p1","value":{"kind":"template","fsaID":"fsa-A"}}]}`,
		"no fsa":                  `{"version":1,"records":[{"parent":"3","id":"p1","value":{"kind":"device"}}]}`,
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "program-origins.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("%s: seed: %v", name, err)
		}
		if _, err := memory.NewProgramOriginStore(path); err == nil {
			t.Errorf("%s: load succeeded, want an error", name)
		}
	}
}

func TestProgramOrigin_OnDiskShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := newOriginStore(t)
	if err := s.Create(ctx, "3", "p1", memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: "fsa-A", TemplateID: "derp-1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	want := `{"version":1,"records":[{"parent":"3","id":"p1","value":{"kind":"template","fsaID":"fsa-A","templateID":"derp-1"}}]}`
	if string(data) != want {
		t.Errorf("snapshot = %s\nwant      %s", data, want)
	}
}

func TestProgramOrigin_WriteRollsBackWhenPersistFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := memory.NewProgramOriginStore(filepath.Join(dir, "program-origins.json"))
	if err != nil {
		t.Fatalf("NewProgramOriginStore: %v", err)
	}
	orig := memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}
	if err := s.Create(ctx, "3", "p1", orig); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.Create(ctx, "3", "p2", orig); err == nil {
		t.Error("Create succeeded on a read-only directory")
	}
	if err := s.Update(ctx, "3", "p1", memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-B"}); err == nil {
		t.Error("Update succeeded on a read-only directory")
	}
	if err := s.Delete(ctx, "3", "p1"); err == nil {
		t.Error("Delete succeeded on a read-only directory")
	}
	wantOrigin(t, s, "3", "p1", orig)
	if _, err := s.Get(ctx, "3", "p2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(p2) after failed create: err = %v, want ErrNotFound", err)
	}
}

func TestProgramOrigin_EmptyPathIsInMemory(t *testing.T) {
	t.Parallel()
	s, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatalf("NewProgramOriginStore(\"\"): %v", err)
	}
	if s.Persists() {
		t.Error("Persists() = true for an empty path")
	}
	if err := s.Create(context.Background(), "3", "p1", memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}); err != nil {
		t.Errorf("Create: %v", err)
	}
}
