package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFromFileRejectionSeatsNoPartialRecord is item 3: the black-box
// test in enddevicemanagement_persistence_test.go can only observe that the
// constructor returned an error and a nil store, which holds even if
// loadFromFile itself seats records one at a time as it validates them,
// since the constructor discards whatever state a failing loadFromFile left
// behind and returns nil regardless. This white-box test calls loadFromFile
// directly on a store built with NewEndDeviceManagementStore, the seam the
// constructor's nil-on-error return hides, so a loader that seats the first
// good record before failing on the second is caught here. package memory
// (not memory_test) so the unexported managerOf/managedBy maps are readable
// directly, mirroring the seam the review named.
func TestLoadFromFileRejectionSeatsNoPartialRecord(t *testing.T) {
	t.Parallel()
	const (
		loadStateManagerA = "AAAA000000000000000000000000000000000010"
		loadStateChildA   = "C0A0000000000000000000000000000000000001"
	)
	path := filepath.Join(t.TempDir(), "management.json")
	body := `{"version":1,"records":[{"managerLFDI":"` + loadStateManagerA + `","managedLFDI":"` + loadStateChildA + `"},` +
		`{"managerLFDI":"` + loadStateManagerA + `","managedLFDI":"` + loadStateManagerA + `"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed records: %v", err)
	}

	s := NewEndDeviceManagementStore()
	if err := s.loadFromFile(path); err == nil {
		t.Fatal("loadFromFile with a bad second record: expected an error, got nil")
	}
	if len(s.managerOf) != 0 {
		t.Errorf("managerOf after a rejected load = %v, want empty: the first record must not have been seated", s.managerOf)
	}
	if len(s.managedBy) != 0 {
		t.Errorf("managedBy after a rejected load = %v, want empty: the first record must not have been seated", s.managedBy)
	}
}
