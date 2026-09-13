package bootfixture_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeFiles captures the persisted store files by name and bytes.
func (h *seedHarness) storeFiles() map[string][]byte {
	h.t.Helper()
	files := make(map[string][]byte, 2)
	for _, name := range []string{endDevicesFile, derProgramsFile} {
		raw, err := os.ReadFile(h.path(name))
		if err != nil {
			h.t.Fatalf("read %s: %v", name, err)
		}
		files[name] = raw
	}
	return files
}

func (h *seedHarness) assertStoreFilesUnchanged(before map[string][]byte) {
	h.t.Helper()
	for name, got := range h.storeFiles() {
		if !bytes.Equal(got, before[name]) {
			h.t.Errorf("store file %s changed:\nbefore: %s\nafter:  %s", name, before[name], got)
		}
	}
}

// Criterion 2: a store write that fails during apply leaves no seed record,
// so the next boot still adopts what was written and creates the rest.
func TestReconcileApplyFailureWritesNoSeedRecord(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	// A non-empty directory at the DERProgram snapshot's temporary path fails
	// that write after the EndDevice snapshot has succeeded.
	blocker := h.path(derProgramsFile + ".tmp")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatalf("create blocker: %v", err)
	}

	_, err := h.boot(fixtureV1)
	if err == nil {
		t.Fatal("boot with a failing DERProgram write succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `der_programs[0] (id="p1")`) {
		t.Errorf("error %q does not name der_programs[0]", err)
	}
	if _, err := os.Stat(h.path(seedRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("seed record after a failed apply: stat err = %v, want not exist", err)
	}
	edevs, derps := h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	_, err = derps.Get(ctx, "e1", "p1")
	assertNotFound(t, err, "DERProgram (e1, p1) after its write failed")

	if err := os.RemoveAll(blocker); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	h.mustBoot(fixtureV1)

	edevs, derps = h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	assertEndDeviceCount(t, edevs, 1)
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}

// A seed record that exists but cannot be read fails the boot. Reading it as
// an empty set would recreate the record deleted since it was seeded.
func TestReconcileUnreadableSeedRecordFailsClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		makeBroken func(t *testing.T, path string)
	}{
		{
			name: "directory at the path",
			makeBroken: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove seed record: %v", err)
				}
				if err := os.MkdirAll(filepath.Join(path, "keep"), 0o700); err != nil {
					t.Fatalf("create directory: %v", err)
				}
			},
		},
		{
			name: "mode 000",
			makeBroken: func(t *testing.T, path string) {
				if os.Getuid() == 0 {
					t.Skip("permission checks do not apply when running as root")
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("chmod seed record: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)
			ctx := context.Background()

			h.mustBoot(fixtureV1)
			edevs, _ := h.reopen()
			if err := edevs.Delete(ctx, "e1"); err != nil {
				t.Fatalf("operator Delete e1: %v", err)
			}
			tc.makeBroken(t, h.path(seedRecordFile))
			before := h.storeFiles()

			_, err := h.boot(fixtureV1)
			if err == nil {
				t.Fatal("boot with an unreadable seed record succeeded, want an error")
			}
			if !strings.Contains(err.Error(), seedRecordFile) {
				t.Errorf("error %q does not cite the seed record path", err)
			}
			h.assertStoreFilesUnchanged(before)
			edevs, _ = h.reopen()
			_, err = edevs.Get(ctx, "e1")
			assertNotFound(t, err, "EndDevice e1 deleted before the failed boot")
		})
	}
}

// Criterion 2: a key stays in the seed record after its record leaves the
// fixture, so putting the record back does not resurrect a delete.
func TestReconcileKeepsKeyAfterRecordLeavesFixture(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	h.mustBoot(fixtureV1)
	_, derps := h.reopen()
	if err := derps.Delete(ctx, "e1", "p1"); err != nil {
		t.Fatalf("operator Delete (e1, p1): %v", err)
	}

	e3 := `
  - id: e3
    sfdi: "333333333333"
    lfdi: "` + lfdiE3 + `"
    changed_time: 300
`
	withoutP1 := `
end_devices:
  - id: e1
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    enabled: true
    changed_time: 100` + e3
	// Adding e3 makes this boot rewrite the seed record, so a writer that
	// rebuilt the set from the current fixture would drop p1 here.
	h.mustBoot(withoutP1)
	h.assertSeedKeys(edevKey("e1"), edevKey("e3"), derpKey("e1", "p1"))

	h.mustBoot(withoutP1 + `der_programs:
  - end_device_id: e1
    id: p1
    primacy: 3
`)
	_, derps = h.reopen()
	_, err := derps.Get(ctx, "e1", "p1")
	assertNotFound(t, err, "DERProgram (e1, p1) put back in the fixture after a delete")
	h.assertSeedKeys(edevKey("e1"), edevKey("e3"), derpKey("e1", "p1"))
	h.requireLog(`DERProgram edev="e1" id="p1"`, "deleted since seeded")
}

// A fixture that fails its own checks stops a data_dir boot before any store
// or seed record write.
func TestReconcileInvalidFixtureFailsBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		name    string
		fixture string
		wantMsg string
	}{
		{
			name: "duplicate EndDevice id",
			fixture: `
end_devices:
  - id: e4
    sfdi: "444444444444"
    lfdi: "` + lfdiE3 + `"
    changed_time: 400
  - id: e4
    sfdi: "444444444444"
    lfdi: "` + lfdiE3 + `"
    changed_time: 400
`,
			wantMsg: `end_devices[1]: duplicate id "e4"`,
		},
		{
			name: "DERProgram under an unknown EndDevice",
			fixture: fixtureV1 + `  - end_device_id: nope
    id: p9
    primacy: 9
`,
			wantMsg: `der_programs[1] (id="p9"): unknown end_device_id "nope"`,
		},
		{
			name: "FSA under an unknown EndDevice",
			fixture: fixtureV1 + `fsas:
  - end_device_id: nope
    id: f9
`,
			wantMsg: `fsas[0] (id="f9"): unknown end_device_id "nope"`,
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)

			h.mustBoot(fixtureV1)
			before := h.snapshotDataDir()

			_, err := h.boot(tc.fixture)
			if err == nil {
				t.Fatal("boot with an invalid fixture succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tc.wantMsg)
			}
			h.assertDataDirUnchanged(before)
			edevs, derps := h.reopen()
			assertEndDeviceCount(t, edevs, 1)
			assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
			assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
		})
	}
}
