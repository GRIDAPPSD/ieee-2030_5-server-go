// Tests for the #52 fixture loader. These run against the
// in-memory store implementations (pkg/store/memory) only — no
// internal/server, no internal/handler. The loader's contract is
// that it writes through the public store API, so these tests are
// the canonical proof.
//
// The three fixture YAMLs are loaded by relative path
// (../fixtures/<name>.yaml) — `go test` runs with the package
// directory as cwd, and the fixtures live one level up.
package csiptest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// fixturePath joins the package-local fixture directory with name.
// Centralized so a test-data layout change touches one place.
func fixturePath(name string) string {
	return filepath.Join("..", "fixtures", name)
}

func TestLoad_SingleEdev_BuildsStoreState(t *testing.T) {
	t.Parallel()

	const (
		wantSFDI = "273448359951"
		wantLFDI = "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"
	)

	target := csiptest.NewTarget()
	if err := csiptest.Load(context.Background(), target, fixturePath("single-edev.yaml")); err != nil {
		t.Fatalf("Load(single-edev): %v", err)
	}

	count, err := target.EndDevices.Count(context.Background())
	if err != nil {
		t.Fatalf("EndDevices.Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("EndDevices.Count = %d, want 1", count)
	}

	// Spot-check the secondary indexes that EndDeviceStore promises
	// (GetBySFDI / GetByLFDI). If the loader skipped Create on the
	// concrete EndDeviceStore and went straight to the embedded
	// Store, these would return ErrNotFound.
	dev, err := target.EndDevices.GetBySFDI(context.Background(), wantSFDI)
	if err != nil {
		t.Fatalf("GetBySFDI(%q): %v", wantSFDI, err)
	}
	if dev.LFDI != wantLFDI {
		t.Errorf("dev.LFDI = %q, want %q", dev.LFDI, wantLFDI)
	}

	dev2, err := target.EndDevices.GetByLFDI(context.Background(), wantLFDI)
	if err != nil {
		t.Fatalf("GetByLFDI(%q): %v", wantLFDI, err)
	}
	if dev2.SFDI != wantSFDI {
		t.Errorf("dev2.SFDI = %q, want %q", dev2.SFDI, wantSFDI)
	}
	if dev2.RegistrationLink == nil || dev2.RegistrationLink.Href != "/edev/0/rg" {
		t.Errorf("dev2.RegistrationLink = %+v, want Href=/edev/0/rg", dev2.RegistrationLink)
	}
	if dev2.Enabled == nil || !*dev2.Enabled {
		t.Errorf("dev2.Enabled = %v, want non-nil true", dev2.Enabled)
	}
}

func TestLoad_SevenLevelFSA_BuildsPriorityChain(t *testing.T) {
	t.Parallel()

	target := csiptest.NewTarget()
	if err := csiptest.Load(context.Background(), target, fixturePath("seven-level-fsa.yaml")); err != nil {
		t.Fatalf("Load(seven-level-fsa): %v", err)
	}

	ctx := context.Background()

	// Walk the FSA store for the single EndDevice. Expect seven
	// entries in ID order (0..6).
	fsaList, err := target.FSAs.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("FSAs.List(edev=0): %v", err)
	}
	if fsaList.All != 7 {
		t.Fatalf("FSAList.All = %d, want 7", fsaList.All)
	}
	if len(fsaList.Items) != 7 {
		t.Fatalf("len(FSAList.Items) = %d, want 7", len(fsaList.Items))
	}

	wantDescriptions := []string{
		"L0-system", "L1-substation", "L2-feeder", "L3-transformer",
		"L4-service-point", "L5-meter", "L6-device",
	}
	for i, want := range wantDescriptions {
		got := fsaList.Items[i]
		if got.Description != want {
			t.Errorf("FSA[%d].Description = %q, want %q", i, got.Description, want)
		}
		if got.DERProgramListLink == nil {
			t.Fatalf("FSA[%d].DERProgramListLink is nil", i)
		}
		wantHref := fmt.Sprintf("/edev/0/fsa/%d/derp", i)
		if got.DERProgramListLink.Href != wantHref {
			t.Errorf("FSA[%d].DERProgramListLink.Href = %q, want %q", i, got.DERProgramListLink.Href, wantHref)
		}
	}

	// Walk the DERProgram store. Same EndDevice scope; expect
	// seven entries with primacy 0..6 — the priority chain.
	progList, err := target.DERPrograms.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("DERPrograms.List(edev=0): %v", err)
	}
	if progList.All != 7 {
		t.Fatalf("DERProgramList.All = %d, want 7", progList.All)
	}
	for i, prog := range progList.Items {
		if prog.Primacy != uint8(i) {
			t.Errorf("DERProgram[%d].Primacy = %d, want %d", i, prog.Primacy, i)
		}
	}

	// Cross-check: the 7 primacy values strictly ascend.
	for i := 1; i < len(progList.Items); i++ {
		if progList.Items[i-1].Primacy >= progList.Items[i].Primacy {
			t.Errorf("primacy chain not ascending at i=%d: %d -> %d",
				i, progList.Items[i-1].Primacy, progList.Items[i].Primacy)
		}
	}
}

func TestLoad_DERProgramSingle_BuildsStoreState(t *testing.T) {
	t.Parallel()

	target := csiptest.NewTarget()
	if err := csiptest.Load(context.Background(), target, fixturePath("derprogram-single.yaml")); err != nil {
		t.Fatalf("Load(derprogram-single): %v", err)
	}

	ctx := context.Background()

	// Exactly one DERProgram in scope.
	progList, err := target.DERPrograms.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("DERPrograms.List(edev=0): %v", err)
	}
	if progList.All != 1 {
		t.Fatalf("DERProgramList.All = %d, want 1", progList.All)
	}
	if progList.Items[0].Primacy != 0 {
		t.Errorf("Primacy = %d, want 0", progList.Items[0].Primacy)
	}
	if progList.Items[0].DefaultDERControlLink == nil {
		t.Fatal("DefaultDERControlLink is nil; loader dropped the link")
	}

	// Exactly one DefaultDERControl under the composite scope.
	// The scope key matches the server's "{edev}/{fsa}/{derp}"
	// convention and the singleton key is "default".
	dderc, err := target.DefaultDERControls.Get(ctx, "0/0/0", "default")
	if err != nil {
		t.Fatalf("DefaultDERControls.Get(0/0/0, default): %v", err)
	}
	if dderc.MRID != "CORE-012-DDERC" {
		t.Errorf("DefaultDERControl.MRID = %q, want CORE-012-DDERC", dderc.MRID)
	}
	if dderc.DERControlBase == nil || dderc.DERControlBase.OpModMaxLimW == nil {
		t.Fatalf("DefaultDERControl.DERControlBase.OpModMaxLimW is nil; loader dropped the limit")
	}
	if got := *dderc.DERControlBase.OpModMaxLimW; got != 5000 {
		t.Errorf("OpModMaxLimW = %d, want 5000", got)
	}

	// Zero DERControls — the "C in CORE-012" assertion.
	dcCount, err := target.DERControls.Count(ctx, "0/0/0")
	if err != nil {
		t.Fatalf("DERControls.Count: %v", err)
	}
	if dcCount != 0 {
		t.Errorf("DERControls.Count = %d, want 0", dcCount)
	}

	// Zero DERCurves.
	curveCount, err := target.DERCurves.Count(ctx)
	if err != nil {
		t.Fatalf("DERCurves.Count: %v", err)
	}
	if curveCount != 0 {
		t.Errorf("DERCurves.Count = %d, want 0", curveCount)
	}
}

func TestLoad_IdempotentReload(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// First load.
	a := csiptest.NewTarget()
	if err := csiptest.Load(ctx, a, fixturePath("seven-level-fsa.yaml")); err != nil {
		t.Fatalf("Load(first): %v", err)
	}

	// "Clear" = use a fresh Target (the harness rule: one Target
	// per test, see #53 BootServer). This proves the loader
	// produces deterministic state across independent fresh targets.
	b := csiptest.NewTarget()
	if err := csiptest.Load(ctx, b, fixturePath("seven-level-fsa.yaml")); err != nil {
		t.Fatalf("Load(second): %v", err)
	}

	// Same end-device count.
	countA, err := a.EndDevices.Count(ctx)
	if err != nil {
		t.Fatalf("a.EndDevices.Count: %v", err)
	}
	countB, err := b.EndDevices.Count(ctx)
	if err != nil {
		t.Fatalf("b.EndDevices.Count: %v", err)
	}
	if countA != countB {
		t.Fatalf("EndDevices.Count diverged: a=%d b=%d", countA, countB)
	}

	// Same FSA list.
	listA, err := a.FSAs.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("a.FSAs.List: %v", err)
	}
	listB, err := b.FSAs.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("b.FSAs.List: %v", err)
	}
	if listA.All != listB.All || len(listA.Items) != len(listB.Items) {
		t.Fatalf("FSA list diverged: a=%+v b=%+v", listA, listB)
	}
	for i := range listA.Items {
		if listA.Items[i].Description != listB.Items[i].Description {
			t.Errorf("FSA[%d] desc diverged: a=%q b=%q",
				i, listA.Items[i].Description, listB.Items[i].Description)
		}
		if listA.Items[i].MRID != listB.Items[i].MRID {
			t.Errorf("FSA[%d] mrid diverged: a=%q b=%q",
				i, listA.Items[i].MRID, listB.Items[i].MRID)
		}
	}

	// Same DERProgram primacy chain.
	progA, err := a.DERPrograms.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("a.DERPrograms.List: %v", err)
	}
	progB, err := b.DERPrograms.List(ctx, "0", store.ListOptions{Start: 0, Limit: 100})
	if err != nil {
		t.Fatalf("b.DERPrograms.List: %v", err)
	}
	if len(progA.Items) != len(progB.Items) {
		t.Fatalf("DERProgram list len diverged: %d vs %d", len(progA.Items), len(progB.Items))
	}
	for i := range progA.Items {
		if progA.Items[i].Primacy != progB.Items[i].Primacy {
			t.Errorf("DERProgram[%d] primacy diverged: a=%d b=%d",
				i, progA.Items[i].Primacy, progB.Items[i].Primacy)
		}
	}

	// And: reload onto the SAME target rejects because Create
	// enforces ErrAlreadyExists. This is the loader's documented
	// contract — "one Target per test".
	err = csiptest.Load(ctx, a, fixturePath("seven-level-fsa.yaml"))
	if err == nil {
		t.Fatal("Load on already-loaded target: want error, got nil")
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("Load on already-loaded target: error = %v, want errors.Is(ErrAlreadyExists)", err)
	}
}

func TestLoad_MalformedYAML_WrappedError(t *testing.T) {
	t.Parallel()

	// Write a malformed YAML to a tempdir so the assertion does
	// not rely on a checked-in bad fixture.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("end_devices: [this isn't valid yaml :\n"), 0o600); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}

	target := csiptest.NewTarget()
	err := csiptest.Load(context.Background(), target, bad)
	if err == nil {
		t.Fatal("Load(bad.yaml): want error, got nil")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("Load error does not cite path %q: %v", bad, err)
	}
	if !strings.Contains(err.Error(), "decode fixture") {
		t.Errorf("Load error not wrapped with 'decode fixture' prefix: %v", err)
	}
}

func TestLoad_MissingFile_WrappedError(t *testing.T) {
	t.Parallel()

	target := csiptest.NewTarget()
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	err := csiptest.Load(context.Background(), target, missing)
	if err == nil {
		t.Fatal("Load(missing): want error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(missing): want errors.Is(os.ErrNotExist), got %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("Load error does not cite missing path %q: %v", missing, err)
	}
}

func TestLoad_NilTarget_Errors(t *testing.T) {
	t.Parallel()

	err := csiptest.Load(context.Background(), nil, fixturePath("single-edev.yaml"))
	if err == nil {
		t.Fatal("Load(nil target): want error, got nil")
	}
	if !strings.Contains(err.Error(), "target is nil") {
		t.Errorf("Load(nil target): error = %v, want 'target is nil'", err)
	}
}

func TestLoadSpec_OrphanFSA_Errors(t *testing.T) {
	t.Parallel()

	// Hand-crafted Spec referencing a missing EndDevice — proves
	// the orphan-check fires inside applySpec, not just at
	// decode-time.
	spec := &csiptest.Spec{
		FSAs: []csiptest.FSASpec{
			{EndDeviceID: "ghost", ID: "0", MRID: "ORPHAN"},
		},
	}
	target := csiptest.NewTarget()
	err := csiptest.LoadSpec(context.Background(), target, spec)
	if err == nil {
		t.Fatal("LoadSpec(orphan FSA): want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown end_device_id") {
		t.Errorf("LoadSpec(orphan FSA): error = %v, want 'unknown end_device_id'", err)
	}
}

func TestLoadSpec_DuplicateEndDevice_Errors(t *testing.T) {
	t.Parallel()

	spec := &csiptest.Spec{
		EndDevices: []csiptest.EndDeviceSpec{
			{ID: "0", SFDI: "111", LFDI: "AA"},
			{ID: "0", SFDI: "222", LFDI: "BB"},
		},
	}
	target := csiptest.NewTarget()
	err := csiptest.LoadSpec(context.Background(), target, spec)
	if err == nil {
		t.Fatal("LoadSpec(dup EndDevice): want error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("LoadSpec(dup EndDevice): error = %v, want 'duplicate id'", err)
	}
}

// TestLoadSpec_PerCentOutOfRange_Errors pins that a percent the XML encoder
// would refuse fails the load and stores no control, on both control paths.
func TestLoadSpec_PerCentOutOfRange_Errors(t *testing.T) {
	t.Parallel()

	over := sep2.PerCent(10001)
	under := sep2.SignedPerCent(-10001)
	cases := []struct {
		name    string
		dderc   bool
		base    csiptest.DERControlBaseSpec
		wantMsg string
	}{
		{"default control op_mod_max_lim_w above 10000", true, csiptest.DERControlBaseSpec{OpModMaxLimW: &over}, "default_der_controls[0]: op_mod_max_lim_w"},
		{"control op_mod_max_lim_w above 10000", false, csiptest.DERControlBaseSpec{OpModMaxLimW: &over}, `der_controls[0] (id="a"): op_mod_max_lim_w`},
		{"control op_mod_fixed_w below -10000", false, csiptest.DERControlBaseSpec{OpModFixedW: &under}, `der_controls[0] (id="a"): op_mod_fixed_w`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := tc.base
			spec := &csiptest.Spec{}
			if tc.dderc {
				spec.DefaultDERControls = []csiptest.DefaultDERControlSpec{
					{EndDeviceID: "0", FSAID: "0", DERProgramID: "0", DERControlBase: &base},
				}
			} else {
				spec.DERControls = []csiptest.DERControlSpec{
					{EndDeviceID: "0", FSAID: "0", DERProgramID: "0", ID: "a", DERControlBase: &base},
				}
			}
			target := csiptest.NewTarget()
			ctx := context.Background()
			err := csiptest.LoadSpec(ctx, target, spec)
			if err == nil {
				t.Fatal("LoadSpec: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("LoadSpec: error = %v, want it to contain %q", err, tc.wantMsg)
			}
			ddercCount, err := target.DefaultDERControls.Count(ctx, "0/0/0")
			if err != nil {
				t.Fatalf("DefaultDERControls.Count: %v", err)
			}
			dercCount, err := target.DERControls.Count(ctx, "0/0/0")
			if err != nil {
				t.Fatalf("DERControls.Count: %v", err)
			}
			if ddercCount != 0 || dercCount != 0 {
				t.Errorf("stored %d default and %d event controls after a rejected load, want 0 and 0", ddercCount, dercCount)
			}
		})
	}
}

func TestLoad_UnknownYAMLKey_Errors(t *testing.T) {
	t.Parallel()

	// KnownFields(true) is the typo-catching guarantee. Write a
	// fixture with a slightly misspelled top-level key and confirm
	// the decoder rejects it.
	dir := t.TempDir()
	bad := filepath.Join(dir, "typo.yaml")
	const body = "end_devices: []\nend_deviceZ: []\n" // typo: Z
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatalf("write typo fixture: %v", err)
	}

	target := csiptest.NewTarget()
	err := csiptest.Load(context.Background(), target, bad)
	if err == nil {
		t.Fatal("Load(typo): want error, got nil")
	}
	if !strings.Contains(err.Error(), "decode fixture") {
		t.Errorf("Load(typo): error = %v, want 'decode fixture' prefix", err)
	}
}
