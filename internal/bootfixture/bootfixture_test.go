package bootfixture_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

func freshTarget() *bootfixture.Target {
	return &bootfixture.Target{
		EndDevices:         memory.NewEndDeviceStore(),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms:        memory.NewScopedStore[sep2.DERProgram](),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
	}
}

func TestLoadEnphaseFixture(t *testing.T) {
	t.Parallel()

	// Resolve fixture path relative to repo root. tests run from the
	// package dir, so go up two levels.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	path := filepath.Join(repoRoot, "test", "csip", "fixtures", "enphase-edev.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture missing at %s: %v", path, err)
	}

	target := freshTarget()
	if err := bootfixture.Load(context.Background(), target, path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	list, err := target.EndDevices.List(context.Background(), store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("EndDevices.List: %v", err)
	}
	if got := list.All; got != 1 {
		t.Fatalf("expected exactly 1 EndDevice total, got %d", got)
	}
	if got := len(list.Items); got != 1 {
		t.Fatalf("expected exactly 1 EndDevice returned, got %d", got)
	}
	dev := list.Items[0]
	if want := "9711948EBF52B988A39728F756AEAA763EFF5540"; dev.LFDI != want {
		t.Errorf("LFDI = %q, want %q", dev.LFDI, want)
	}
	if want := "405521881394"; dev.SFDI != want {
		t.Errorf("SFDI = %q, want %q", dev.SFDI, want)
	}
	if dev.Enabled == nil || !*dev.Enabled {
		t.Errorf("Enabled = %v, want true", dev.Enabled)
	}
	if want := "/edev/enphase"; dev.Href != want {
		t.Errorf("Href = %q, want %q", dev.Href, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()

	target := freshTarget()
	err := bootfixture.Load(context.Background(), target, filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing fixture, got nil")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("error chain does not include *os.PathError: %v", err)
	}
	if !strings.Contains(err.Error(), "missing.yaml") {
		t.Errorf("error %q does not cite the missing path", err)
	}
}

func TestLoadNilTarget(t *testing.T) {
	t.Parallel()
	err := bootfixture.Load(context.Background(), nil, "irrelevant.yaml")
	if err == nil {
		t.Fatal("expected error for nil target, got nil")
	}
	if !strings.Contains(err.Error(), "target is nil") {
		t.Errorf("error %q does not mention nil target", err)
	}
}

func TestLoadEmptyPath(t *testing.T) {
	t.Parallel()
	err := bootfixture.Load(context.Background(), freshTarget(), "")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestLoadOrphanFSA(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "orphan.yaml")
	yaml := []byte(`
end_devices:
  - id: "a"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
fsas:
  - id: "fsa-1"
    end_device_id: "missing"
`)
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	err := bootfixture.Load(context.Background(), freshTarget(), path)
	if err == nil {
		t.Fatal("expected orphan-FSA error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown end_device_id") {
		t.Errorf("error %q does not flag the orphan", err)
	}
}

func TestLoadUnknownYAMLKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "typo.yaml")
	// "end_device" is a typo for "end_devices". Strict decode should reject.
	yaml := []byte(`
end_device:
  - id: "a"
`)
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	err := bootfixture.Load(context.Background(), freshTarget(), path)
	if err == nil {
		t.Fatal("expected strict-decode error, got nil")
	}
}
