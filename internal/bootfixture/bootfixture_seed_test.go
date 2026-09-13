package bootfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// File names under a data_dir, as internal/server derives them through
// config.EffectiveStorePath.
const (
	endDevicesFile  = "enddevices.json"
	derProgramsFile = "derprograms.json"
	seedRecordFile  = "bootfixture-seed.json"
)

const (
	lfdiE1    = "E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1"
	lfdiE2    = "E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2E2"
	lfdiE3    = "E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3E3"
	lfdiOther = "0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F"
)

var allLFDIs = []string{lfdiE1, lfdiE2, lfdiE3, lfdiOther}

const fixtureV1 = `
end_devices:
  - id: e1
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    enabled: true
    changed_time: 100
der_programs:
  - end_device_id: e1
    id: p1
    mrid: "A1A1A1A1A1A1A1A1"
    description: fixture program p1
    primacy: 3
`

// seedEnvelope mirrors the seed record's on-disk shape through an independent
// declaration, so a writer and reader that agree on a wrong shape still fail.
type seedEnvelope struct {
	Version int       `json:"version"`
	Records []seedKey `json:"records"`
}

type seedKey struct {
	Kind   string `json:"kind"`
	Parent string `json:"parent"`
	ID     string `json:"id"`
}

type seedHarness struct {
	t          *testing.T
	dataDir    string
	fixtureDir string
	boots      int

	mu   sync.Mutex
	logs []string
}

func newSeedHarness(t *testing.T) *seedHarness {
	t.Helper()
	// The fixture lives outside the data_dir so the byte-identical check
	// covers exactly what persistence owns.
	return &seedHarness{t: t, dataDir: t.TempDir(), fixtureDir: t.TempDir()}
}

func (h *seedHarness) path(name string) string { return filepath.Join(h.dataDir, name) }

func (h *seedHarness) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

// boot is one server start: the persisted stores are opened from the data_dir
// files, the in-memory stores are fresh, and the fixture is reconciled.
func (h *seedHarness) boot(fixture string) (*bootfixture.Target, error) {
	h.t.Helper()
	return h.bootWithSeedPath(fixture, h.path(seedRecordFile))
}

func (h *seedHarness) bootWithSeedPath(fixture, seedPath string) (*bootfixture.Target, error) {
	h.t.Helper()
	edevs, err := memory.NewEndDeviceStoreWithPersistence(h.path(endDevicesFile))
	if err != nil {
		return nil, fmt.Errorf("open EndDevice store: %w", err)
	}
	derps, err := memory.NewDERProgramStoreWithPersistence(h.path(derProgramsFile))
	if err != nil {
		return nil, fmt.Errorf("open DERProgram store: %w", err)
	}
	target := freshTarget()
	target.EndDevices = edevs
	target.DERPrograms = derps

	h.boots++
	fixturePath := filepath.Join(h.fixtureDir, fmt.Sprintf("fixture-%d.yaml", h.boots))
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		h.t.Fatalf("write fixture: %v", err)
	}
	return target, bootfixture.Reconcile(context.Background(), target, fixturePath, seedPath, h.logf)
}

func (h *seedHarness) mustBoot(fixture string) *bootfixture.Target {
	h.t.Helper()
	target, err := h.boot(fixture)
	if err != nil {
		h.t.Fatalf("boot %d: %v", h.boots, err)
	}
	return target
}

// reopen returns fresh store instances on the data_dir files, which is the
// path the next boot reads by.
func (h *seedHarness) reopen() (*memory.EndDeviceStore, *memory.DERProgramStore) {
	h.t.Helper()
	edevs, err := memory.NewEndDeviceStoreWithPersistence(h.path(endDevicesFile))
	if err != nil {
		h.t.Fatalf("reopen EndDevice store: %v", err)
	}
	derps, err := memory.NewDERProgramStoreWithPersistence(h.path(derProgramsFile))
	if err != nil {
		h.t.Fatalf("reopen DERProgram store: %v", err)
	}
	return edevs, derps
}

func (h *seedHarness) seedKeys() []seedKey {
	h.t.Helper()
	raw, err := os.ReadFile(h.path(seedRecordFile))
	if err != nil {
		h.t.Fatalf("read seed record: %v", err)
	}
	var env seedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		h.t.Fatalf("decode seed record: %v\n%s", err, raw)
	}
	if env.Version != 1 {
		h.t.Errorf("seed record version = %d, want 1", env.Version)
	}
	sortSeedKeys(env.Records)
	return env.Records
}

func (h *seedHarness) assertSeedKeys(want ...seedKey) {
	h.t.Helper()
	sortSeedKeys(want)
	if got := h.seedKeys(); !slices.Equal(got, want) {
		h.t.Errorf("seed record keys = %v, want %v", got, want)
	}
}

// snapshotDataDir captures every file in the data_dir by name and bytes.
func (h *seedHarness) snapshotDataDir() map[string][]byte {
	h.t.Helper()
	entries, err := os.ReadDir(h.dataDir)
	if err != nil {
		h.t.Fatalf("read data_dir: %v", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(h.dataDir, e.Name()))
		if err != nil {
			h.t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = raw
	}
	return files
}

func (h *seedHarness) assertDataDirUnchanged(before map[string][]byte) {
	h.t.Helper()
	after := h.snapshotDataDir()
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			h.t.Errorf("data_dir file %s was removed", name)
			continue
		}
		if !bytes.Equal(got, want) {
			h.t.Errorf("data_dir file %s changed:\nbefore: %s\nafter:  %s", name, want, got)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			h.t.Errorf("data_dir file %s was created", name)
		}
	}
}

// requireLog fails unless one log line holds every part.
func (h *seedHarness) requireLog(parts ...string) {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, line := range h.logs {
		if containsAll(line, parts) {
			return
		}
	}
	h.t.Errorf("no log line contains %q; logs: %q", parts, h.logs)
}

func (h *seedHarness) assertNoLFDIInLogs() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, line := range h.logs {
		assertNoLFDI(h.t, line)
	}
}

func assertNoLFDI(t *testing.T, text string) {
	t.Helper()
	upper := strings.ToUpper(text)
	for _, lfdi := range allLFDIs {
		if strings.Contains(upper, lfdi) {
			t.Errorf("text carries an LFDI: %q", text)
		}
	}
}

func containsAll(s string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func sortSeedKeys(keys []seedKey) {
	slices.SortFunc(keys, func(a, b seedKey) int {
		return strings.Compare(a.Kind+"\x00"+a.Parent+"\x00"+a.ID, b.Kind+"\x00"+b.Parent+"\x00"+b.ID)
	})
}

func edevKey(id string) seedKey { return seedKey{Kind: "EndDevice", ID: id} }

func derpKey(parent, id string) seedKey {
	return seedKey{Kind: "DERProgram", Parent: parent, ID: id}
}

func mustEndDevice(t *testing.T, s store.EndDeviceStore, id string) sep2.EndDevice {
	t.Helper()
	dev, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("EndDevice %q: %v", id, err)
	}
	return dev
}

func mustProgram(t *testing.T, s store.ScopedStore[sep2.DERProgram], parent, id string) sep2.DERProgram {
	t.Helper()
	prog, err := s.Get(context.Background(), parent, id)
	if err != nil {
		t.Fatalf("DERProgram (%q, %q): %v", parent, id, err)
	}
	return prog
}

func assertNotFound(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%s: err = %v, want store.ErrNotFound", what, err)
	}
}

func assertEndDeviceCount(t *testing.T, s store.EndDeviceStore, want uint32) {
	t.Helper()
	n, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("EndDevice Count: %v", err)
	}
	if n != want {
		t.Errorf("EndDevice Count = %d, want %d", n, want)
	}
}

func enabledValue(t *testing.T, dev sep2.EndDevice) bool {
	t.Helper()
	return deref(t, dev.Enabled, "EndDevice.Enabled")
}

func assertFixtureE1(t *testing.T, dev sep2.EndDevice) {
	t.Helper()
	if dev.LFDI != lfdiE1 {
		t.Errorf("e1 LFDI = %q, want %q", dev.LFDI, lfdiE1)
	}
	if dev.SFDI != "111111111111" {
		t.Errorf("e1 SFDI = %q, want 111111111111", dev.SFDI)
	}
	if !enabledValue(t, dev) {
		t.Error("e1 Enabled = false, want the fixture's true")
	}
	if dev.ChangedTime != 100 {
		t.Errorf("e1 ChangedTime = %d, want 100", dev.ChangedTime)
	}
	if dev.Href != "/edev/e1" {
		t.Errorf("e1 Href = %q, want /edev/e1", dev.Href)
	}
}

func assertFixtureP1(t *testing.T, prog sep2.DERProgram) {
	t.Helper()
	if prog.Primacy != 3 {
		t.Errorf("p1 Primacy = %d, want 3", prog.Primacy)
	}
	if prog.MRID != "A1A1A1A1A1A1A1A1" {
		t.Errorf("p1 MRID = %q, want A1A1A1A1A1A1A1A1", prog.MRID)
	}
	if prog.Href != "/edev/e1/derp/p1" {
		t.Errorf("p1 Href = %q, want /edev/e1/derp/p1", prog.Href)
	}
}

// Criterion 1: the second boot against the same data_dir succeeds and holds
// each fixture record once, with the fixture's values.
func TestReconcileSecondBootKeepsFixtureRecords(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)

	h.mustBoot(fixtureV1)
	h.mustBoot(fixtureV1)

	edevs, derps := h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	assertEndDeviceCount(t, edevs, 1)
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	if n, err := derps.Count(context.Background(), "e1"); err != nil || n != 1 {
		t.Errorf("DERProgram Count(e1) = %d, %v, want 1, nil", n, err)
	}
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}

// Criterion 2a: edits made between boots survive the next boot.
func TestReconcileKeepsEditsMadeBetweenBoots(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	h.mustBoot(fixtureV1)

	edevs, derps := h.reopen()
	dev := mustEndDevice(t, edevs, "e1")
	disabled := false
	dev.Enabled = &disabled
	dev.ChangedTime = 200
	if err := edevs.Update(ctx, "e1", dev); err != nil {
		t.Fatalf("operator Update e1: %v", err)
	}
	prog := mustProgram(t, derps, "e1", "p1")
	prog.Primacy = 7
	if err := derps.Update(ctx, "e1", "p1", prog); err != nil {
		t.Fatalf("operator Update p1: %v", err)
	}

	h.mustBoot(fixtureV1)

	edevs, derps = h.reopen()
	got := mustEndDevice(t, edevs, "e1")
	if enabledValue(t, got) {
		t.Error("e1 Enabled = true, want the edited false")
	}
	if got.ChangedTime != 200 {
		t.Errorf("e1 ChangedTime = %d, want the edited 200", got.ChangedTime)
	}
	if got.LFDI != lfdiE1 {
		t.Errorf("e1 LFDI = %q, want %q", got.LFDI, lfdiE1)
	}
	gotProg := mustProgram(t, derps, "e1", "p1")
	if gotProg.Primacy != 7 {
		t.Errorf("p1 Primacy = %d, want the edited 7", gotProg.Primacy)
	}
	if gotProg.MRID != "A1A1A1A1A1A1A1A1" {
		t.Errorf("p1 MRID = %q, want A1A1A1A1A1A1A1A1", gotProg.MRID)
	}
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}

const fixtureWithChildren = `
end_devices:
  - id: e1
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    enabled: true
    changed_time: 100
  - id: e2
    sfdi: "222222222222"
    lfdi: "` + lfdiE2 + `"
    changed_time: 100
fsas:
  - end_device_id: e1
    id: f1
    description: fsa under e1
  - end_device_id: e2
    id: f2
    description: fsa under e2
der_programs:
  - end_device_id: e1
    fsa_id: f1
    id: p1
    mrid: "A1A1A1A1A1A1A1A1"
    primacy: 3
  - end_device_id: e1
    fsa_id: f1
    id: p3
    primacy: 4
  - end_device_id: e2
    fsa_id: f2
    id: p2
    primacy: 5
default_der_controls:
  - end_device_id: e1
    fsa_id: f1
    der_program_id: p1
    mrid: "D1D1D1D1D1D1D1D1"
  - end_device_id: e1
    fsa_id: f1
    der_program_id: p3
    mrid: "D3D3D3D3D3D3D3D3"
  - end_device_id: e2
    fsa_id: f2
    der_program_id: p2
    mrid: "D2D2D2D2D2D2D2D2"
der_controls:
  - end_device_id: e1
    fsa_id: f1
    der_program_id: p1
    id: c1
  - end_device_id: e1
    fsa_id: f1
    der_program_id: p3
    id: c3
  - end_device_id: e2
    fsa_id: f2
    der_program_id: p2
    id: c2
der_curves:
  - id: k1
    description: fixture curve
    curve_type: 0
`

// Criteria 2b and 3: deleted records stay deleted, fixture children under a
// deleted EndDevice or DERProgram are not created, and the remaining in-memory
// records still load.
func TestReconcileKeepsDeletesAndSkipsChildren(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	h.mustBoot(fixtureWithChildren)

	edevs, derps := h.reopen()
	if err := edevs.Delete(ctx, "e2"); err != nil {
		t.Fatalf("operator Delete e2: %v", err)
	}
	if err := derps.Delete(ctx, "e2", "p2"); err != nil {
		t.Fatalf("operator Delete (e2, p2): %v", err)
	}
	if err := derps.Delete(ctx, "e1", "p3"); err != nil {
		t.Fatalf("operator Delete (e1, p3): %v", err)
	}

	target := h.mustBoot(fixtureWithChildren)

	edevs, derps = h.reopen()
	_, err := edevs.Get(ctx, "e2")
	assertNotFound(t, err, "EndDevice e2")
	assertEndDeviceCount(t, edevs, 1)
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	_, err = derps.Get(ctx, "e2", "p2")
	assertNotFound(t, err, "DERProgram (e2, p2)")
	_, err = derps.Get(ctx, "e1", "p3")
	assertNotFound(t, err, "DERProgram (e1, p3)")
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))

	fsa, err := target.FSAs.Get(ctx, "e1", "f1")
	if err != nil {
		t.Fatalf("FSA (e1, f1): %v", err)
	}
	if fsa.Href != "/edev/e1/fsa/f1" || fsa.Description != "fsa under e1" {
		t.Errorf("FSA (e1, f1) = href %q description %q, want /edev/e1/fsa/f1 and the fixture description", fsa.Href, fsa.Description)
	}
	_, err = target.FSAs.Get(ctx, "e2", "f2")
	assertNotFound(t, err, "FSA (e2, f2) under deleted e2")

	ctl, err := target.DERControls.Get(ctx, "e1/f1/p1", "c1")
	if err != nil {
		t.Fatalf("DERControl c1: %v", err)
	}
	if ctl.Href != "/edev/e1/fsa/f1/derp/p1/derc/c1" {
		t.Errorf("DERControl c1 Href = %q", ctl.Href)
	}
	_, err = target.DERControls.Get(ctx, "e1/f1/p3", "c3")
	assertNotFound(t, err, "DERControl c3 under deleted p3")
	_, err = target.DERControls.Get(ctx, "e2/f2/p2", "c2")
	assertNotFound(t, err, "DERControl c2 under deleted e2")

	dflt, err := target.DefaultDERControls.Get(ctx, "e1/f1/p1", dderControlSingletonKey)
	if err != nil {
		t.Fatalf("DefaultDERControl under p1: %v", err)
	}
	if dflt.MRID != "D1D1D1D1D1D1D1D1" {
		t.Errorf("DefaultDERControl under p1 MRID = %q, want D1D1D1D1D1D1D1D1", dflt.MRID)
	}
	_, err = target.DefaultDERControls.Get(ctx, "e1/f1/p3", dderControlSingletonKey)
	assertNotFound(t, err, "DefaultDERControl under deleted p3")
	_, err = target.DefaultDERControls.Get(ctx, "e2/f2/p2", dderControlSingletonKey)
	assertNotFound(t, err, "DefaultDERControl under deleted e2")

	curve, err := target.DERCurves.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("DERCurve k1: %v", err)
	}
	if curve.Href != "/dc/k1" || curve.Description != "fixture curve" {
		t.Errorf("DERCurve k1 = href %q description %q", curve.Href, curve.Description)
	}

	h.assertSeedKeys(edevKey("e1"), edevKey("e2"),
		derpKey("e1", "p1"), derpKey("e1", "p3"), derpKey("e2", "p2"))

	h.requireLog(`EndDevice id="e2"`, "deleted since seeded")
	h.requireLog(`DERProgram edev="e1" id="p3"`, "deleted since seeded")
	h.requireLog(`DERProgram edev="e2" id="p2"`, "parent EndDevice skipped")
	h.assertNoLFDIInLogs()
}

// Criterion 2c: a record added in a later fixture version is created.
func TestReconcileCreatesRecordsAddedInLaterFixture(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)

	h.mustBoot(fixtureV1)

	fixtureV2 := `
end_devices:
  - id: e1
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    enabled: true
    changed_time: 100
  - id: e3
    sfdi: "333333333333"
    lfdi: "` + lfdiE3 + `"
    enabled: false
    changed_time: 300
der_programs:
  - end_device_id: e1
    id: p1
    mrid: "A1A1A1A1A1A1A1A1"
    description: fixture program p1
    primacy: 3
  - end_device_id: e1
    id: p9
    mrid: "A9A9A9A9A9A9A9A9"
    primacy: 9
`
	h.mustBoot(fixtureV2)

	edevs, derps := h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	e3 := mustEndDevice(t, edevs, "e3")
	if e3.LFDI != lfdiE3 || e3.SFDI != "333333333333" || e3.ChangedTime != 300 || e3.Href != "/edev/e3" {
		t.Errorf("e3 = LFDI %q SFDI %q ChangedTime %d Href %q, want the v2 fixture values", e3.LFDI, e3.SFDI, e3.ChangedTime, e3.Href)
	}
	if enabledValue(t, e3) {
		t.Error("e3 Enabled = true, want the v2 fixture's false")
	}
	assertEndDeviceCount(t, edevs, 2)
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	p9 := mustProgram(t, derps, "e1", "p9")
	if p9.Primacy != 9 || p9.MRID != "A9A9A9A9A9A9A9A9" || p9.Href != "/edev/e1/derp/p9" {
		t.Errorf("p9 = Primacy %d MRID %q Href %q, want the v2 fixture values", p9.Primacy, p9.MRID, p9.Href)
	}
	h.assertSeedKeys(edevKey("e1"), edevKey("e3"), derpKey("e1", "p1"), derpKey("e1", "p9"))
}

// Criterion 2d: persisted records with no seed record are adopted unchanged,
// and the EndDevice LFDI match ignores case.
func TestReconcileAdoptsMatchingPersistedRecords(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	edevs, derps := h.reopen()
	disabled := false
	pre := sep2.EndDevice{LFDI: strings.ToLower(lfdiE1), SFDI: "111111111111", ChangedTime: 50, Enabled: &disabled}
	pre.Href = "/edev/e1"
	if err := edevs.Create(ctx, "e1", pre); err != nil {
		t.Fatalf("pre-populate e1: %v", err)
	}
	preProg := sep2.DERProgram{MRID: "B5B5B5B5B5B5B5B5", Primacy: 5}
	preProg.Href = "/edev/e1/derp/p1"
	if err := derps.Create(ctx, "e1", "p1", preProg); err != nil {
		t.Fatalf("pre-populate p1: %v", err)
	}

	h.mustBoot(fixtureV1)

	edevs, derps = h.reopen()
	got := mustEndDevice(t, edevs, "e1")
	if got.LFDI != strings.ToLower(lfdiE1) || got.ChangedTime != 50 || enabledValue(t, got) {
		t.Errorf("e1 = LFDI %q ChangedTime %d Enabled %v, want the pre-populated record unchanged", got.LFDI, got.ChangedTime, *got.Enabled)
	}
	assertEndDeviceCount(t, edevs, 1)
	gotProg := mustProgram(t, derps, "e1", "p1")
	if gotProg.Primacy != 5 || gotProg.MRID != "B5B5B5B5B5B5B5B5" {
		t.Errorf("p1 = Primacy %d MRID %q, want the pre-populated 5 and B5B5B5B5B5B5B5B5", gotProg.Primacy, gotProg.MRID)
	}
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}

// Criterion 2f: an id deleted after seeding and reallocated to another device
// is treated as deleted on every later boot.
func TestReconcileTreatsReusedIDAsDeleted(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()

	fixtureBoot1 := `
end_devices:
  - id: "1"
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    changed_time: 100
`
	fixtureLater := fixtureBoot1 + `fsas:
  - end_device_id: "1"
    id: f1
der_programs:
  - end_device_id: "1"
    fsa_id: f1
    id: p1
    primacy: 3
der_controls:
  - end_device_id: "1"
    fsa_id: f1
    der_program_id: p1
    id: c1
`
	h.mustBoot(fixtureBoot1)

	edevs, _ := h.reopen()
	if err := edevs.Delete(ctx, "1"); err != nil {
		t.Fatalf("operator Delete 1: %v", err)
	}
	disabled := false
	other := sep2.EndDevice{LFDI: lfdiOther, SFDI: "999999999999", ChangedTime: 900, Enabled: &disabled}
	other.Href = "/edev/1"
	if err := edevs.Create(ctx, "1", other); err != nil {
		t.Fatalf("reallocate id 1: %v", err)
	}

	for _, bootName := range []string{"boot 2", "boot 3"} {
		target, err := h.boot(fixtureLater)
		if err != nil {
			t.Fatalf("%s: %v", bootName, err)
		}
		edevs, derps := h.reopen()
		got := mustEndDevice(t, edevs, "1")
		if got.LFDI != lfdiOther || got.SFDI != "999999999999" || got.ChangedTime != 900 || enabledValue(t, got) {
			t.Errorf("%s: id 1 = LFDI %q SFDI %q ChangedTime %d, want the other device unchanged", bootName, got.LFDI, got.SFDI, got.ChangedTime)
		}
		assertEndDeviceCount(t, edevs, 1)
		_, err = derps.Get(ctx, "1", "p1")
		assertNotFound(t, err, bootName+": DERProgram (1, p1)")
		_, err = target.FSAs.Get(ctx, "1", "f1")
		assertNotFound(t, err, bootName+": FSA (1, f1)")
		_, err = target.DERControls.Get(ctx, "1/f1/p1", "c1")
		assertNotFound(t, err, bootName+": DERControl c1")
		h.assertSeedKeys(edevKey("1"), derpKey("1", "p1"))
	}
	h.requireLog(`EndDevice id="1"`, "id now held by another identity")
	h.assertNoLFDIInLogs()
}

// Criterion 2e: a fixture EndDevice colliding with a persisted identity stops
// the boot before any store or seed record write, even when an earlier fixture
// record could have been created.
func TestReconcileIdentityConflictFailsBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	fixture := `
end_devices:
  - id: e0
    sfdi: "333333333333"
    lfdi: "` + lfdiE3 + `"
    changed_time: 100
  - id: e1
    sfdi: "111111111111"
    lfdi: "` + lfdiE1 + `"
    changed_time: 100
der_programs:
  - end_device_id: e0
    id: p0
    primacy: 1
  - end_device_id: e1
    id: p1
    primacy: 3
`
	cases := []struct {
		name       string
		holderID   string
		holderLFDI string
		wantMsg    []string
	}{
		{
			name:       "id held by a different LFDI",
			holderID:   "e1",
			holderLFDI: lfdiOther,
			wantMsg:    []string{`end_devices[1] (id="e1")`},
		},
		{
			name:       "LFDI held by a different id",
			holderID:   "x",
			holderLFDI: strings.ToLower(lfdiE1),
			wantMsg:    []string{`end_devices[1] (id="e1")`, `"/edev/x"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)
			ctx := context.Background()

			edevs, derps := h.reopen()
			holder := sep2.EndDevice{LFDI: tc.holderLFDI, SFDI: "999999999999", ChangedTime: 7}
			holder.Href = "/edev/" + tc.holderID
			if err := edevs.Create(ctx, tc.holderID, holder); err != nil {
				t.Fatalf("pre-populate holder: %v", err)
			}
			if err := derps.Create(ctx, tc.holderID, "pz", sep2.DERProgram{Primacy: 9}); err != nil {
				t.Fatalf("pre-populate holder program: %v", err)
			}
			before := h.snapshotDataDir()

			_, err := h.boot(fixture)
			if err == nil {
				t.Fatal("boot succeeded, want an identity conflict")
			}
			if !errors.Is(err, bootfixture.ErrIdentityConflict) {
				t.Errorf("error chain lacks ErrIdentityConflict: %v", err)
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			assertNoLFDI(t, err.Error())

			h.assertDataDirUnchanged(before)
			edevs, derps = h.reopen()
			_, err = edevs.Get(ctx, "e0")
			assertNotFound(t, err, "EndDevice e0")
			assertEndDeviceCount(t, edevs, 1)
			got := mustEndDevice(t, edevs, tc.holderID)
			if got.LFDI != tc.holderLFDI || got.ChangedTime != 7 {
				t.Errorf("holder = LFDI %q ChangedTime %d, want it unchanged", got.LFDI, got.ChangedTime)
			}
			_, err = derps.Get(ctx, "e0", "p0")
			assertNotFound(t, err, "DERProgram (e0, p0)")
		})
	}
}

// Criterion 2: the seed record is written only after the store writes it
// covers. A failed seed record write leaves created records that the next
// boot adopts.
func TestReconcileWritesSeedRecordAfterStores(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)

	unwritable := filepath.Join(h.dataDir, "no-such-dir", seedRecordFile)
	if _, err := h.bootWithSeedPath(fixtureV1, unwritable); err == nil {
		t.Fatal("boot with an unwritable seed record path succeeded, want an error")
	}

	edevs, derps := h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	if _, err := os.Stat(h.path(seedRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("seed record at the real path: stat err = %v, want not exist", err)
	}

	h.mustBoot(fixtureV1)

	edevs, derps = h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	assertEndDeviceCount(t, edevs, 1)
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}

// A seed record that cannot be trusted fails the boot before any write: an
// empty or unreadable set would resurrect every record the operator deleted.
func TestReconcileRejectsUntrustedSeedRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: "{"},
		{name: "unknown version", body: `{"version":2,"records":[]}`},
		{name: "empty file", body: ""},
		{name: "unknown kind", body: `{"version":1,"records":[{"kind":"Widget","parent":"","id":"e1"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)
			if err := os.WriteFile(h.path(seedRecordFile), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write seed record: %v", err)
			}
			before := h.snapshotDataDir()

			_, err := h.boot(fixtureV1)
			if err == nil {
				t.Fatal("boot succeeded, want a seed record error")
			}
			if !strings.Contains(err.Error(), seedRecordFile) {
				t.Errorf("error %q does not cite the seed record path", err)
			}
			h.assertDataDirUnchanged(before)
		})
	}
}

// Criterion 4: without a data_dir every fixture record is created on each
// boot, and a second pass into the same stores fails exactly as Load does.
func TestReconcileWithoutDataDirCreatesEveryRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(path, []byte(fixtureWithChildren), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for _, bootName := range []string{"boot 1", "boot 2"} {
		target := freshTarget()
		if err := bootfixture.Reconcile(ctx, target, path, "", nil); err != nil {
			t.Fatalf("%s: %v", bootName, err)
		}
		assertEndDeviceCount(t, target.EndDevices, 2)
		assertFixtureE1(t, mustEndDevice(t, target.EndDevices, "e1"))
		for _, key := range [][2]string{{"e1", "p1"}, {"e1", "p3"}, {"e2", "p2"}} {
			prog := mustProgram(t, target.DERPrograms, key[0], key[1])
			if want := "/edev/" + key[0] + "/derp/" + key[1]; prog.Href != want {
				t.Errorf("%s: DERProgram Href = %q, want %q", bootName, prog.Href, want)
			}
		}
		for _, scope := range []string{"e1/f1/p3", "e2/f2/p2"} {
			if _, err := target.DefaultDERControls.Get(ctx, scope, dderControlSingletonKey); err != nil {
				t.Errorf("%s: DefaultDERControl %s: %v", bootName, scope, err)
			}
		}
		if _, err := target.FSAs.Get(ctx, "e2", "f2"); err != nil {
			t.Errorf("%s: FSA (e2, f2): %v", bootName, err)
		}

		err := bootfixture.Reconcile(ctx, target, path, "", nil)
		if !errors.Is(err, store.ErrAlreadyExists) {
			t.Errorf("%s: second pass into the same stores: err = %v, want store.ErrAlreadyExists", bootName, err)
		}
	}
}
