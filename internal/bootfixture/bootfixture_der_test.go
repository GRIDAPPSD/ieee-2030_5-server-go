package bootfixture_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// dderControlSingletonKey mirrors the unexported bootfixture.singletonKey. The
// production handler at GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc
// looks the resource up under this inner id, so a rename on either side
// makes the DefaultDERControl unreachable; asserting the literal here is
// the cross-check.
const dderControlSingletonKey = "default"

// loadYAML writes body to a temp fixture file and runs the production
// loader against a fresh Target. It returns the target so callers can
// assert on the constructed resources, and the loader error verbatim so
// callers can assert on rejection paths.
func loadYAML(t *testing.T, body string) (*bootfixture.Target, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	target := freshTarget()
	return target, bootfixture.Load(context.Background(), target, path)
}

func mustLoadYAML(t *testing.T, body string) *bootfixture.Target {
	t.Helper()
	target, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return target
}

// deref fails the test rather than panicking when an optional pointer
// field the fixture set is nil, so a dropped field reports as a named
// assertion failure instead of a nil dereference.
func deref[T any](t *testing.T, p *T, name string) T {
	t.Helper()
	if p == nil {
		var zero T
		t.Fatalf("%s = nil, want non-nil", name)
		return zero
	}
	return *p
}

// --- buildFSA -------------------------------------------------------

func TestBuildFSAValues(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 100
fsas:
  - end_device_id: "e1"
    id: "f1"
    mrid: "FSA-MRID-1"
    description: "primary assignment"
    der_program_list_link:
      href: "/edev/e1/fsa/f1/derp"
      all: 2
  - end_device_id: "e1"
    id: "f2"
`)
	ctx := context.Background()

	fsa, err := target.FSAs.Get(ctx, "e1", "f1")
	if err != nil {
		t.Fatalf("FSAs.Get(e1, f1): %v", err)
	}
	// The FSA href is the anchor the DERProgram list hangs off; core
	// routes DERPrograms only at /edev/{id}/fsa/{fsaId}/derp, so a wrong
	// href here makes every nested DERControl unreachable by link-walk.
	if want := "/edev/e1/fsa/f1"; fsa.Href != want {
		t.Errorf("FSA.Href = %q, want %q", fsa.Href, want)
	}
	if want := "FSA-MRID-1"; fsa.MRID != want {
		t.Errorf("FSA.MRID = %q, want %q", fsa.MRID, want)
	}
	if want := "primary assignment"; fsa.Description != want {
		t.Errorf("FSA.Description = %q, want %q", fsa.Description, want)
	}
	link := deref(t, fsa.DERProgramListLink, "FSA.DERProgramListLink")
	if want := "/edev/e1/fsa/f1/derp"; link.Href != want {
		t.Errorf("FSA.DERProgramListLink.Href = %q, want %q", link.Href, want)
	}
	if link.All != 2 {
		t.Errorf("FSA.DERProgramListLink.All = %d, want 2", link.All)
	}
	// buildFSA never populates these two; a fixture cannot express them.
	if fsa.UsagePointListLink != nil {
		t.Errorf("FSA.UsagePointListLink = %+v, want nil", fsa.UsagePointListLink)
	}
	if fsa.DemandResponseProgramListLink != nil {
		t.Errorf("FSA.DemandResponseProgramListLink = %+v, want nil", fsa.DemandResponseProgramListLink)
	}

	// Omitted list link stays nil rather than becoming a zero-href link:
	// an empty href would serialize as href="" and dead-end a link-walk.
	bare, err := target.FSAs.Get(ctx, "e1", "f2")
	if err != nil {
		t.Fatalf("FSAs.Get(e1, f2): %v", err)
	}
	if bare.DERProgramListLink != nil {
		t.Errorf("FSA(f2).DERProgramListLink = %+v, want nil", bare.DERProgramListLink)
	}
	if want := "/edev/e1/fsa/f2"; bare.Href != want {
		t.Errorf("FSA(f2).Href = %q, want %q", bare.Href, want)
	}
	if bare.MRID != "" {
		t.Errorf("FSA(f2).MRID = %q, want empty", bare.MRID)
	}
}

// TestFSAScopeIsEndDeviceID pins the parent key FSAs are stored under.
// The core list handler reads them with the edev path segment as parent,
// so a change here silently empties GET /edev/{id}/fsa.
func TestFSAScopeIsEndDeviceID(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
  - id: "e2"
    sfdi: "2"
    lfdi: "BB"
    changed_time: 0
fsas:
  - end_device_id: "e1"
    id: "shared"
  - end_device_id: "e2"
    id: "shared"
`)
	ctx := context.Background()

	// Same inner id under two EndDevices must not collide.
	one, err := target.FSAs.Get(ctx, "e1", "shared")
	if err != nil {
		t.Fatalf("FSAs.Get(e1, shared): %v", err)
	}
	two, err := target.FSAs.Get(ctx, "e2", "shared")
	if err != nil {
		t.Fatalf("FSAs.Get(e2, shared): %v", err)
	}
	if want := "/edev/e1/fsa/shared"; one.Href != want {
		t.Errorf("FSA(e1).Href = %q, want %q", one.Href, want)
	}
	if want := "/edev/e2/fsa/shared"; two.Href != want {
		t.Errorf("FSA(e2).Href = %q, want %q", two.Href, want)
	}
	if n, err := target.FSAs.Count(ctx, "e1"); err != nil || n != 1 {
		t.Errorf("FSAs.Count(e1) = %d, %v, want 1, nil", n, err)
	}
}

// --- buildDERProgram ------------------------------------------------

func TestBuildDERProgramValues(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
fsas:
  - end_device_id: "e1"
    id: "f1"
der_programs:
  - end_device_id: "e1"
    fsa_id: "f1"
    id: "p1"
    mrid: "DERP-MRID-1"
    description: "primary program"
    primacy: 3
    default_der_control_link: "/edev/e1/fsa/f1/derp/p1/dderc"
    der_control_list_link:
      href: "/edev/e1/fsa/f1/derp/p1/derc"
      all: 1
    der_curve_list_link:
      href: "/dc"
      all: 4
  - end_device_id: "e1"
    fsa_id: "f1"
    id: "p2"
    primacy: 0
`)
	ctx := context.Background()

	prog, err := target.DERPrograms.Get(ctx, "e1", "p1")
	if err != nil {
		t.Fatalf("DERPrograms.Get(e1, p1): %v", err)
	}
	// NOTE: this href is NOT the routable form. Core registers the
	// DERProgram list only at /edev/{id}/fsa/{fsaId}/derp; there is no
	// /edev/{id}/derp/{derpId} route. Pinned as current behavior and
	// reported as a finding rather than changed here.
	if want := "/edev/e1/derp/p1"; prog.Href != want {
		t.Errorf("DERProgram.Href = %q, want %q", prog.Href, want)
	}
	if want := "DERP-MRID-1"; prog.MRID != want {
		t.Errorf("DERProgram.MRID = %q, want %q", prog.MRID, want)
	}
	if want := "primary program"; prog.Description != want {
		t.Errorf("DERProgram.Description = %q, want %q", prog.Description, want)
	}
	// Primacy drives which program wins when two programs overlap; a
	// dropped or defaulted primacy silently reorders control precedence.
	if prog.Primacy != 3 {
		t.Errorf("DERProgram.Primacy = %d, want 3", prog.Primacy)
	}
	dd := deref(t, prog.DefaultDERControlLink, "DERProgram.DefaultDERControlLink")
	if want := "/edev/e1/fsa/f1/derp/p1/dderc"; dd.Href != want {
		t.Errorf("DefaultDERControlLink.Href = %q, want %q", dd.Href, want)
	}
	dcl := deref(t, prog.DERControlListLink, "DERProgram.DERControlListLink")
	if want := "/edev/e1/fsa/f1/derp/p1/derc"; dcl.Href != want {
		t.Errorf("DERControlListLink.Href = %q, want %q", dcl.Href, want)
	}
	if dcl.All != 1 {
		t.Errorf("DERControlListLink.All = %d, want 1", dcl.All)
	}
	curves := deref(t, prog.DERCurveListLink, "DERProgram.DERCurveListLink")
	if want := "/dc"; curves.Href != want {
		t.Errorf("DERCurveListLink.Href = %q, want %q", curves.Href, want)
	}
	if curves.All != 4 {
		t.Errorf("DERCurveListLink.All = %d, want 4", curves.All)
	}
	// buildDERProgram never sets ActiveDERControlListLink.
	if prog.ActiveDERControlListLink != nil {
		t.Errorf("DERProgram.ActiveDERControlListLink = %+v, want nil", prog.ActiveDERControlListLink)
	}

	bare, err := target.DERPrograms.Get(ctx, "e1", "p2")
	if err != nil {
		t.Fatalf("DERPrograms.Get(e1, p2): %v", err)
	}
	if bare.DefaultDERControlLink != nil {
		t.Errorf("DERProgram(p2).DefaultDERControlLink = %+v, want nil", bare.DefaultDERControlLink)
	}
	if bare.DERControlListLink != nil {
		t.Errorf("DERProgram(p2).DERControlListLink = %+v, want nil", bare.DERControlListLink)
	}
	if bare.DERCurveListLink != nil {
		t.Errorf("DERProgram(p2).DERCurveListLink = %+v, want nil", bare.DERCurveListLink)
	}
}

// TestDERProgramScopeIgnoresFSAID pins a structural asymmetry: DERControls
// and DefaultDERControls are keyed by the (edev, fsa, derp) triple, but
// DERPrograms are keyed by EndDeviceID alone. The declared fsa_id affects
// neither the store key nor the href, so two programs that differ only by
// FSA collide. Reported as a finding; asserted here so the collapse is
// visible rather than surfacing as a confusing boot failure.
func TestDERProgramScopeIgnoresFSAID(t *testing.T) {
	t.Parallel()

	_, err := loadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
fsas:
  - end_device_id: "e1"
    id: "f1"
  - end_device_id: "e1"
    id: "f2"
der_programs:
  - end_device_id: "e1"
    fsa_id: "f1"
    id: "p1"
    primacy: 0
  - end_device_id: "e1"
    fsa_id: "f2"
    id: "p1"
    primacy: 0
`)
	if err == nil {
		t.Fatal("expected collision between same-id programs under different FSAs, got nil")
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("error chain lacks store.ErrAlreadyExists: %v", err)
	}
	if !strings.Contains(err.Error(), "der_programs[1]") {
		t.Errorf("error %q does not cite the colliding index", err)
	}
}

// --- buildDefaultDERControl -----------------------------------------

func TestBuildDefaultDERControlValues(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
fsas:
  - end_device_id: "e1"
    id: "f1"
der_programs:
  - end_device_id: "e1"
    fsa_id: "f1"
    id: "p1"
    primacy: 0
default_der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    mrid: "DDERC-MRID-1"
    der_control_base:
      op_mod_fixed_w: 2000
      op_mod_connect: true
`)
	ctx := context.Background()

	// The composite scope key and the "default" inner id together are
	// what the dderc handler resolves; both must hold.
	dc, err := target.DefaultDERControls.Get(ctx, "e1/f1/p1", dderControlSingletonKey)
	if err != nil {
		t.Fatalf("DefaultDERControls.Get(e1/f1/p1, %s): %v", dderControlSingletonKey, err)
	}
	if want := "/edev/e1/fsa/f1/derp/p1/dderc"; dc.Href != want {
		t.Errorf("DefaultDERControl.Href = %q, want %q", dc.Href, want)
	}
	if want := "DDERC-MRID-1"; dc.MRID != want {
		t.Errorf("DefaultDERControl.MRID = %q, want %q", dc.MRID, want)
	}
	base := deref(t, dc.DERControlBase, "DefaultDERControl.DERControlBase")
	// This is the value a client falls back to when no event is active, so
	// it has to survive the load unchanged (2000 = 20.00%).
	if got := deref(t, base.OpModFixedW, "DefaultDERControl.DERControlBase.OpModFixedW"); got != 2000 {
		t.Errorf("OpModFixedW = %d, want 2000", got)
	}
	if got := deref(t, base.OpModConnect, "OpModConnect"); !got {
		t.Errorf("OpModConnect = %v, want true", got)
	}
	// SetGradW / SetSoftGradW are on the sep2 type and in the csiptest
	// schema, but bootfixture's DefaultDERControlSpec has no YAML field
	// for either, so a boot fixture can never emit them. Reported.
	if dc.SetGradW != nil {
		t.Errorf("DefaultDERControl.SetGradW = %v, want nil (unrepresentable in this schema)", dc.SetGradW)
	}
	if dc.SetSoftGradW != nil {
		t.Errorf("DefaultDERControl.SetSoftGradW = %v, want nil (unrepresentable in this schema)", dc.SetSoftGradW)
	}
}

func TestBuildDefaultDERControlWithoutBase(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
default_der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    mrid: "DDERC-NOBASE"
`)
	ctx := context.Background()

	dc, err := target.DefaultDERControls.Get(ctx, "e1/f1/p1", dderControlSingletonKey)
	if err != nil {
		t.Fatalf("DefaultDERControls.Get: %v", err)
	}
	// Omitted base stays nil rather than becoming an all-nil struct: the
	// two serialize differently (absent element vs empty element).
	if dc.DERControlBase != nil {
		t.Errorf("DERControlBase = %+v, want nil", dc.DERControlBase)
	}
	if want := "DDERC-NOBASE"; dc.MRID != want {
		t.Errorf("MRID = %q, want %q", dc.MRID, want)
	}
}

// TestDefaultDERControlParentNotValidated pins that the loader accepts a
// DefaultDERControl whose (edev, fsa, derp) triple names resources that do
// not exist, while it rejects an FSA or DERProgram with an unknown
// end_device_id. Asymmetric validation; reported as a finding. The
// fixture above relies on the same behavior (no der_programs block).
func TestDefaultDERControlParentNotValidated(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
default_der_controls:
  - end_device_id: "ghost"
    fsa_id: "ghost-fsa"
    der_program_id: "ghost-derp"
der_controls:
  - end_device_id: "ghost"
    fsa_id: "ghost-fsa"
    der_program_id: "ghost-derp"
    id: "x"
`)
	ctx := context.Background()

	if _, err := target.DefaultDERControls.Get(ctx, "ghost/ghost-fsa/ghost-derp", dderControlSingletonKey); err != nil {
		t.Fatalf("orphan DefaultDERControl was expected to load: %v", err)
	}
	if _, err := target.DERControls.Get(ctx, "ghost/ghost-fsa/ghost-derp", "x"); err != nil {
		t.Fatalf("orphan DERControl was expected to load: %v", err)
	}
	// No EndDevice named "ghost" exists, so both resources are reachable
	// in the store but unreachable over the wire.
	if _, err := target.EndDevices.Get(ctx, "ghost"); err == nil {
		t.Fatal("EndDevice ghost unexpectedly exists; fixture assumption broken")
	}
}

// --- buildDERControl ------------------------------------------------

func TestBuildDERControlValues(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    mrid: "DERC-MRID-A"
    der_control_base:
      op_mod_fixed_w: 5000
      ramp_tms: 20
`)
	ctx := context.Background()

	dc, err := target.DERControls.Get(ctx, "e1/f1/p1", "a")
	if err != nil {
		t.Fatalf("DERControls.Get(e1/f1/p1, a): %v", err)
	}
	if want := "/edev/e1/fsa/f1/derp/p1/derc/a"; dc.Href != want {
		t.Errorf("DERControl.Href = %q, want %q", dc.Href, want)
	}
	base := deref(t, dc.DERControlBase, "DERControl.DERControlBase")
	if got := deref(t, base.OpModFixedW, "OpModFixedW"); got != 5000 {
		t.Errorf("OpModFixedW = %d, want 5000", got)
	}
	if got := deref(t, base.RampTms, "RampTms"); got != 20 {
		t.Errorf("RampTms = %d, want 20", got)
	}

	// --- known defects, pinned deliberately ---------------------------
	//
	// buildDERControl drops DERControlSpec.MRID on the floor: the YAML
	// field is declared and parsed but never copied onto the resource.
	// buildFSA, buildDERProgram and buildDefaultDERControl all copy their
	// MRID. Reported as a finding; when it is fixed this assertion must
	// flip to want "DERC-MRID-A".
	if dc.MRID != "" {
		t.Errorf("DERControl.MRID = %q; expected the known drop-on-load defect (empty). If MRID is now carried, update this test", dc.MRID)
	}
	// The spec type has no interval, event_status, description, replyTo
	// or responseRequired field, so a boot-seeded DERControl carries an
	// absent interval (never activatable) and no response contract. This
	// is the seeding side of the conformance gap; reported.
	if dc.Interval != nil {
		t.Errorf("DERControl.Interval = %+v; schema cannot express an interval today, update this test if it can", dc.Interval)
	}
	if dc.EventStatus != nil {
		t.Errorf("DERControl.EventStatus = %+v; schema cannot express event_status today", dc.EventStatus)
	}
	if dc.ReplyTo != "" {
		t.Errorf("DERControl.ReplyTo = %q; schema cannot express replyTo today", dc.ReplyTo)
	}
	if dc.ResponseRequired != nil {
		t.Errorf("DERControl.ResponseRequired = %v; schema cannot express responseRequired today", dc.ResponseRequired)
	}
	if dc.Description != "" {
		t.Errorf("DERControl.Description = %q; schema cannot express description today", dc.Description)
	}
}

func TestBuildDERControlWithoutBase(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "bare"
`)
	dc, err := target.DERControls.Get(context.Background(), "e1/f1/p1", "bare")
	if err != nil {
		t.Fatalf("DERControls.Get: %v", err)
	}
	if dc.DERControlBase != nil {
		t.Errorf("DERControlBase = %+v, want nil", dc.DERControlBase)
	}
	if want := "/edev/e1/fsa/f1/derp/p1/derc/bare"; dc.Href != want {
		t.Errorf("Href = %q, want %q", dc.Href, want)
	}
}

// TestDERControlSchemaDivergesFromCsiptest pins that the bootfixture
// schema is a strict subset of the csiptest schema despite the package
// doc claiming a fixture authored for the harness can be reused as a
// SEP2_BOOT_FIXTURE. Strict decode rejects the extra keys, so every
// harness fixture carrying a DERControl interval fails to boot-load.
// Reported as a finding.
func TestDERControlSchemaDivergesFromCsiptest(t *testing.T) {
	t.Parallel()

	// Each key below exists in test/csip/csiptest/loader.go but not in
	// bootfixture, so strict decode must reject it today.
	cases := map[string]string{
		"interval": `
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    interval:
      start: 1700000060
      duration: 120
`,
		"event_status": `
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    event_status:
      current_status: 0
      date_time: 1700000000
`,
		"description": `
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    description: "scheduled control"
`,
		"set_grad_w": `
default_der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    set_grad_w: 100
`,
		"op_mod_volt_watt": `
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    der_control_base:
      op_mod_volt_watt: 7
`,
	}

	const edev = `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
`
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := loadYAML(t, edev+body)
			if err == nil {
				t.Fatalf("key %q now decodes; bootfixture caught up with the csiptest schema, update this test", name)
			}
			if !strings.Contains(err.Error(), "decode fixture") {
				t.Errorf("error %q is not a decode rejection", err)
			}
		})
	}
}

// --- buildDERControlBase --------------------------------------------

func TestBuildDERControlBaseAllSupportedFields(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "full"
    der_control_base:
      op_mod_connect: false
      op_mod_energize: true
      op_mod_fixed_w: -10000
      op_mod_max_lim_w: 10000
      op_mod_target_w:
        multiplier: -1
        value: -300
      op_mod_volt_var: 42
      op_mod_freq_droop: 55
      ramp_tms: 66
`)

	dc, err := target.DERControls.Get(context.Background(), "e1/f1/p1", "full")
	if err != nil {
		t.Fatalf("DERControls.Get: %v", err)
	}
	base := deref(t, dc.DERControlBase, "DERControlBase")

	// op_mod_connect: false must round-trip as a non-nil pointer to
	// false. Collapsing it to nil would drop a disconnect command.
	if got := deref(t, base.OpModConnect, "OpModConnect"); got {
		t.Errorf("OpModConnect = %v, want false", got)
	}
	if got := deref(t, base.OpModEnergize, "OpModEnergize"); !got {
		t.Errorf("OpModEnergize = %v, want true", got)
	}
	// The range bounds themselves must load: SignedPerCent's floor, PerCent's ceiling.
	if got := deref(t, base.OpModFixedW, "OpModFixedW"); got != -10000 {
		t.Errorf("OpModFixedW = %d, want -10000", got)
	}
	if got := deref(t, base.OpModMaxLimW, "OpModMaxLimW"); got != 10000 {
		t.Errorf("OpModMaxLimW = %d, want 10000", got)
	}
	// Negative multiplier and negative value together: sign handling on
	// an int8 multiplier and an int64 value are separate failure modes.
	if got := deref(t, base.OpModTargetW, "OpModTargetW"); got.Multiplier != -1 || got.Value != -300 {
		t.Errorf("OpModTargetW = %+v, want {Multiplier:-1 Value:-300}", got)
	}
	if got := deref(t, base.OpModVoltVar, "OpModVoltVar"); got != 42 {
		t.Errorf("OpModVoltVar = %d, want 42", got)
	}
	if got := deref(t, base.OpModFreqDroop, "OpModFreqDroop"); got != 55 {
		t.Errorf("OpModFreqDroop = %d, want 55", got)
	}
	if got := deref(t, base.RampTms, "RampTms"); got != 66 {
		t.Errorf("RampTms = %d, want 66", got)
	}

	// Fields the sep2 type carries but this schema cannot set must stay
	// nil: a zero-valued mode would be an unrequested control action.
	for name, isSet := range map[string]bool{
		"OpModFixedPFAbsorbW":         base.OpModFixedPFAbsorbW != nil,
		"OpModFixedPFInjectW":         base.OpModFixedPFInjectW != nil,
		"OpModFixedVar":               base.OpModFixedVar != nil,
		"OpModFreqWatt":               base.OpModFreqWatt != nil,
		"OpModHFRTMustTrip":           base.OpModHFRTMustTrip != nil,
		"OpModHVRTMomentaryCessation": base.OpModHVRTMomentaryCessation != nil,
		"OpModHVRTMustTrip":           base.OpModHVRTMustTrip != nil,
		"OpModLFRTMustTrip":           base.OpModLFRTMustTrip != nil,
		"OpModLVRTMomentaryCessation": base.OpModLVRTMomentaryCessation != nil,
		"OpModLVRTMustTrip":           base.OpModLVRTMustTrip != nil,
		"OpModTargetVar":              base.OpModTargetVar != nil,
		"OpModVoltWatt":               base.OpModVoltWatt != nil,
	} {
		if isSet {
			t.Errorf("%s is set, want nil (not expressible in this schema)", name)
		}
	}
}

// TestPerCentFieldsRefuseInvalidValues pins that an out-of-range percent, a
// negative PerCent, and the old multiplier/value mapping each fail the load
// and store no DERControl, rather than loading as some other setpoint.
func TestPerCentFieldsRefuseInvalidValues(t *testing.T) {
	t.Parallel()

	const head = `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
    der_control_base:
`
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"op_mod_fixed_w above 10000", "      op_mod_fixed_w: 10001\n", `der_controls[0] (id="a"): op_mod_fixed_w`},
		{"op_mod_fixed_w below -10000", "      op_mod_fixed_w: -10001\n", `der_controls[0] (id="a"): op_mod_fixed_w`},
		{"op_mod_max_lim_w above 10000", "      op_mod_max_lim_w: 10001\n", `der_controls[0] (id="a"): op_mod_max_lim_w`},
		{"negative op_mod_max_lim_w", "      op_mod_max_lim_w: -1\n", "decode fixture"},
		{"op_mod_fixed_w as multiplier and value", "      op_mod_fixed_w:\n        multiplier: 0\n        value: 2000\n", "decode fixture"},
		{"op_mod_max_lim_w as multiplier and value", "      op_mod_max_lim_w:\n        multiplier: 0\n        value: 2000\n", "decode fixture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, err := loadYAML(t, head+tc.body)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tc.wantMsg)
			}
			if _, getErr := target.DERControls.Get(context.Background(), "e1/f1/p1", "a"); !errors.Is(getErr, store.ErrNotFound) {
				t.Errorf("DERControls.Get(e1/f1/p1, a) error = %v, want store.ErrNotFound: the rejected control was stored", getErr)
			}
		})
	}
}

func TestBuildDERControlBaseEmpty(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "empty"
    der_control_base: {}
`)

	dc, err := target.DERControls.Get(context.Background(), "e1/f1/p1", "empty")
	if err != nil {
		t.Fatalf("DERControls.Get: %v", err)
	}
	// An explicitly empty base is present-but-empty, not absent: the
	// two are different wire signals and the loader must not conflate
	// them.
	base := deref(t, dc.DERControlBase, "DERControlBase")
	if base.OpModConnect != nil || base.OpModEnergize != nil || base.OpModFixedW != nil ||
		base.OpModMaxLimW != nil || base.OpModTargetW != nil || base.OpModVoltVar != nil ||
		base.OpModFreqDroop != nil || base.RampTms != nil {
		t.Errorf("empty der_control_base produced non-nil modes: %+v", base)
	}
}

// --- buildDERCurve --------------------------------------------------

func TestBuildDERCurveValues(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
der_curves:
  - id: "c1"
    mrid: "CURVE-MRID-1"
    description: "volt-var"
    curve_type: 11
    curve_data:
      - x: 9200
        y: 300
      - x: 9700
        y: 0
      - x: 10300
        y: 0
      - x: 10800
        y: -300
  - id: "c2"
    curve_type: 0
`)
	ctx := context.Background()

	curve, err := target.DERCurves.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("DERCurves.Get(c1): %v", err)
	}
	if want := "/dc/c1"; curve.Href != want {
		t.Errorf("DERCurve.Href = %q, want %q", curve.Href, want)
	}
	if want := "CURVE-MRID-1"; curve.MRID != want {
		t.Errorf("DERCurve.MRID = %q, want %q", curve.MRID, want)
	}
	if want := "volt-var"; curve.Description != want {
		t.Errorf("DERCurve.Description = %q, want %q", curve.Description, want)
	}
	// curveType selects which mode the curve applies to; a wrong value
	// silently applies a volt-var curve as, say, freq-watt.
	if curve.CurveType != 11 {
		t.Errorf("DERCurve.CurveType = %d, want 11", curve.CurveType)
	}

	// Point order is semantic: a DERCurve is a piecewise function read in
	// index order, so reordering or transposing x and y changes the
	// curve. Assert the exact sequence, not just the length.
	wantPoints := []struct{ x, y int32 }{
		{9200, 300},
		{9700, 0},
		{10300, 0},
		{10800, -300},
	}
	if got := len(curve.CurveData); got != len(wantPoints) {
		t.Fatalf("len(CurveData) = %d, want %d", got, len(wantPoints))
	}
	for i, want := range wantPoints {
		got := curve.CurveData[i]
		if got.XValue != want.x || got.YValue != want.y {
			t.Errorf("CurveData[%d] = {X:%d Y:%d}, want {X:%d Y:%d}",
				i, got.XValue, got.YValue, want.x, want.y)
		}
	}

	// Ramp fields are on the sep2 type but absent from this schema.
	if curve.RampDecTms != nil || curve.RampIncTms != nil || curve.RampPT1Tms != nil {
		t.Errorf("ramp fields set (%v, %v, %v), want all nil",
			curve.RampDecTms, curve.RampIncTms, curve.RampPT1Tms)
	}

	// No curve_data leaves the slice nil, which serializes as an absent
	// element rather than an empty list.
	bare, err := target.DERCurves.Get(ctx, "c2")
	if err != nil {
		t.Fatalf("DERCurves.Get(c2): %v", err)
	}
	if bare.CurveData != nil {
		t.Errorf("DERCurve(c2).CurveData = %+v, want nil", bare.CurveData)
	}
	if want := "/dc/c2"; bare.Href != want {
		t.Errorf("DERCurve(c2).Href = %q, want %q", bare.Href, want)
	}
}

// TestBuildDERCurveCreationTime covers #539: a curve whose spec gives a
// creation_time is served with exactly that value, and a curve whose spec
// gives none is served with the time the fixture was loaded, never 0.
func TestBuildDERCurveCreationTime(t *testing.T) {
	t.Parallel()

	before := time.Now().Unix()
	target := mustLoadYAML(t, `
der_curves:
  - id: "c1"
    curve_type: 11
    creation_time: 1700000000
  - id: "c2"
    curve_type: 0
`)
	after := time.Now().Unix()
	ctx := context.Background()

	withTime, err := target.DERCurves.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("DERCurves.Get(c1): %v", err)
	}
	if want := int64(1700000000); withTime.CreationTime != want {
		t.Errorf("DERCurve(c1).CreationTime = %d, want %d (spec value)",
			withTime.CreationTime, want)
	}

	unset, err := target.DERCurves.Get(ctx, "c2")
	if err != nil {
		t.Fatalf("DERCurves.Get(c2): %v", err)
	}
	if unset.CreationTime == 0 {
		t.Errorf("DERCurve(c2).CreationTime = 0, want the fixture load time")
	}
	if unset.CreationTime < before || unset.CreationTime > after {
		t.Errorf("DERCurve(c2).CreationTime = %d, want within [%d, %d] (the fixture load window)",
			unset.CreationTime, before, after)
	}
}

// --- compositeKey ---------------------------------------------------

// TestCompositeKeyDistinctness asserts what makes two scoped entries
// distinct: all three of (edev, fsa, derp) participate, so the same inner
// DERControl id can appear once per triple without collision, and each
// lands under its own key with its own href.
func TestCompositeKeyDistinctness(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
  - id: "e2"
    sfdi: "2"
    lfdi: "BB"
    changed_time: 0
der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
  - end_device_id: "e2"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
  - end_device_id: "e1"
    fsa_id: "f2"
    der_program_id: "p1"
    id: "a"
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p2"
    id: "a"
`)
	ctx := context.Background()

	// Each triple differs from the first in exactly one component, so a
	// key that ignored any one component would collapse two of these.
	for _, tc := range []struct{ scope, href string }{
		{"e1/f1/p1", "/edev/e1/fsa/f1/derp/p1/derc/a"},
		{"e2/f1/p1", "/edev/e2/fsa/f1/derp/p1/derc/a"},
		{"e1/f2/p1", "/edev/e1/fsa/f2/derp/p1/derc/a"},
		{"e1/f1/p2", "/edev/e1/fsa/f1/derp/p2/derc/a"},
	} {
		dc, err := target.DERControls.Get(ctx, tc.scope, "a")
		if err != nil {
			t.Fatalf("DERControls.Get(%q, a): %v", tc.scope, err)
		}
		if dc.Href != tc.href {
			t.Errorf("scope %q: Href = %q, want %q", tc.scope, dc.Href, tc.href)
		}
		if n, err := target.DERControls.Count(ctx, tc.scope); err != nil || n != 1 {
			t.Errorf("scope %q: Count = %d, %v, want 1, nil", tc.scope, n, err)
		}
	}
}

// TestCompositeKeyAliasingCollision pins that compositeKey joins the three
// components with an unescaped "/", so a component containing a slash
// aliases onto a different triple. Two logically distinct scopes produce
// one key. Reported as a finding; when the key gains escaping this test
// must be updated to assert both resources coexist.
func TestCompositeKeyAliasingCollision(t *testing.T) {
	t.Parallel()

	_, err := loadYAML(t, `
end_devices:
  - id: "a/b"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
  - id: "a"
    sfdi: "2"
    lfdi: "BB"
    changed_time: 0
der_controls:
  - end_device_id: "a/b"
    fsa_id: "c"
    der_program_id: "d"
    id: "x"
  - end_device_id: "a"
    fsa_id: "b/c"
    der_program_id: "d"
    id: "x"
`)
	if err == nil {
		t.Fatal("expected the known key-aliasing collision, got nil; if compositeKey now escapes its components, update this test")
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("error chain lacks store.ErrAlreadyExists: %v", err)
	}
	if !strings.Contains(err.Error(), "a/b/c/d") {
		t.Errorf("error %q does not show the aliased key a/b/c/d", err)
	}
}

// --- applySpec rejection paths --------------------------------------

func TestApplySpecRejections(t *testing.T) {
	t.Parallel()

	const edev = `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
`
	cases := []struct {
		name     string
		yaml     string
		wantMsg  string
		wantDupe bool
	}{
		{
			name:    "end device missing id",
			yaml:    "end_devices:\n  - sfdi: \"1\"\n    lfdi: \"AA\"\n    changed_time: 0\n",
			wantMsg: "end_devices[0]: id is required",
		},
		{
			name: "duplicate end device id",
			yaml: edev + `  - id: "e1"
    sfdi: "2"
    lfdi: "BB"
    changed_time: 0
`,
			wantMsg: `end_devices[1]: duplicate id "e1"`,
		},
		{
			name:    "fsa missing id",
			yaml:    edev + "fsas:\n  - end_device_id: \"e1\"\n",
			wantMsg: "fsas[0]: id is required",
		},
		{
			name:    "fsa missing end_device_id",
			yaml:    edev + "fsas:\n  - id: \"f1\"\n",
			wantMsg: "end_device_id is required",
		},
		{
			name:     "duplicate fsa in same scope",
			yaml:     edev + "fsas:\n  - end_device_id: \"e1\"\n    id: \"f1\"\n  - end_device_id: \"e1\"\n    id: \"f1\"\n",
			wantMsg:  "fsas[1]",
			wantDupe: true,
		},
		{
			name:    "der program missing id",
			yaml:    edev + "der_programs:\n  - end_device_id: \"e1\"\n    primacy: 0\n",
			wantMsg: "der_programs[0]: id is required",
		},
		{
			name:    "der program missing end_device_id",
			yaml:    edev + "der_programs:\n  - id: \"p1\"\n    primacy: 0\n",
			wantMsg: "end_device_id is required",
		},
		{
			name:    "der program unknown end device",
			yaml:    edev + "der_programs:\n  - end_device_id: \"ghost\"\n    id: \"p1\"\n    primacy: 0\n",
			wantMsg: `unknown end_device_id "ghost"`,
		},
		{
			name:    "default der control missing end_device_id",
			yaml:    edev + "default_der_controls:\n  - fsa_id: \"f1\"\n    der_program_id: \"p1\"\n",
			wantMsg: "default_der_controls[0]",
		},
		{
			name:    "default der control missing fsa_id",
			yaml:    edev + "default_der_controls:\n  - end_device_id: \"e1\"\n    der_program_id: \"p1\"\n",
			wantMsg: "default_der_controls[0]",
		},
		{
			name:    "default der control missing der_program_id",
			yaml:    edev + "default_der_controls:\n  - end_device_id: \"e1\"\n    fsa_id: \"f1\"\n",
			wantMsg: "default_der_controls[0]",
		},
		{
			name: "duplicate default der control in same scope",
			yaml: edev + `default_der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
`,
			wantMsg:  `default_der_controls[1] (scope="e1/f1/p1")`,
			wantDupe: true,
		},
		{
			name:    "default der control op_mod_max_lim_w above 10000",
			yaml:    edev + "default_der_controls:\n  - end_device_id: \"e1\"\n    fsa_id: \"f1\"\n    der_program_id: \"p1\"\n    der_control_base:\n      op_mod_max_lim_w: 10001\n",
			wantMsg: "default_der_controls[0]: op_mod_max_lim_w",
		},
		{
			name:    "der control missing id",
			yaml:    edev + "der_controls:\n  - end_device_id: \"e1\"\n    fsa_id: \"f1\"\n    der_program_id: \"p1\"\n",
			wantMsg: "der_controls[0]: id is required",
		},
		{
			name:    "der control missing der_program_id",
			yaml:    edev + "der_controls:\n  - end_device_id: \"e1\"\n    fsa_id: \"f1\"\n    id: \"a\"\n",
			wantMsg: "der_controls[0]",
		},
		{
			name: "duplicate der control in same scope",
			yaml: edev + `der_controls:
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
  - end_device_id: "e1"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "a"
`,
			wantMsg:  `der_controls[1] (id="a", scope="e1/f1/p1")`,
			wantDupe: true,
		},
		{
			name:    "der curve missing id",
			yaml:    "der_curves:\n  - curve_type: 0\n",
			wantMsg: "der_curves[0]: id is required",
		},
		{
			name:     "duplicate der curve id",
			yaml:     "der_curves:\n  - id: \"c1\"\n    curve_type: 0\n  - id: \"c1\"\n    curve_type: 0\n",
			wantMsg:  `der_curves[1] (id="c1")`,
			wantDupe: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadYAML(t, tc.yaml)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tc.wantMsg)
			}
			// Every applySpec failure is wrapped once with the fixture
			// path so a boot failure names the offending file.
			if !strings.Contains(err.Error(), "fixture.yaml") {
				t.Errorf("error %q does not cite the fixture path", err)
			}
			if tc.wantDupe && !errors.Is(err, store.ErrAlreadyExists) {
				t.Errorf("error chain lacks store.ErrAlreadyExists: %v", err)
			}
		})
	}
}

// TestLoadIsNotIdempotent pins the documented behavior that loading the
// same fixture twice into one Target fails with store.ErrAlreadyExists
// rather than overwriting, and that the target is left partially
// populated from the first pass.
func TestLoadIsNotIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fixture.yaml")
	body := `
end_devices:
  - id: "e1"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 0
der_curves:
  - id: "c1"
    curve_type: 0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	target := freshTarget()
	ctx := context.Background()
	if err := bootfixture.Load(ctx, target, path); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	err := bootfixture.Load(ctx, target, path)
	if err == nil {
		t.Fatal("second Load into the same target succeeded, want ErrAlreadyExists")
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("error chain lacks store.ErrAlreadyExists: %v", err)
	}
	// The first pass's data is intact; the second pass did not clobber it.
	if n, err := target.DERCurves.Count(ctx); err != nil || n != 1 {
		t.Errorf("DERCurves.Count = %d, %v, want 1, nil", n, err)
	}
}

// TestLoadEmptyFixture covers the all-sections-absent path: a valid but
// empty document must load cleanly and leave every store empty rather
// than synthesizing placeholder resources.
func TestLoadEmptyFixture(t *testing.T) {
	t.Parallel()

	target := mustLoadYAML(t, "end_devices: []\n")
	ctx := context.Background()

	if n, err := target.DERCurves.Count(ctx); err != nil || n != 0 {
		t.Errorf("DERCurves.Count = %d, %v, want 0, nil", n, err)
	}
	list, err := target.EndDevices.List(ctx, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("EndDevices.List: %v", err)
	}
	if list.All != 0 {
		t.Errorf("EndDevices.All = %d, want 0", list.All)
	}
}
