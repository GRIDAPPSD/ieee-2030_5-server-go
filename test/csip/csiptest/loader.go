// #52 fixture loader for the CSIP conformance harness.
//
// Test authors describe their topology as YAML - EndDevices, FSAs,
// DERPrograms, DefaultDERControls, DERControls, DERCurves - and Load
// seeds the spec server's stores accordingly. Phase 3 tests then
// assert the wire shape and downstream behavior without re-encoding
// the topology inline in Go.
//
// Design rules:
//
//   - Loader writes through the public pkg/store/ + pkg/store/memory/
//     types only. It does NOT reach into any private package state.
//     Same fixtures stay valid against a future SQL-backed store
//     (per phase doc #52 hard constraint).
//   - Loader does not depend on internal/server. The consumer adapts
//     its server.Stores to Target at the call site - Phase 3 tests
//     own that 8-line adapter.
//   - Loader is idempotent on a fresh Target: Load -> reset Target ->
//     Load yields the same state. Loading twice without resetting
//     errors with store.ErrAlreadyExists, by design - the harness
//     test isolation rule is "one Target per test" (paired with
//     #53's BootServer).
//   - Errors wrap with %w at every boundary (read, unmarshal, store
//     write), and every error message cites the fixture path so a
//     malformed file surfaces in test logs without ambiguity.
package csiptest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"gopkg.in/yaml.v3"
)

// singletonKey mirrors internal/handler.SingletonKey. DefaultDERControl
// is stored under this key within its (edev/fsa/derp) scope so the
// handler at /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc finds it.
//
// Replicated as an unexported constant rather than imported so the
// harness package does not pull in internal/handler. If the server
// constant ever changes, an in-package test would have to be adjusted
// in lockstep; today's fixtures intentionally do not assume the
// constant value beyond this seed.
const singletonKey = "default"

// Target is the set of stores the loader writes into. Each field maps
// 1:1 to a server.Stores field; the consumer assembles Target from
// its &server.Stores{...} at the call site. The loader only reads
// from this struct, never the inverse.
//
// Why not accept *server.Stores directly: the csiptest package lives
// in test/csip/ and is consumed both by Phase 3 server tests AND by
// the loader's own _test.go below. The latter must run without
// pulling in internal/server. Target is the narrow interface that
// decouples them - and a SQL-backed Store implementation slots in
// without the loader caring.
type Target struct {
	EndDevices store.EndDeviceStore
	FSAs       *memory.ScopedStore[sep2.FunctionSetAssignments]
	// DERPrograms is the store.ScopedStore contract, so a booted server's
	// *memory.DERProgramStore and a bare *memory.ScopedStore both satisfy
	// it. Core unexported the wrapper's inner collection, so the contract
	// is the only surface the two share.
	DERPrograms        store.ScopedStore[sep2.DERProgram]
	DERControls        *memory.ScopedStore[sep2.DERControl]
	DefaultDERControls *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves          *memory.Store[sep2.DERCurve]
	// EndDeviceManagers receives the pairs fixtures declare with managed_by.
	// A fixture that declares one fails to load while this is unset.
	EndDeviceManagers store.EndDeviceManagementStore
}

// NewTarget returns a Target whose stores are fresh in-memory
// instances. Useful for the loader's own unit tests; Phase 3 tests
// typically build Target from their existing server.Stores instead.
func NewTarget() *Target {
	return &Target{
		EndDevices:         memory.NewEndDeviceStore(),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms:        memory.NewScopedStore[sep2.DERProgram](),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
	}
}

// Spec is the YAML schema. Field tags use snake_case to match the
// fixture files; Go names stay MixedCaps. Unknown YAML keys are
// rejected (strict decode) so a typo in a fixture surfaces as a
// loader error, not as silently missing topology.
type Spec struct {
	EndDevices         []EndDeviceSpec         `yaml:"end_devices"`
	FSAs               []FSASpec               `yaml:"fsas"`
	DERPrograms        []DERProgramSpec        `yaml:"der_programs"`
	DefaultDERControls []DefaultDERControlSpec `yaml:"default_der_controls"`
	DERControls        []DERControlSpec        `yaml:"der_controls"`
	DERCurves          []DERCurveSpec          `yaml:"der_curves"`
}

// EndDeviceSpec describes one EndDevice. ID is the store key, also
// the path segment in /edev/{id}.
type EndDeviceSpec struct {
	ID                             string   `yaml:"id"`
	SFDI                           string   `yaml:"sfdi"`
	LFDI                           string   `yaml:"lfdi"`
	Enabled                        *bool    `yaml:"enabled,omitempty"`
	ChangedTime                    int64    `yaml:"changed_time"`
	RegistrationLink               string   `yaml:"registration_link,omitempty"`
	FunctionSetAssignmentsListLink *ListRef `yaml:"function_set_assignments_list_link,omitempty"`
	DERListLink                    *ListRef `yaml:"der_list_link,omitempty"`
	// ManagedBy names the id of the EndDevice in this fixture that manages
	// this one. The pair is recorded by LFDI, after any Bind has applied.
	ManagedBy string `yaml:"managed_by,omitempty"`
}

// FSASpec describes one FunctionSetAssignments scoped under an
// EndDevice. EndDeviceID matches an EndDeviceSpec.ID; the loader
// rejects orphans.
type FSASpec struct {
	EndDeviceID        string   `yaml:"end_device_id"`
	ID                 string   `yaml:"id"`
	MRID               string   `yaml:"mrid,omitempty"`
	Description        string   `yaml:"description,omitempty"`
	DERProgramListLink *ListRef `yaml:"der_program_list_link,omitempty"`
}

// DERProgramSpec describes one DERProgram scoped under an EndDevice
// (matching the server's scopedListHandler which keys DERPrograms by
// the path's {id} segment alone; the FSA position is encoded in the
// URL the GET handler builds, but the store scope key is just the
// EndDevice). FSAID is fixture metadata that downstream tests use to
// assert the priority chain; it is NOT used as the store scope.
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

// DefaultDERControlSpec describes one DefaultDERControl scoped under
// a (EndDevice, FSA, DERProgram) triple. The server stores
// DefaultDERControl under composite key "{edev}/{fsa}/{derp}" with
// inner id "default" (handler.SingletonKey).
//
// SetGradW / SetSoftGradW live on the DefaultDERControl directly (per
// IEEE 2030.5 Section 10.11), not on DERControlBase - they are device-level
// default ramp rates, not per-event overrides. #140 wired these
// through after #135's BASIC-007 skip-flip.
type DefaultDERControlSpec struct {
	EndDeviceID    string              `yaml:"end_device_id"`
	FSAID          string              `yaml:"fsa_id"`
	DERProgramID   string              `yaml:"der_program_id"`
	MRID           string              `yaml:"mrid,omitempty"`
	DERControlBase *DERControlBaseSpec `yaml:"der_control_base,omitempty"`
	SetGradW       *uint16             `yaml:"set_grad_w,omitempty"`
	SetSoftGradW   *uint16             `yaml:"set_soft_grad_w,omitempty"`
}

// DERControlSpec describes one DERControl scoped under
// (EndDevice, FSA, DERProgram). Same composite scope key as
// DefaultDERControl; ID is the per-event key.
//
// #143 (BASIC-016..020 non-overlap event-prioritization) extended
// this with Interval, EventStatus, and Description so fixtures can
// express the per-event timing windows the V1.2 Section 8.16 to Section 8.20 procedures
// assert. MRID was previously declared but never copied to the
// rendered DERControl - #143 plumbs that through buildDERControl
// too (latent bug fix in scope).
type DERControlSpec struct {
	EndDeviceID    string              `yaml:"end_device_id"`
	FSAID          string              `yaml:"fsa_id"`
	DERProgramID   string              `yaml:"der_program_id"`
	ID             string              `yaml:"id"`
	MRID           string              `yaml:"mrid,omitempty"`
	Description    string              `yaml:"description,omitempty"`
	EventStatus    *EventStatusSpec    `yaml:"event_status,omitempty"`
	Interval       *IntervalSpec       `yaml:"interval,omitempty"`
	DERControlBase *DERControlBaseSpec `yaml:"der_control_base,omitempty"`
}

// EventStatusSpec is the YAML shape of sep2.EventStatus. The
// CSIP V1.2 event-prioritization procedures (BASIC-016..026) drive
// CurrentStatus through Scheduled (0), Active (1), Cancelled (2),
// Superseded (4), Complete (5). DateTime is the timestamp the status
// transition occurred - fixtures set this to the same epoch the
// containing Interval references so the wire-rendered event looks
// internally consistent.
type EventStatusSpec struct {
	CurrentStatus         uint8  `yaml:"current_status"`
	DateTime              int64  `yaml:"date_time"`
	PotentiallySuperseded bool   `yaml:"potentially_superseded,omitempty"`
	Reason                string `yaml:"reason,omitempty"`
}

// IntervalSpec is the YAML shape of sep2.DateTimeInterval. Start is
// the epoch seconds the event becomes Active; Duration is its length
// in seconds. The BASIC-016..020 non-overlap fixtures place events on
// disjoint [Start, Start+Duration) windows; #145 (overlapping)
// will reuse the same spec with intersecting windows.
type IntervalSpec struct {
	Start    int64  `yaml:"start"`
	Duration uint32 `yaml:"duration"`
}

// DERCurveSpec describes one DERCurve in the global curve store.
type DERCurveSpec struct {
	ID          string          `yaml:"id"`
	MRID        string          `yaml:"mrid,omitempty"`
	Description string          `yaml:"description,omitempty"`
	CurveType   uint8           `yaml:"curve_type"`
	CurveData   []CurveDataSpec `yaml:"curve_data,omitempty"`
}

// CurveDataSpec is one (x, y) point on a DERCurve.
type CurveDataSpec struct {
	X int32 `yaml:"x"`
	Y int32 `yaml:"y"`
}

// DERControlBaseSpec captures the subset of DERControlBase fields the
// CSIP V1.2 fixture set needs today. Add fields incrementally as new
// tests require them - keeping the surface narrow makes each fixture
// readable.
//
// #135 (BASIC-002 + BASIC-004..012) extended this with
// op_mod_fixed_pf_inject_w. #140 wired the remaining per-mode
// curve-reference fields (LVRT/HVRT/LFRT/HFRT, VoltWatt, FreqWatt) and
// flipped the BASIC-004/005/007/011/012 skips to active assertions.
// SetGradW/SetSoftGradW live on DefaultDERControlSpec, not here, per
// IEEE 2030.5 Section 10.11.
type DERControlBaseSpec struct {
	OpModConnect                *bool                 `yaml:"op_mod_connect,omitempty"`
	OpModEnergize               *bool                 `yaml:"op_mod_energize,omitempty"`
	OpModFixedW                 *ActivePowerSpec      `yaml:"op_mod_fixed_w,omitempty"`
	OpModFixedPFInjectW         *FixedPowerFactorSpec `yaml:"op_mod_fixed_pf_inject_w,omitempty"`
	OpModFreqDroop              *uint16               `yaml:"op_mod_freq_droop,omitempty"`
	OpModFreqWatt               *int32                `yaml:"op_mod_freq_watt,omitempty"`
	OpModHFRTMustTrip           *int32                `yaml:"op_mod_hfrt_must_trip,omitempty"`
	OpModHVRTMomentaryCessation *int32                `yaml:"op_mod_hvrt_momentary_cessation,omitempty"`
	OpModHVRTMustTrip           *int32                `yaml:"op_mod_hvrt_must_trip,omitempty"`
	OpModLFRTMustTrip           *int32                `yaml:"op_mod_lfrt_must_trip,omitempty"`
	OpModLVRTMomentaryCessation *int32                `yaml:"op_mod_lvrt_momentary_cessation,omitempty"`
	OpModLVRTMustTrip           *int32                `yaml:"op_mod_lvrt_must_trip,omitempty"`
	OpModMaxLimW                *ActivePowerSpec      `yaml:"op_mod_max_lim_w,omitempty"`
	OpModTargetW                *ActivePowerSpec      `yaml:"op_mod_target_w,omitempty"`
	OpModVoltVar                *int32                `yaml:"op_mod_volt_var,omitempty"`
	OpModVoltWatt               *int32                `yaml:"op_mod_volt_watt,omitempty"`
	RampTms                     *uint16               `yaml:"ramp_tms,omitempty"`
}

// ActivePowerSpec is the YAML shape of sep2.ActivePower. Value is int16
// (not int64) because sep2.ActivePower.Value is xs:short (XSD Int16,
// -32768..32767) per sep.xsd: see ieee-2030_5-core-go's ActivePower doc
// comment for the rationale.
type ActivePowerSpec struct {
	Multiplier int8  `yaml:"multiplier"`
	Value      int16 `yaml:"value"`
}

// FixedPowerFactorSpec is the YAML shape of sep2.FixedPowerFactor.
type FixedPowerFactorSpec struct {
	Displacement uint16 `yaml:"displacement"`
	Excitation   bool   `yaml:"excitation"`
	Multiplier   int8   `yaml:"multiplier,omitempty"`
}

// ListRef is the YAML shape of sep2.ListLink (href + advertised count).
type ListRef struct {
	Href string `yaml:"href"`
	All  uint32 `yaml:"all,omitempty"`
}

// Load reads a YAML fixture at path, decodes it strictly (unknown
// keys reject), and writes the resulting topology into target via
// the public store API.
//
// Behavior:
//
//   - File read errors wrap the OS error with the path.
//   - YAML decode errors wrap yaml.v3's error with the path.
//   - Topology errors (orphan FSA, duplicate ID, store rejection)
//     wrap store sentinels (store.ErrAlreadyExists / ErrNotFound)
//     so callers can errors.Is them.
//   - On any failure mid-load the target may be partially populated.
//     This is deliberate: harness tests detect by asserting empty
//     state before Load (NewTarget guarantees that), and the next
//     iteration uses a fresh Target. Backing out partial writes
//     would double the loader's surface area for no benefit in the
//     scoped use-case.
func Load(ctx context.Context, target *Target, path string, opts ...LoadOption) error {
	if target == nil {
		return fmt.Errorf("csiptest: load %s: target is nil", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("csiptest: read fixture %s: %w", path, err)
	}

	var spec Spec
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // reject unknown keys -> typos fail loud
	if err := dec.Decode(&spec); err != nil {
		return fmt.Errorf("csiptest: decode fixture %s: %w", path, err)
	}

	if err := applySpec(ctx, target, &spec, opts); err != nil {
		return fmt.Errorf("csiptest: apply fixture %s: %w", path, err)
	}
	return nil
}

// LoadSpec applies an already-decoded Spec to target. Exposed so
// tests can construct a Spec in-memory without round-tripping
// through YAML - useful for property-based fixture variants.
func LoadSpec(ctx context.Context, target *Target, spec *Spec, opts ...LoadOption) error {
	if target == nil {
		return errors.New("csiptest: LoadSpec: target is nil")
	}
	if spec == nil {
		return errors.New("csiptest: LoadSpec: spec is nil")
	}
	if err := applySpec(ctx, target, spec, opts); err != nil {
		return fmt.Errorf("csiptest: apply spec: %w", err)
	}
	return nil
}

// LoadOption adjusts how a fixture is applied.
type LoadOption func(*loadConfig)

type loadConfig struct {
	binds []binding
}

type binding struct {
	endDeviceID string
	identity    DeviceIdentity
}

// Bind overwrites the LFDI and SFDI of the fixture EndDevice endDeviceID with
// those of id, so the certificate a test presents owns that record. Loading
// fails if the EndDevice is not in the fixture or is bound twice.
func Bind(endDeviceID string, id DeviceIdentity) LoadOption {
	return func(c *loadConfig) {
		c.binds = append(c.binds, binding{endDeviceID: endDeviceID, identity: id})
	}
}

func applySpec(ctx context.Context, target *Target, spec *Spec, opts []LoadOption) error {
	// EndDevices first - FSA/DERProgram inserts assume the EndDevice
	// store carries the parent record. The store interface itself does
	// not enforce parent existence (ScopedStore.ForParent auto-creates
	// the inner store), but a sane fixture lists the parent so a
	// future SQL-backed store with FK constraints just works.
	var cfg loadConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	bound, err := resolveBindings(spec, cfg.binds)
	if err != nil {
		return err
	}

	edevIDs := make(map[string]struct{}, len(spec.EndDevices))
	lfdiOf := make(map[string]string, len(spec.EndDevices))
	for i, e := range spec.EndDevices {
		if e.ID == "" {
			return fmt.Errorf("end_devices[%d]: id is required", i)
		}
		if _, dup := edevIDs[e.ID]; dup {
			return fmt.Errorf("end_devices[%d]: duplicate id %q", i, e.ID)
		}
		edevIDs[e.ID] = struct{}{}

		dev := buildEndDevice(e)
		if id, ok := bound[e.ID]; ok {
			dev.LFDI, dev.SFDI = id.LFDI, id.SFDI
		}
		lfdiOf[e.ID] = dev.LFDI
		if err := target.EndDevices.Create(ctx, e.ID, dev); err != nil {
			return fmt.Errorf("end_devices[%d] (id=%q): create: %w", i, e.ID, err)
		}
	}
	if err := assignManagers(ctx, target, spec, lfdiOf); err != nil {
		return err
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

		fsa := buildFSA(f)
		if err := target.FSAs.Create(ctx, f.EndDeviceID, f.ID, fsa); err != nil {
			return fmt.Errorf("fsas[%d] (id=%q, edev=%q): create: %w", i, f.ID, f.EndDeviceID, err)
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

		prog := buildDERProgram(p)
		if err := target.DERPrograms.Create(ctx, p.EndDeviceID, p.ID, prog); err != nil {
			return fmt.Errorf("der_programs[%d] (id=%q): create: %w", i, p.ID, err)
		}
	}

	for i, d := range spec.DefaultDERControls {
		if d.EndDeviceID == "" || d.FSAID == "" || d.DERProgramID == "" {
			return fmt.Errorf("default_der_controls[%d]: end_device_id, fsa_id, der_program_id all required", i)
		}
		key := compositeKey(d.EndDeviceID, d.FSAID, d.DERProgramID)
		dc := buildDefaultDERControl(d)
		if err := target.DefaultDERControls.Create(ctx, key, singletonKey, dc); err != nil {
			return fmt.Errorf("default_der_controls[%d] (scope=%q): create: %w", i, key, err)
		}
	}

	for i, c := range spec.DERControls {
		if c.ID == "" {
			return fmt.Errorf("der_controls[%d]: id is required", i)
		}
		if c.EndDeviceID == "" || c.FSAID == "" || c.DERProgramID == "" {
			return fmt.Errorf("der_controls[%d] (id=%q): end_device_id, fsa_id, der_program_id all required", i, c.ID)
		}
		key := compositeKey(c.EndDeviceID, c.FSAID, c.DERProgramID)
		dc := buildDERControl(c)
		if err := target.DERControls.Create(ctx, key, c.ID, dc); err != nil {
			return fmt.Errorf("der_controls[%d] (id=%q, scope=%q): create: %w", i, c.ID, key, err)
		}
	}

	for i, c := range spec.DERCurves {
		if c.ID == "" {
			return fmt.Errorf("der_curves[%d]: id is required", i)
		}
		cur := buildDERCurve(c)
		if err := target.DERCurves.Create(ctx, c.ID, cur); err != nil {
			return fmt.Errorf("der_curves[%d] (id=%q): create: %w", i, c.ID, err)
		}
	}

	return nil
}

// resolveBindings maps each bound EndDevice id to its identity. It refuses a
// binding the fixture cannot honor before anything is written.
func resolveBindings(spec *Spec, binds []binding) (map[string]DeviceIdentity, error) {
	if len(binds) == 0 {
		return nil, nil
	}
	inFixture := make(map[string]bool, len(spec.EndDevices))
	for _, e := range spec.EndDevices {
		inFixture[e.ID] = true
	}
	bound := make(map[string]DeviceIdentity, len(binds))
	boundTo := make(map[string]string, len(binds))
	for _, b := range binds {
		if !inFixture[b.endDeviceID] {
			return nil, fmt.Errorf("bind: end device %q is not in the fixture", b.endDeviceID)
		}
		if b.identity.LFDI == "" || b.identity.SFDI == "" {
			return nil, fmt.Errorf("bind: end device %q: the identity has no LFDI or SFDI", b.endDeviceID)
		}
		if _, dup := bound[b.endDeviceID]; dup {
			return nil, fmt.Errorf("bind: end device %q is bound twice", b.endDeviceID)
		}
		if other, dup := boundTo[b.identity.LFDI]; dup {
			return nil, fmt.Errorf("bind: LFDI %s is bound to both %q and %q", b.identity.LFDI, other, b.endDeviceID)
		}
		bound[b.endDeviceID] = b.identity
		boundTo[b.identity.LFDI] = b.endDeviceID
	}
	return bound, nil
}

// assignManagers records every managed_by pair, by the LFDIs the EndDevices
// were created with.
func assignManagers(ctx context.Context, target *Target, spec *Spec, lfdiOf map[string]string) error {
	for i, e := range spec.EndDevices {
		if e.ManagedBy == "" {
			continue
		}
		manager, ok := lfdiOf[e.ManagedBy]
		if !ok {
			return fmt.Errorf("end_devices[%d] (id=%q): managed_by %q is not an end device in this fixture", i, e.ID, e.ManagedBy)
		}
		if store.IsAbsent(target.EndDeviceManagers) {
			return fmt.Errorf("end_devices[%d] (id=%q): managed_by is set but the target has no EndDeviceManagers store", i, e.ID)
		}
		if err := target.EndDeviceManagers.Assign(ctx, manager, lfdiOf[e.ID]); err != nil {
			return fmt.Errorf("end_devices[%d] (id=%q): managed_by %q: %w", i, e.ID, e.ManagedBy, err)
		}
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
	if s.SetGradW != nil {
		v := *s.SetGradW
		dc.SetGradW = &v
	}
	if s.SetSoftGradW != nil {
		v := *s.SetSoftGradW
		dc.SetSoftGradW = &v
	}
	return dc
}

func buildDERControl(s DERControlSpec) sep2.DERControl {
	dc := sep2.DERControl{}
	dc.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc/%s",
		s.EndDeviceID, s.FSAID, s.DERProgramID, s.ID)
	dc.MRID = s.MRID
	dc.Description = s.Description
	if s.EventStatus != nil {
		es := sep2.EventStatus{
			CurrentStatus:         s.EventStatus.CurrentStatus,
			DateTime:              s.EventStatus.DateTime,
			PotentiallySuperseded: s.EventStatus.PotentiallySuperseded,
			Reason:                s.EventStatus.Reason,
		}
		dc.EventStatus = &es
	}
	if s.Interval != nil {
		iv := sep2.DateTimeInterval{Start: s.Interval.Start, Duration: s.Interval.Duration}
		dc.Interval = &iv
	}
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
		base.OpModFixedW = &sep2.ActivePower{Multiplier: s.OpModFixedW.Multiplier, Value: s.OpModFixedW.Value}
	}
	if s.OpModFixedPFInjectW != nil {
		base.OpModFixedPFInjectW = &sep2.FixedPowerFactor{
			Displacement: s.OpModFixedPFInjectW.Displacement,
			Excitation:   s.OpModFixedPFInjectW.Excitation,
			Multiplier:   s.OpModFixedPFInjectW.Multiplier,
		}
	}
	if s.OpModMaxLimW != nil {
		base.OpModMaxLimW = &sep2.ActivePower{Multiplier: s.OpModMaxLimW.Multiplier, Value: s.OpModMaxLimW.Value}
	}
	if s.OpModTargetW != nil {
		base.OpModTargetW = &sep2.ActivePower{Multiplier: s.OpModTargetW.Multiplier, Value: s.OpModTargetW.Value}
	}
	if s.OpModVoltVar != nil {
		v := *s.OpModVoltVar
		base.OpModVoltVar = &v
	}
	if s.OpModVoltWatt != nil {
		v := *s.OpModVoltWatt
		base.OpModVoltWatt = &v
	}
	if s.OpModFreqDroop != nil {
		v := *s.OpModFreqDroop
		base.OpModFreqDroop = &v
	}
	if s.OpModFreqWatt != nil {
		v := *s.OpModFreqWatt
		base.OpModFreqWatt = &v
	}
	if s.OpModLVRTMustTrip != nil {
		v := *s.OpModLVRTMustTrip
		base.OpModLVRTMustTrip = &v
	}
	if s.OpModLVRTMomentaryCessation != nil {
		v := *s.OpModLVRTMomentaryCessation
		base.OpModLVRTMomentaryCessation = &v
	}
	if s.OpModHVRTMustTrip != nil {
		v := *s.OpModHVRTMustTrip
		base.OpModHVRTMustTrip = &v
	}
	if s.OpModHVRTMomentaryCessation != nil {
		v := *s.OpModHVRTMomentaryCessation
		base.OpModHVRTMomentaryCessation = &v
	}
	if s.OpModLFRTMustTrip != nil {
		v := *s.OpModLFRTMustTrip
		base.OpModLFRTMustTrip = &v
	}
	if s.OpModHFRTMustTrip != nil {
		v := *s.OpModHFRTMustTrip
		base.OpModHFRTMustTrip = &v
	}
	if s.RampTms != nil {
		v := *s.RampTms
		base.RampTms = &v
	}
	return base
}

func buildDERCurve(s DERCurveSpec) sep2.DERCurve {
	curve := sep2.DERCurve{
		MRID:        s.MRID,
		Description: s.Description,
		CurveType:   s.CurveType,
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
