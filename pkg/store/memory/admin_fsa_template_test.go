package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func mkTemplate(id, mRID, desc string, primacy uint8, version uint16, limW uint16) memory.ProgramTemplate {
	connect := true
	lim := sep2.PerCent(limW)
	return memory.ProgramTemplate{
		ID:          id,
		MRID:        mRID,
		Description: desc,
		Primacy:     primacy,
		Version:     version,
		DefaultControlBase: sep2.DERControlBase{
			OpModConnect: &connect,
			OpModMaxLimW: &lim,
		},
	}
}

func seededFSA(t *testing.T) (*memory.AdminFSAStore, string) {
	t.Helper()
	s, path := newPersistedAdminFSAStore(t)
	if err := s.Create(context.Background(), "fsa-A", mkFSA("MRa", "a", 1)); err != nil {
		t.Fatalf("Create fsa-A: %v", err)
	}
	return s, path
}

func wantTemplate(t *testing.T, s *memory.AdminFSAStore, fsaID string, want memory.ProgramTemplate) {
	t.Helper()
	got, err := s.Template(context.Background(), fsaID, want.ID)
	if err != nil {
		t.Fatalf("Template(%s, %s): %v", fsaID, want.ID, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Template(%s, %s) = %+v, want %+v", fsaID, want.ID, got, want)
	}
}

func TestAdminFSATemplate_CreateReadAndReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSA(t)

	b := mkTemplate("derp-00000000000000b2", "0000000000000000000000000000B002", "peak shave", 1, 0, 5000)
	a := mkTemplate("derp-00000000000000a1", "0000000000000000000000000000A001", "volt support", 2, 7, 8000)
	for _, tmpl := range []memory.ProgramTemplate{b, a} {
		if err := s.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
			t.Fatalf("CreateTemplate(%s): %v", tmpl.ID, err)
		}
	}
	wantTemplate(t, s, "fsa-A", a)
	wantTemplate(t, s, "fsa-A", b)
	if got := s.Templates(ctx, "fsa-A"); !reflect.DeepEqual(got, []memory.ProgramTemplate{a, b}) {
		t.Errorf("Templates(fsa-A) = %+v, want [a b] in id order", got)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	wantTemplate(t, revived, "fsa-A", a)
	wantTemplate(t, revived, "fsa-A", b)
	if got := revived.Templates(ctx, "fsa-A"); !reflect.DeepEqual(got, []memory.ProgramTemplate{a, b}) {
		t.Errorf("revived Templates(fsa-A) = %+v, want [a b]", got)
	}
}

func TestAdminFSATemplate_ReadsAreIndependentCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seededFSA(t)
	tmpl := mkTemplate("derp-1", "MR1", "d", 1, 0, 5000)
	if err := s.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	// The caller's value must not alias the stored one.
	*tmpl.DefaultControlBase.OpModMaxLimW = 1

	got, err := s.Template(ctx, "fsa-A", "derp-1")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	*got.DefaultControlBase.OpModMaxLimW = 2
	listed := s.Templates(ctx, "fsa-A")
	*listed[0].DefaultControlBase.OpModMaxLimW = 3

	again, err := s.Template(ctx, "fsa-A", "derp-1")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if v := *again.DefaultControlBase.OpModMaxLimW; v != 5000 {
		t.Errorf("stored opModMaxLimW = %d, want 5000 untouched by caller mutations", v)
	}
}

func TestAdminFSATemplate_CreateRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seededFSA(t)
	if err := s.CreateTemplate(ctx, "fsa-A", mkTemplate("derp-1", "MR1", "d", 1, 0, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name  string
		fsaID string
		tmpl  memory.ProgramTemplate
		want  error
	}{
		{"absent FSA", "fsa-missing", mkTemplate("derp-2", "MR2", "d", 1, 0, 1), store.ErrNotFound},
		{"duplicate id", "fsa-A", mkTemplate("derp-1", "MR2", "d", 1, 0, 1), store.ErrAlreadyExists},
		{"duplicate mRID", "fsa-A", mkTemplate("derp-2", "MR1", "d", 1, 0, 1), store.ErrAlreadyExists},
		{"empty id", "fsa-A", mkTemplate("", "MR3", "d", 1, 0, 1), memory.ErrInvalidProgramTemplate},
		{"empty mRID", "fsa-A", mkTemplate("derp-3", "", "d", 1, 0, 1), memory.ErrInvalidProgramTemplate},
	}
	for _, tc := range cases {
		if err := s.CreateTemplate(ctx, tc.fsaID, tc.tmpl); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := s.Templates(ctx, "fsa-A"); len(got) != 1 || got[0].ID != "derp-1" || got[0].MRID != "MR1" {
		t.Errorf("Templates(fsa-A) after refusals = %+v, want only derp-1/MR1", got)
	}
}

func TestAdminFSATemplate_UpdateAndReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSA(t)
	if err := s.CreateTemplate(ctx, "fsa-A", mkTemplate("derp-1", "MR1", "before", 1, 0, 5000)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	updated := mkTemplate("derp-1", "MR1", "after", 2, 1, 6000)
	if err := s.UpdateTemplate(ctx, "fsa-A", updated); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	wantTemplate(t, s, "fsa-A", updated)

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	wantTemplate(t, revived, "fsa-A", updated)
}

func TestAdminFSATemplate_UpdateRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seededFSA(t)
	orig := mkTemplate("derp-1", "MR1", "d", 1, 0, 5000)
	if err := s.CreateTemplate(ctx, "fsa-A", orig); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name  string
		fsaID string
		tmpl  memory.ProgramTemplate
		want  error
	}{
		{"absent FSA", "fsa-missing", mkTemplate("derp-1", "MR1", "x", 1, 1, 1), store.ErrNotFound},
		{"absent template", "fsa-A", mkTemplate("derp-9", "MR9", "x", 1, 1, 1), store.ErrNotFound},
		// IEEE 2030.5: on modification the mRID SHALL remain the same.
		{"mRID change", "fsa-A", mkTemplate("derp-1", "MR2", "x", 1, 1, 1), memory.ErrInvalidProgramTemplate},
	}
	for _, tc := range cases {
		if err := s.UpdateTemplate(ctx, tc.fsaID, tc.tmpl); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	wantTemplate(t, s, "fsa-A", orig)
}

func TestAdminFSATemplate_DeleteAndReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSA(t)
	keep := mkTemplate("derp-2", "MR2", "keep", 1, 0, 1)
	for _, tmpl := range []memory.ProgramTemplate{mkTemplate("derp-1", "MR1", "drop", 1, 0, 1), keep} {
		if err := s.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := s.DeleteTemplate(ctx, "fsa-A", "derp-1"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, err := s.Template(ctx, "fsa-A", "derp-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Template(derp-1) after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTemplate(ctx, "fsa-A", "derp-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second DeleteTemplate: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTemplate(ctx, "fsa-missing", "derp-2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteTemplate on absent FSA: err = %v, want ErrNotFound", err)
	}

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if got := revived.Templates(ctx, "fsa-A"); !reflect.DeepEqual(got, []memory.ProgramTemplate{keep}) {
		t.Errorf("revived Templates(fsa-A) = %+v, want only derp-2", got)
	}
}

func TestAdminFSATemplate_DeleteFSAWithTemplatesRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSA(t)
	tmpl := mkTemplate("derp-1", "MR1", "d", 1, 0, 1)
	if err := s.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := s.Delete(ctx, "fsa-A")
	if !errors.Is(err, memory.ErrAdminFSAHasTemplates) {
		t.Fatalf("Delete(fsa-A) with a template: err = %v, want ErrAdminFSAHasTemplates", err)
	}
	// The existing admin route maps ErrAdminFSAInUse to 409; a template
	// must reach that branch rather than the 500 default.
	if !errors.Is(err, memory.ErrAdminFSAInUse) {
		t.Errorf("Delete(fsa-A) with a template: err = %v, want it to match ErrAdminFSAInUse too", err)
	}
	if _, err := s.Get(ctx, "fsa-A"); err != nil {
		t.Errorf("Get(fsa-A) after refused delete: %v", err)
	}
	wantTemplate(t, s, "fsa-A", tmpl)

	revived, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	wantTemplate(t, revived, "fsa-A", tmpl)

	if err := s.DeleteTemplate(ctx, "fsa-A", "derp-1"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if err := s.Delete(ctx, "fsa-A"); err != nil {
		t.Errorf("Delete(fsa-A) once its templates are gone: %v", err)
	}
}

// A snapshot written before templates existed has no "templates" key.
func TestAdminFSATemplate_PreTemplateSnapshotLoadsWithNone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fsas.json")
	legacy := `{"version":1,"records":[{"id":"fsa-A","fsa":{"MRID":"MRa","Description":"a"},"programs":["/p/1"],"devices":["d-1"]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("load pre-template snapshot: %v", err)
	}
	got, err := s.Get(ctx, "fsa-A")
	if err != nil || got.MRID != "MRa" || got.Description != "a" {
		t.Fatalf("Get(fsa-A) = %+v, %v; want MRa/a", got, err)
	}
	if got := s.Templates(ctx, "fsa-A"); len(got) != 0 {
		t.Errorf("Templates(fsa-A) = %+v, want none", got)
	}
	if got := s.Programs(ctx, "fsa-A"); !equalSlices(got, []string{"/p/1"}) {
		t.Errorf("Programs(fsa-A) = %v, want [/p/1]", got)
	}
	if got := s.Devices(ctx, "fsa-A"); !equalSlices(got, []string{"d-1"}) {
		t.Errorf("Devices(fsa-A) = %v, want [d-1]", got)
	}
	if err := s.Delete(ctx, "fsa-A"); !errors.Is(err, memory.ErrAdminFSAInUse) || errors.Is(err, memory.ErrAdminFSAHasTemplates) {
		t.Errorf("Delete(fsa-A) with links and no templates: err = %v, want ErrAdminFSAInUse only", err)
	}
}

func TestAdminFSATemplate_InvalidSnapshotRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty template id": `{"version":1,"records":[{"id":"fsa-A","fsa":{},"templates":[{"id":"","mRID":"MR1"}]}]}`,
		"empty mRID":        `{"version":1,"records":[{"id":"fsa-A","fsa":{},"templates":[{"id":"derp-1","mRID":""}]}]}`,
		"duplicate id":      `{"version":1,"records":[{"id":"fsa-A","fsa":{},"templates":[{"id":"derp-1","mRID":"MR1"},{"id":"derp-1","mRID":"MR2"}]}]}`,
		"duplicate mRID":    `{"version":1,"records":[{"id":"fsa-A","fsa":{},"templates":[{"id":"derp-1","mRID":"MR1"},{"id":"derp-2","mRID":"MR1"}]}]}`,
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "fsas.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("%s: seed: %v", name, err)
		}
		if _, err := memory.NewAdminFSAStoreWithPersistence(path); err == nil {
			t.Errorf("%s: load succeeded, want an error", name)
		}
	}
}

// Each template write mutates memory before persisting; a write that cannot
// reach disk must leave memory exactly as it was.
func TestAdminFSATemplate_WriteRollsBackMemoryWhenPersistFails(t *testing.T) {
	t.Parallel()
	orig := mkTemplate("derp-1", "MR1", "orig", 1, 0, 5000)

	cases := []struct {
		name   string
		mutate func(ctx context.Context, s *memory.AdminFSAStore) error
		want   []memory.ProgramTemplate
	}{
		{
			name: "CreateTemplate",
			mutate: func(ctx context.Context, s *memory.AdminFSAStore) error {
				return s.CreateTemplate(ctx, "fsa-A", mkTemplate("derp-2", "MR2", "new", 1, 0, 1))
			},
			want: []memory.ProgramTemplate{orig},
		},
		{
			name: "UpdateTemplate",
			mutate: func(ctx context.Context, s *memory.AdminFSAStore) error {
				return s.UpdateTemplate(ctx, "fsa-A", mkTemplate("derp-1", "MR1", "changed", 2, 1, 1))
			},
			want: []memory.ProgramTemplate{orig},
		},
		{
			name: "DeleteTemplate",
			mutate: func(ctx context.Context, s *memory.AdminFSAStore) error {
				return s.DeleteTemplate(ctx, "fsa-A", "derp-1")
			},
			want: []memory.ProgramTemplate{orig},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "fsas.json")
			s, err := memory.NewAdminFSAStoreWithPersistence(path)
			if err != nil {
				t.Fatalf("NewAdminFSAStoreWithPersistence: %v", err)
			}
			if err := s.Create(ctx, "fsa-A", mkFSA("MRa", "a", 1)); err != nil {
				t.Fatalf("seed: Create: %v", err)
			}
			if err := s.CreateTemplate(ctx, "fsa-A", orig); err != nil {
				t.Fatalf("seed: CreateTemplate: %v", err)
			}

			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatalf("chmod the snapshot directory read-only: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			if err := tc.mutate(ctx, s); err == nil {
				t.Fatalf("%s succeeded while the snapshot directory was read-only; want an error", tc.name)
			}
			if got := s.Templates(ctx, "fsa-A"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Templates(fsa-A) after a failed %s = %+v, want %+v", tc.name, got, tc.want)
			}

			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatalf("restore write access: %v", err)
			}
			reloaded, err := memory.NewAdminFSAStoreWithPersistence(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := reloaded.Templates(ctx, "fsa-A"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reloaded Templates(fsa-A) after a failed %s = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}
