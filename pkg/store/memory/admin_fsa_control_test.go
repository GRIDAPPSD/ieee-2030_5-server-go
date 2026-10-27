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

func mkFSAControl(template, id, mrid string, creation int64, lim uint16) memory.FSAControl {
	l := sep2.PerCent(lim)
	return memory.FSAControl{
		TemplateID:   template,
		ID:           id,
		MRID:         mrid,
		CreationTime: creation,
		Start:        creation + 10,
		Duration:     600,
		Description:  "peak",
		Base:         sep2.DERControlBase{OpModMaxLimW: &l},
	}
}

func seededFSAWithTemplates(t *testing.T) (*memory.AdminFSAStore, string) {
	t.Helper()
	s, path := seededFSA(t)
	ctx := context.Background()
	for _, tmpl := range []memory.ProgramTemplate{
		mkTemplate("derp-1", "MR1", "one", 1, 0, 5000),
		mkTemplate("derp-2", "MR2", "two", 2, 0, 5000),
	} {
		if err := s.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
			t.Fatalf("CreateTemplate %s: %v", tmpl.ID, err)
		}
	}
	return s, path
}

func TestAdminFSAControl_CreateListReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSAWithTemplates(t)

	later := mkFSAControl("derp-1", "c-b", "MRB", 200, 4000)
	earlier := mkFSAControl("derp-1", "c-a", "MRA", 100, 3000)
	other := mkFSAControl("derp-2", "c-c", "MRC", 150, 2000)
	for _, c := range []memory.FSAControl{later, earlier, other} {
		if err := s.CreateFSAControl(ctx, "fsa-A", c); err != nil {
			t.Fatalf("CreateFSAControl %s: %v", c.ID, err)
		}
	}
	want := []memory.FSAControl{earlier, later}
	if got := s.FSAControls(ctx, "fsa-A", "derp-1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("FSAControls(derp-1) = %+v, want %+v (oldest creationTime first)", got, want)
	}

	reloaded, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.FSAControls(ctx, "fsa-A", "derp-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded FSAControls(derp-1) = %+v, want %+v", got, want)
	}
	if got := reloaded.FSAControls(ctx, "fsa-A", "derp-2"); !reflect.DeepEqual(got, []memory.FSAControl{other}) {
		t.Errorf("reloaded FSAControls(derp-2) = %+v, want %+v", got, other)
	}
	fsaID, got, ok := reloaded.FSAControlByMRID("mrc")
	if !ok || fsaID != "fsa-A" || !reflect.DeepEqual(got, other) {
		t.Errorf("FSAControlByMRID(mrc) = %q, %+v, %v; want fsa-A and the derp-2 control", fsaID, got, ok)
	}
}

func TestAdminFSAControl_CreateRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seededFSAWithTemplates(t)
	if err := s.CreateFSAControl(ctx, "fsa-A", mkFSAControl("derp-1", "c-a", "MRA", 100, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, tc := range []struct {
		name string
		fsa  string
		c    memory.FSAControl
		want error
	}{
		{"no fsa", "fsa-X", mkFSAControl("derp-1", "c-x", "MRX", 1, 1), store.ErrNotFound},
		{"no template", "fsa-A", mkFSAControl("derp-9", "c-x", "MRX", 1, 1), store.ErrNotFound},
		{"id taken", "fsa-A", mkFSAControl("derp-1", "c-a", "MRX", 1, 1), store.ErrAlreadyExists},
		{"mRID taken on another template", "fsa-A", mkFSAControl("derp-2", "c-x", "mra", 1, 1), store.ErrAlreadyExists},
		{"no id", "fsa-A", mkFSAControl("derp-1", "", "MRX", 1, 1), memory.ErrInvalidFSAControl},
	} {
		if err := s.CreateFSAControl(ctx, tc.fsa, tc.c); !errors.Is(err, tc.want) {
			t.Errorf("%s: CreateFSAControl = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := len(s.FSAControls(ctx, "fsa-A", "derp-2")); got != 0 {
		t.Errorf("derp-2 holds %d controls after refusals, want 0", got)
	}
}

func TestAdminFSAControl_UpdateAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSAWithTemplates(t)
	c := mkFSAControl("derp-1", "c-a", "MRA", 100, 1)
	if err := s.CreateFSAControl(ctx, "fsa-A", c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	at := int64(500)
	c.CancelledAt, c.CancelReason = &at, "operator"
	if err := s.UpdateFSAControl(ctx, "fsa-A", c); err != nil {
		t.Fatalf("UpdateFSAControl: %v", err)
	}
	moved := c
	moved.MRID = "OTHER"
	if err := s.UpdateFSAControl(ctx, "fsa-A", moved); !errors.Is(err, memory.ErrInvalidFSAControl) {
		t.Errorf("UpdateFSAControl changing the mRID = %v, want ErrInvalidFSAControl", err)
	}
	reloaded, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.FSAControl(ctx, "fsa-A", "derp-1", "c-a")
	if err != nil || got.CancelledAt == nil || *got.CancelledAt != 500 || got.CancelReason != "operator" || got.MRID != "MRA" {
		t.Fatalf("reloaded control = %+v, %v; want cancelled at 500 by operator, mRID MRA", got, err)
	}

	if err := s.DeleteFSAControl(ctx, "fsa-A", "derp-1", "c-a"); err != nil {
		t.Fatalf("DeleteFSAControl: %v", err)
	}
	if _, err := s.FSAControl(ctx, "fsa-A", "derp-1", "c-a"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("FSAControl after delete = %v, want ErrNotFound", err)
	}
	if _, _, ok := s.FSAControlByMRID("MRA"); ok {
		t.Error("FSAControlByMRID finds a deleted control")
	}
}

// TestAdminFSAControl_TemplateDeleteTakesItsControls: a template's control
// records go with it, and a failed delete keeps both.
func TestAdminFSAControl_TemplateDeleteTakesItsControls(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "fsas.json")
	s, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Create(ctx, "fsa-A", mkFSA("MRa", "a", 1)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.CreateTemplate(ctx, "fsa-A", mkTemplate("derp-1", "MR1", "one", 1, 0, 1)); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	c := mkFSAControl("derp-1", "c-a", "MRA", 100, 1)
	if err := s.CreateFSAControl(ctx, "fsa-A", c); err != nil {
		t.Fatalf("CreateFSAControl: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.DeleteTemplate(ctx, "fsa-A", "derp-1"); err == nil {
		t.Fatal("DeleteTemplate succeeded on a read-only directory")
	}
	if got := s.FSAControls(ctx, "fsa-A", "derp-1"); !reflect.DeepEqual(got, []memory.FSAControl{c}) {
		t.Fatalf("controls after a failed template delete = %+v, want the one control kept", got)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod back: %v", err)
	}

	if err := s.DeleteTemplate(ctx, "fsa-A", "derp-1"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, _, ok := s.FSAControlByMRID("MRA"); ok {
		t.Error("a control of a deleted template is still found")
	}
	if err := s.CreateTemplate(ctx, "fsa-A", mkTemplate("derp-1", "MR1", "again", 1, 0, 1)); err != nil {
		t.Fatalf("re-create the template: %v", err)
	}
	if got := s.FSAControls(ctx, "fsa-A", "derp-1"); len(got) != 0 {
		t.Errorf("a re-created template inherits %d controls, want 0", len(got))
	}
}

func TestAdminFSAControl_SnapshotNamingNoTemplateRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fsas.json")
	body := `{"version":1,"records":[{"id":"fsa-A","fsa":{"mRID":"MRa"},` +
		`"templates":[{"id":"derp-1","mRID":"MR1","primacy":1,"version":0,"defaultControlBase":{}}],` +
		`"controls":[{"templateId":"derp-9","id":"c","mRID":"M","creationTime":1,"start":1,"duration":1,"derControlBase":{}}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := memory.NewAdminFSAStoreWithPersistence(path); err == nil {
		t.Fatal("a snapshot whose control names no template loaded; want an error")
	}
}

// #1156: a grid share's basis survives a reload, and a read returns a copy
// a caller cannot change the store through.
func TestAdminFSAControl_GridShareReloadsAndCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, path := seededFSAWithTemplates(t)
	c := mkFSAControl("derp-1", "c-g", "MRG", 100, 3000)
	c.Origin = "derms"
	c.GridShare = &memory.GridShare{
		Batch: "b1", Row: 2, AggregatorLFDI: "AAAA", Direction: "charge", RequestValue: 50, RequestMultiplier: 3,
		Devices: 3, WeightW: 15000, TotalWeightW: 20000, DefaultedDevices: []string{"7"}, Units: 38,
	}
	if err := s.CreateFSAControl(ctx, "fsa-A", c); err != nil {
		t.Fatal(err)
	}
	got, err := s.FSAControl(ctx, "fsa-A", "derp-1", "c-g")
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("read = %+v, %v; want %+v", got, err, c)
	}
	got.GridShare.DefaultedDevices[0] = "changed"
	got.GridShare.Units = 0
	if again, _ := s.FSAControl(ctx, "fsa-A", "derp-1", "c-g"); again.GridShare.DefaultedDevices[0] != "7" || again.GridShare.Units != 38 {
		t.Errorf("a change to a read copy reached the store: %+v", again.GridShare)
	}
	reloaded, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := reloaded.FSAControl(ctx, "fsa-A", "derp-1", "c-g"); err != nil || !reflect.DeepEqual(back, c) {
		t.Errorf("reloaded = %+v, %v; want %+v", back, err, c)
	}
}

func TestFSAControl_Live(t *testing.T) {
	t.Parallel()
	c := mkFSAControl("derp-1", "c", "MR", 100, 1)
	end := c.Start + int64(c.Duration)
	if !c.Live(end - 1) {
		t.Error("not live one second before its end")
	}
	if c.Live(end) {
		t.Error("live at its end")
	}
	at := int64(105)
	c.CancelledAt = &at
	if c.Live(end - 1) {
		t.Error("live after a cancel")
	}
}
