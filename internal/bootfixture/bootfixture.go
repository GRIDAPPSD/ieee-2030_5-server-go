// Package bootfixture loads a YAML topology fixture into the server's
// stores at boot time. It is the production-side counterpart to the
// in-test loader at test/csip/csiptest (#52), and shares the same
// YAML schema so a fixture authored for the harness can be reused as a
// `SEP2_BOOT_FIXTURE` knob in deployment.
//
// Design rules:
//
//   - Writes through the public pkg/store + pkg/store/memory types only.
//     No reach into internal/server private state.
//   - Decoupled from internal/server via the Target struct (caller adapts
//     its *server.Stores to Target at the call site, same shape csiptest
//     uses).
//   - Errors wrap with %w and cite the fixture path at every boundary so
//     a malformed file surfaces in server logs without ambiguity.
//   - Load is strict: loading twice without reset errors with
//     store.ErrAlreadyExists. Reconcile is the server's boot path; with a
//     data_dir it seeds each persisted record once and yields to persisted
//     state (#352).
//
// Schema duplication: bootfixture intentionally duplicates the YAML schema
// from test/csip/csiptest/loader.go rather than depending on the test
// package (production code under internal/ cannot import test/). The two
// schemas are kept in sync by convention; flagged as future-work in the
// #75 journal entry. Either both move into bootfixture (csiptest
// re-exports) or stay duplicated - that decision belongs to a follow-up
// ticket once a third consumer appears.
package bootfixture

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"gopkg.in/yaml.v3"
)

// singletonKey mirrors internal/handler.SingletonKey for the same reason
// csiptest.singletonKey does - the production handlers store
// DefaultDERControl under inner id "default" inside its composite scope.
const singletonKey = "default"

// Target is the set of stores the loader writes into. Each field maps
// 1:1 to an internal/server.Stores field; the caller in server.Run()
// builds Target inline from its *server.Stores at the call site.
//
// Why not accept *server.Stores directly: server.Run is the only caller
// today, but the indirection lets bootfixture stay free of an
// internal/server import (which would otherwise create a cycle).
type Target struct {
	EndDevices store.EndDeviceStore
	FSAs       *memory.ScopedStore[sep2.FunctionSetAssignments]
	// DERPrograms is the store.ScopedStore contract, not a concrete
	// *memory.ScopedStore. The loader only reads and Creates, and the server
	// hands over *memory.DERProgramStore, whose collection stopped being an
	// exported embedded field in core. The contract is what both shapes
	// have in common and all this loader ever needed.
	DERPrograms        store.ScopedStore[sep2.DERProgram]
	DERControls        *memory.ScopedStore[sep2.DERControl]
	DefaultDERControls *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves          *memory.Store[sep2.DERCurve]
}

// Spec is the YAML schema. Field tags use snake_case to match the
// fixture files; Go names stay MixedCaps. Unknown YAML keys are rejected
// (strict decode) so a typo in a fixture surfaces as a loader error.
type Spec struct {
	EndDevices         []EndDeviceSpec         `yaml:"end_devices"`
	FSAs               []FSASpec               `yaml:"fsas"`
	DERPrograms        []DERProgramSpec        `yaml:"der_programs"`
	DefaultDERControls []DefaultDERControlSpec `yaml:"default_der_controls"`
	DERControls        []DERControlSpec        `yaml:"der_controls"`
	DERCurves          []DERCurveSpec          `yaml:"der_curves"`
}

// EndDeviceSpec describes one EndDevice.
type EndDeviceSpec struct {
	ID                             string   `yaml:"id"`
	SFDI                           string   `yaml:"sfdi"`
	LFDI                           string   `yaml:"lfdi"`
	Enabled                        *bool    `yaml:"enabled,omitempty"`
	ChangedTime                    int64    `yaml:"changed_time"`
	RegistrationLink               string   `yaml:"registration_link,omitempty"`
	FunctionSetAssignmentsListLink *ListRef `yaml:"function_set_assignments_list_link,omitempty"`
	DERListLink                    *ListRef `yaml:"der_list_link,omitempty"`
}

// FSASpec describes one FunctionSetAssignments scoped under an EndDevice.
type FSASpec struct {
	EndDeviceID        string   `yaml:"end_device_id"`
	ID                 string   `yaml:"id"`
	MRID               string   `yaml:"mrid,omitempty"`
	Description        string   `yaml:"description,omitempty"`
	DERProgramListLink *ListRef `yaml:"der_program_list_link,omitempty"`
}

// DERProgramSpec describes one DERProgram scoped under an EndDevice.
type DERProgramSpec struct {
	EndDeviceID           string   `yaml:"end_device_id"`
	FSAID                 string   `yaml:"fsa_id,omitempty"`
	ID                    string   `yaml:"id"`
	MRID                  string   `yaml:"mrid,omitempty"`
	Description           string   `yaml:"description,omitempty"`
	Primacy               uint8    `yaml:"primacy"`
	DefaultDERControlLink string   `yaml:"default_der_control_link,omitempty"`
	DERControlListLink    *ListRef `yaml:"der_control_list_link,omitempty"`
	DERCurveListLink      *ListRef `yaml:"der_curve_list_link,omitempty"`
}

// DefaultDERControlSpec describes one DefaultDERControl scoped under a
// (EndDevice, FSA, DERProgram) triple.
type DefaultDERControlSpec struct {
	EndDeviceID    string              `yaml:"end_device_id"`
	FSAID          string              `yaml:"fsa_id"`
	DERProgramID   string              `yaml:"der_program_id"`
	MRID           string              `yaml:"mrid,omitempty"`
	DERControlBase *DERControlBaseSpec `yaml:"der_control_base,omitempty"`
}

// DERControlSpec describes one DERControl scoped under
// (EndDevice, FSA, DERProgram).
type DERControlSpec struct {
	EndDeviceID    string              `yaml:"end_device_id"`
	FSAID          string              `yaml:"fsa_id"`
	DERProgramID   string              `yaml:"der_program_id"`
	ID             string              `yaml:"id"`
	MRID           string              `yaml:"mrid,omitempty"`
	DERControlBase *DERControlBaseSpec `yaml:"der_control_base,omitempty"`
}

// DERCurveSpec describes one DERCurve in the global curve store.
//
// CreationTime is optional. When the spec omits it, the served curve
// carries the time the fixture was loaded rather than 0: DERCurve's
// creationTime is a required element, and an unset one round-trips as a
// schema-valid but wrong claim that the curve was created at the Unix
// epoch (#539).
type DERCurveSpec struct {
	ID           string          `yaml:"id"`
	MRID         string          `yaml:"mrid,omitempty"`
	Description  string          `yaml:"description,omitempty"`
	CurveType    uint8           `yaml:"curve_type"`
	CreationTime *int64          `yaml:"creation_time,omitempty"`
	CurveData    []CurveDataSpec `yaml:"curve_data,omitempty"`
}

// CurveDataSpec is one (x, y) point on a DERCurve.
type CurveDataSpec struct {
	X int32 `yaml:"x"`
	Y int32 `yaml:"y"`
}

// DERControlBaseSpec captures the subset of DERControlBase fields the
// bootfixture set needs today. Add fields incrementally.
//
// OpModFixedW and OpModMaxLimW are bare integers in hundredths of a percent
// (4000 = 40.00%), not watts; a multiplier/value mapping fails to decode.
type DERControlBaseSpec struct {
	OpModConnect   *bool               `yaml:"op_mod_connect,omitempty"`
	OpModEnergize  *bool               `yaml:"op_mod_energize,omitempty"`
	OpModFixedW    *sep2.SignedPerCent `yaml:"op_mod_fixed_w,omitempty"`
	OpModMaxLimW   *sep2.PerCent       `yaml:"op_mod_max_lim_w,omitempty"`
	OpModTargetW   *ActivePowerSpec    `yaml:"op_mod_target_w,omitempty"`
	OpModVoltVar   *int32              `yaml:"op_mod_volt_var,omitempty"`
	OpModFreqDroop *uint16             `yaml:"op_mod_freq_droop,omitempty"`
	RampTms        *uint16             `yaml:"ramp_tms,omitempty"`
}

// validatePerCents refuses a percent the XML encoder would refuse, so an
// out-of-range fixture fails at load instead of when the control is served.
func (s *DERControlBaseSpec) validatePerCents() error {
	if s == nil {
		return nil
	}
	if s.OpModFixedW != nil {
		if _, err := xml.Marshal(*s.OpModFixedW); err != nil {
			return fmt.Errorf("op_mod_fixed_w: %w", err)
		}
	}
	if s.OpModMaxLimW != nil {
		if _, err := xml.Marshal(*s.OpModMaxLimW); err != nil {
			return fmt.Errorf("op_mod_max_lim_w: %w", err)
		}
	}
	return nil
}

// ActivePowerSpec is the YAML shape of sep2.ActivePower. Value is int16
// (not int64) because sep2.ActivePower.Value is xs:short (XSD Int16,
// -32768..32767) per sep.xsd: see ieee-2030_5-core-go's ActivePower doc
// comment for the rationale.
type ActivePowerSpec struct {
	Multiplier int8  `yaml:"multiplier"`
	Value      int16 `yaml:"value"`
}

// ListRef is the YAML shape of sep2.ListLink (href + advertised count).
type ListRef struct {
	Href string `yaml:"href"`
	All  uint32 `yaml:"all,omitempty"`
}

// Load reads a YAML fixture at path, decodes it strictly (unknown keys
// reject), and writes the resulting topology into target via the public
// store API.
//
// Behavior mirrors csiptest.Load:
//
//   - File read errors wrap the OS error with the path.
//   - YAML decode errors wrap yaml.v3's error with the path.
//   - Topology errors (orphan FSA, duplicate ID, store rejection) wrap
//     store sentinels so callers can errors.Is them.
//   - On any failure mid-load the target may be partially populated.
func Load(ctx context.Context, target *Target, path string) error {
	spec, err := readSpec(target, path)
	if err != nil {
		return err
	}
	if err := applySpec(ctx, target, spec); err != nil {
		return fmt.Errorf("bootfixture: apply fixture %s: %w", path, err)
	}
	return nil
}

func readSpec(target *Target, path string) (*Spec, error) {
	if target == nil {
		return nil, fmt.Errorf("bootfixture: load %s: target is nil", path)
	}
	if path == "" {
		return nil, errors.New("bootfixture: load: path is empty")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bootfixture: read fixture %s: %w", path, err)
	}

	var spec Spec
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("bootfixture: decode fixture %s: %w", path, err)
	}
	return &spec, nil
}

func applySpec(ctx context.Context, target *Target, spec *Spec) error {
	edevIDs := make(map[string]struct{}, len(spec.EndDevices))
	for i, e := range spec.EndDevices {
		if e.ID == "" {
			return fmt.Errorf("end_devices[%d]: id is required", i)
		}
		if _, dup := edevIDs[e.ID]; dup {
			return fmt.Errorf("end_devices[%d]: duplicate id %q", i, e.ID)
		}
		edevIDs[e.ID] = struct{}{}

		if err := createEndDevice(ctx, target, i, e); err != nil {
			return err
		}
	}

	for i, f := range spec.FSAs {
		if f.ID == "" {
			return fmt.Errorf("fsas[%d]: id is required", i)
		}
		if f.EndDeviceID == "" {
			return fmt.Errorf("fsas[%d] (id=%q): end_device_id is required", i, f.ID)
		}
		if _, ok := edevIDs[f.EndDeviceID]; !ok {
			return fmt.Errorf("fsas[%d] (id=%q): unknown end_device_id %q", i, f.ID, f.EndDeviceID)
		}
		if err := createFSA(ctx, target, i, f); err != nil {
			return err
		}
	}

	for i, p := range spec.DERPrograms {
		if p.ID == "" {
			return fmt.Errorf("der_programs[%d]: id is required", i)
		}
		if p.EndDeviceID == "" {
			return fmt.Errorf("der_programs[%d] (id=%q): end_device_id is required", i, p.ID)
		}
		if _, ok := edevIDs[p.EndDeviceID]; !ok {
			return fmt.Errorf("der_programs[%d] (id=%q): unknown end_device_id %q", i, p.ID, p.EndDeviceID)
		}
		if err := createDERProgram(ctx, target, i, p); err != nil {
			return err
		}
	}

	for i, d := range spec.DefaultDERControls {
		if d.EndDeviceID == "" || d.FSAID == "" || d.DERProgramID == "" {
			return fmt.Errorf("default_der_controls[%d]: end_device_id, fsa_id, der_program_id all required", i)
		}
		if err := d.DERControlBase.validatePerCents(); err != nil {
			return fmt.Errorf("default_der_controls[%d]: %w", i, err)
		}
		if err := createDefaultDERControl(ctx, target, i, d); err != nil {
			return err
		}
	}

	for i, c := range spec.DERControls {
		if c.ID == "" {
			return fmt.Errorf("der_controls[%d]: id is required", i)
		}
		if c.EndDeviceID == "" || c.FSAID == "" || c.DERProgramID == "" {
			return fmt.Errorf("der_controls[%d] (id=%q): end_device_id, fsa_id, der_program_id all required", i, c.ID)
		}
		if err := c.DERControlBase.validatePerCents(); err != nil {
			return fmt.Errorf("der_controls[%d] (id=%q): %w", i, c.ID, err)
		}
		if err := createDERControl(ctx, target, i, c); err != nil {
			return err
		}
	}

	loadTime := time.Now().Unix()
	for i, c := range spec.DERCurves {
		if c.ID == "" {
			return fmt.Errorf("der_curves[%d]: id is required", i)
		}
		if err := createDERCurve(ctx, target, i, c, loadTime); err != nil {
			return err
		}
	}

	return nil
}

// The create helpers are shared by Load and Reconcile so both name a failing
// fixture entry the same way.

func createEndDevice(ctx context.Context, target *Target, i int, e EndDeviceSpec) error {
	if err := target.EndDevices.Create(ctx, e.ID, buildEndDevice(e)); err != nil {
		return fmt.Errorf("end_devices[%d] (id=%q): create: %w", i, e.ID, err)
	}
	return nil
}

func createFSA(ctx context.Context, target *Target, i int, f FSASpec) error {
	if err := target.FSAs.Create(ctx, f.EndDeviceID, f.ID, buildFSA(f)); err != nil {
		return fmt.Errorf("fsas[%d] (id=%q, edev=%q): create: %w", i, f.ID, f.EndDeviceID, err)
	}
	return nil
}

func createDERProgram(ctx context.Context, target *Target, i int, p DERProgramSpec) error {
	if err := target.DERPrograms.Create(ctx, p.EndDeviceID, p.ID, buildDERProgram(p)); err != nil {
		return fmt.Errorf("der_programs[%d] (id=%q): create: %w", i, p.ID, err)
	}
	return nil
}

func createDefaultDERControl(ctx context.Context, target *Target, i int, d DefaultDERControlSpec) error {
	key := compositeKey(d.EndDeviceID, d.FSAID, d.DERProgramID)
	if err := target.DefaultDERControls.Create(ctx, key, singletonKey, buildDefaultDERControl(d)); err != nil {
		return fmt.Errorf("default_der_controls[%d] (scope=%q): create: %w", i, key, err)
	}
	return nil
}

func createDERControl(ctx context.Context, target *Target, i int, c DERControlSpec) error {
	key := compositeKey(c.EndDeviceID, c.FSAID, c.DERProgramID)
	if err := target.DERControls.Create(ctx, key, c.ID, buildDERControl(c)); err != nil {
		return fmt.Errorf("der_controls[%d] (id=%q, scope=%q): create: %w", i, c.ID, key, err)
	}
	return nil
}

func createDERCurve(ctx context.Context, target *Target, i int, c DERCurveSpec, loadTime int64) error {
	if err := target.DERCurves.Create(ctx, c.ID, buildDERCurve(c, loadTime)); err != nil {
		return fmt.Errorf("der_curves[%d] (id=%q): create: %w", i, c.ID, err)
	}
	return nil
}

func buildEndDevice(s EndDeviceSpec) sep2.EndDevice {
	dev := sep2.EndDevice{
		ChangedTime: s.ChangedTime,
		SFDI:        s.SFDI,
		LFDI:        s.LFDI,
	}
	dev.Href = fmt.Sprintf("/edev/%s", s.ID)
	if s.Enabled != nil {
		v := *s.Enabled
		dev.Enabled = &v
	}
	if s.RegistrationLink != "" {
		dev.RegistrationLink = &sep2.Link{Href: s.RegistrationLink}
	}
	if s.FunctionSetAssignmentsListLink != nil {
		dev.FunctionSetAssignmentsListLink = &sep2.ListLink{
			Href: s.FunctionSetAssignmentsListLink.Href,
			All:  s.FunctionSetAssignmentsListLink.All,
		}
	}
	if s.DERListLink != nil {
		dev.DERListLink = &sep2.ListLink{
			Href: s.DERListLink.Href,
			All:  s.DERListLink.All,
		}
	}
	return dev
}

func buildFSA(s FSASpec) sep2.FunctionSetAssignments {
	fsa := sep2.FunctionSetAssignments{
		MRID:        s.MRID,
		Description: s.Description,
	}
	fsa.Href = fmt.Sprintf("/edev/%s/fsa/%s", s.EndDeviceID, s.ID)
	if s.DERProgramListLink != nil {
		fsa.DERProgramListLink = &sep2.ListLink{
			Href: s.DERProgramListLink.Href,
			All:  s.DERProgramListLink.All,
		}
	}
	return fsa
}

func buildDERProgram(s DERProgramSpec) sep2.DERProgram {
	prog := sep2.DERProgram{
		MRID:        s.MRID,
		Description: s.Description,
		Primacy:     s.Primacy,
	}
	prog.Href = fmt.Sprintf("/edev/%s/derp/%s", s.EndDeviceID, s.ID)
	if s.DefaultDERControlLink != "" {
		prog.DefaultDERControlLink = &sep2.Link{Href: s.DefaultDERControlLink}
	}
	if s.DERControlListLink != nil {
		prog.DERControlListLink = &sep2.ListLink{
			Href: s.DERControlListLink.Href,
			All:  s.DERControlListLink.All,
		}
	}
	if s.DERCurveListLink != nil {
		prog.DERCurveListLink = &sep2.ListLink{
			Href: s.DERCurveListLink.Href,
			All:  s.DERCurveListLink.All,
		}
	}
	return prog
}

func buildDefaultDERControl(s DefaultDERControlSpec) sep2.DefaultDERControl {
	dc := sep2.DefaultDERControl{MRID: s.MRID}
	dc.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/dderc", s.EndDeviceID, s.FSAID, s.DERProgramID)
	if s.DERControlBase != nil {
		base := buildDERControlBase(*s.DERControlBase)
		dc.DERControlBase = &base
	}
	return dc
}

func buildDERControl(s DERControlSpec) sep2.DERControl {
	dc := sep2.DERControl{}
	dc.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc/%s",
		s.EndDeviceID, s.FSAID, s.DERProgramID, s.ID)
	if s.DERControlBase != nil {
		base := buildDERControlBase(*s.DERControlBase)
		dc.DERControlBase = &base
	}
	return dc
}

func buildDERControlBase(s DERControlBaseSpec) sep2.DERControlBase {
	var base sep2.DERControlBase
	if s.OpModConnect != nil {
		v := *s.OpModConnect
		base.OpModConnect = &v
	}
	if s.OpModEnergize != nil {
		v := *s.OpModEnergize
		base.OpModEnergize = &v
	}
	if s.OpModFixedW != nil {
		v := *s.OpModFixedW
		base.OpModFixedW = &v
	}
	if s.OpModMaxLimW != nil {
		v := *s.OpModMaxLimW
		base.OpModMaxLimW = &v
	}
	if s.OpModTargetW != nil {
		base.OpModTargetW = &sep2.ActivePower{Multiplier: s.OpModTargetW.Multiplier, Value: s.OpModTargetW.Value}
	}
	if s.OpModVoltVar != nil {
		v := *s.OpModVoltVar
		base.OpModVoltVar = &v
	}
	if s.OpModFreqDroop != nil {
		v := *s.OpModFreqDroop
		base.OpModFreqDroop = &v
	}
	if s.RampTms != nil {
		v := *s.RampTms
		base.RampTms = &v
	}
	return base
}

// buildDERCurve maps a DERCurveSpec onto sep2.DERCurve. CreationTime
// follows the spec's value when given; otherwise it is loadTime, the time
// the enclosing fixture was loaded, so a served curve never claims the
// Unix epoch (#539).
func buildDERCurve(s DERCurveSpec, loadTime int64) sep2.DERCurve {
	creationTime := loadTime
	if s.CreationTime != nil {
		creationTime = *s.CreationTime
	}
	curve := sep2.DERCurve{
		MRID:         s.MRID,
		Description:  s.Description,
		CurveType:    s.CurveType,
		CreationTime: creationTime,
	}
	curve.Href = fmt.Sprintf("/dc/%s", s.ID)
	if len(s.CurveData) > 0 {
		curve.CurveData = make([]sep2.CurveData, len(s.CurveData))
		for i, p := range s.CurveData {
			curve.CurveData[i] = sep2.CurveData{XValue: p.X, YValue: p.Y}
		}
	}
	return curve
}

func compositeKey(edev, fsa, derp string) string {
	return edev + "/" + fsa + "/" + derp
}
