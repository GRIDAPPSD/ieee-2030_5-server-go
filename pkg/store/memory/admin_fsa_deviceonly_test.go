package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #970: a device-only FSA is bound to one device and refuses every other.

func TestDeviceOnlyFSA_RefusesAnotherDevice(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	if err := s.CreateDeviceOnly(ctx, "fsa-d1", mkFSA("fsa-d1", "device-only 1", 0), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignDevice(ctx, "fsa-d1", "2"); !errors.Is(err, memory.ErrAdminFSADeviceOnly) {
		t.Fatalf("assign another device = %v, want ErrAdminFSADeviceOnly", err)
	}
	if got := s.Devices(ctx, "fsa-d1"); len(got) != 0 {
		t.Errorf("devices after the refusal = %v, want none", got)
	}
	if err := s.AssignDevice(ctx, "fsa-d1", "1"); err != nil {
		t.Fatalf("assign the bound device = %v, want nil", err)
	}
	if got := s.DeviceOnlyDevice("fsa-d1"); got != "1" {
		t.Errorf("DeviceOnlyDevice = %q, want 1", got)
	}
	if id, ok := s.DeviceOnlyFSA("1"); !ok || id != "fsa-d1" {
		t.Errorf("DeviceOnlyFSA(1) = %q, %v, want fsa-d1, true", id, ok)
	}
	if id, ok := s.DeviceOnlyFSA("2"); ok {
		t.Errorf("DeviceOnlyFSA(2) = %q, true, want none", id)
	}
}

func TestDeviceOnlyFSA_OrdinaryFSAIsNotDeviceOnly(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	if err := s.Create(ctx, "fsa-A", mkFSA("fsa-A", "roof", 0)); err != nil {
		t.Fatal(err)
	}
	for _, dev := range []string{"1", "2"} {
		if err := s.AssignDevice(ctx, "fsa-A", dev); err != nil {
			t.Fatalf("assign %s: %v", dev, err)
		}
	}
	if got := s.DeviceOnlyDevice("fsa-A"); got != "" {
		t.Errorf("DeviceOnlyDevice(fsa-A) = %q, want empty", got)
	}
}

func TestDeviceOnlyFSA_OneFSAPerDevice(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	if err := s.CreateDeviceOnly(ctx, "a", mkFSA("a", "x", 0), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDeviceOnly(ctx, "b", mkFSA("b", "x", 0), "1"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second device-only FSA for one device = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.Get(ctx, "b"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refused FSA is stored: %v", err)
	}
}

func TestDeviceOnlyFSA_SurvivesRestart(t *testing.T) {
	t.Parallel()
	s, path := newPersistedAdminFSAStore(t)
	ctx := context.Background()
	if err := s.CreateDeviceOnly(ctx, "fsa-d1", mkFSA("fsa-d1", "device-only 1", 0), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignDevice(ctx, "fsa-d1", "1"); err != nil {
		t.Fatal(err)
	}
	re, err := memory.NewAdminFSAStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.DeviceOnlyDevice("fsa-d1"); got != "1" {
		t.Errorf("marker after restart = %q, want 1", got)
	}
	if err := re.AssignDevice(ctx, "fsa-d1", "2"); !errors.Is(err, memory.ErrAdminFSADeviceOnly) {
		t.Errorf("assign another device after restart = %v, want ErrAdminFSADeviceOnly", err)
	}
	if id, ok := re.DeviceOnlyFSA("1"); !ok || id != "fsa-d1" {
		t.Errorf("DeviceOnlyFSA(1) after restart = %q, %v", id, ok)
	}
}

func TestDeviceOnlyFSA_DeleteDropsTheMarker(t *testing.T) {
	t.Parallel()
	s := memory.NewAdminFSAStore()
	ctx := context.Background()
	if err := s.CreateDeviceOnly(ctx, "a", mkFSA("a", "x", 0), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.DeviceOnlyFSA("1"); ok {
		t.Error("marker outlived its FSA")
	}
	if err := s.CreateDeviceOnly(ctx, "b", mkFSA("b", "x", 0), "1"); err != nil {
		t.Errorf("a new device-only FSA after the delete = %v", err)
	}
}

// A snapshot that assigns a second device to a device-only FSA is refused at
// load, not served: the guard is fail-closed on stored data too.
func TestDeviceOnlyFSA_SnapshotWithAnotherDeviceIsRefused(t *testing.T) {
	t.Parallel()
	s, path := newPersistedAdminFSAStore(t)
	ctx := context.Background()
	if err := s.CreateDeviceOnly(ctx, "a", mkFSA("a", "x", 0), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignDevice(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"devices":["1"]`) {
		t.Fatalf("snapshot shape changed, cannot tamper: %s", raw)
	}
	tampered := strings.Replace(string(raw), `"devices":["1"]`, `"devices":["1","2"]`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.NewAdminFSAStoreWithPersistence(path); err == nil {
		t.Fatal("a device-only FSA holding two devices loaded")
	}
}
