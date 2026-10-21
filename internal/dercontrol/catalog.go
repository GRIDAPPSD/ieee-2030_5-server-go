package dercontrol

import (
	"fmt"
	"math"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// This file is the one place a DERControlBase element is named. Every other
// site that handles a control function walks catalog or legacyTypes.

// Function names are the IEEE 2030.5-2018 DERControlBase element names, so
// the catalog, the request body and the wire share one vocabulary.
const (
	FuncConnect        = "opModConnect"
	FuncEnergize       = "opModEnergize"
	FuncMaxLimW        = "opModMaxLimW"
	FuncFixedPFInjectW = "opModFixedPFInjectW"
	FuncTargetW        = "opModTargetW"
)

var (
	rowConnect = row(FuncConnect, "Connect (true) or disconnect (false) the DER.", booleanKind,
		func(b *sep2.DERControlBase) **bool { return &b.OpModConnect },
		func(v *DERControlBaseView) **bool { return &v.OpModConnect })
	rowEnergize = row(FuncEnergize, "Energize (true) or de-energize (false) the DER.", booleanKind,
		func(b *sep2.DERControlBase) **bool { return &b.OpModEnergize },
		func(v *DERControlBaseView) **bool { return &v.OpModEnergize })
	rowMaxLimW = row(FuncMaxLimW, "Active power ceiling in hundredths of a percent of setMaxW.",
		perCentKind(intRange{min: 0, max: 10000}),
		func(b *sep2.DERControlBase) **sep2.PerCent { return &b.OpModMaxLimW },
		func(v *DERControlBaseView) **uint16 { return &v.OpModMaxLimW }).
		worded(label{path: "maxLimW", code: "max_lim_w"})
	// The displacement is in thousandths, which is what the fixed multiplier
	// says on the wire.
	rowFixedPFInjectW = row(FuncFixedPFInjectW, "Fixed power factor while injecting active power; displacement is thousandths.",
		powerFactorKind(intRange{min: 1, max: 1000}, -3),
		func(b *sep2.DERControlBase) **sep2.FixedPowerFactor { return &b.OpModFixedPFInjectW },
		func(v *DERControlBaseView) **FixedPowerFactorView { return &v.OpModFixedPFInjectW }).
		worded(label{path: "powerFactor", code: "power_factor", fields: map[string]label{
			"displacement": {path: "powerFactor.displacement", code: "displacement"},
			"excitation":   {path: "powerFactor.excitation", code: "excitation"},
		}})
	// The multiplier is a PowerOfTenMultiplierType, within the range the
	// standard allows.
	rowTargetW = row(FuncTargetW, "Active power target, value x 10^multiplier watts, discharge positive.",
		activePowerKind(intRange{min: math.MinInt16, max: math.MaxInt16, typeName: "int16"}, intRange{min: -9, max: 9}),
		func(b *sep2.DERControlBase) **sep2.ActivePower { return &b.OpModTargetW },
		func(v *DERControlBaseView) **ActivePowerView { return &v.OpModTargetW }).
		worded(label{path: "targetW", code: "target_w", fields: map[string]label{
			"value":      {path: "targetW.value", code: "target_w_value"},
			"multiplier": {path: "targetW.multiplier", code: "target_w_multiplier"},
		}})
)

// catalog is every function a control can carry, in the order GET
// /api/der/control-functions lists them and the views report them.
var catalog = []function{rowConnect, rowEnergize, rowMaxLimW, rowFixedPFInjectW, rowTargetW}

// DERControlBaseView is the JSON form of the DERControlBase values the admin
// API creates. Its fields follow catalog order; TestBaseViewFollowsCatalog
// fails if a row has no field here.
type DERControlBaseView struct {
	OpModConnect        *bool                 `json:"opModConnect,omitempty"`
	OpModEnergize       *bool                 `json:"opModEnergize,omitempty"`
	OpModMaxLimW        *uint16               `json:"opModMaxLimW,omitempty"`
	OpModFixedPFInjectW *FixedPowerFactorView `json:"opModFixedPFInjectW,omitempty"`
	OpModTargetW        *ActivePowerView      `json:"opModTargetW,omitempty"`
}

// ControlType selects a legacy control shape. The set is closed: a control
// that needs another combination of functions carries a functions set.
type ControlType string

const (
	Connect        ControlType = "connect"
	Disconnect     ControlType = "disconnect"
	MaxLimW        ControlType = "maxLimW"
	FixedPFInjectW ControlType = "fixedPFInjectW"
	TargetW        ControlType = "targetW"
)

// LegacyValues are the top-level value members a legacy type takes, as a
// request body carries them.
type LegacyValues struct {
	MaxLimW     *int64            `json:"maxLimW"`
	PowerFactor *PowerFactorInput `json:"powerFactor"`
	TargetW     *ActivePowerInput `json:"targetW"`
}

var (
	legacyMaxLimW = legacy(rowMaxLimW,
		func(r CreateRequest) *int64 {
			if r.MaxLimW == nil {
				return nil
			}
			v := int64(*r.MaxLimW)
			return &v
		},
		func(v LegacyValues) *int64 { return v.MaxLimW },
		func(r *CreateRequest, v sep2.PerCent) {
			m := uint16(v)
			r.MaxLimW = &m
		})
	legacyPowerFactor = legacy(rowFixedPFInjectW,
		func(r CreateRequest) *PowerFactorInput {
			if r.PowerFactor == nil {
				return nil
			}
			d := int64(r.PowerFactor.Displacement)
			return &PowerFactorInput{Displacement: &d, Excitation: r.PowerFactor.Excitation}
		},
		func(v LegacyValues) *PowerFactorInput { return v.PowerFactor },
		func(r *CreateRequest, v sep2.FixedPowerFactor) {
			e := v.Excitation
			r.PowerFactor = &PowerFactorValue{Displacement: v.Displacement, Excitation: &e}
		})
	legacyTargetW = legacy(rowTargetW,
		func(r CreateRequest) *ActivePowerInput {
			if r.TargetW == nil {
				return nil
			}
			v, m := int64(r.TargetW.Value), int64(r.TargetW.Multiplier)
			return &ActivePowerInput{Value: &v, Multiplier: &m}
		},
		func(v LegacyValues) *ActivePowerInput { return v.TargetW },
		func(r *CreateRequest, v sep2.ActivePower) { r.TargetW = &v })
)

// legacyTypes is the closed set of legacy types and what each builds.
// Connect and disconnect set opModConnect and opModEnergize to one value.
var legacyTypes = []legacyType{
	{typ: Connect, fixed: []fixedValue{fixed(rowConnect, true), fixed(rowEnergize, true)}},
	{typ: Disconnect, fixed: []fixedValue{fixed(rowConnect, false), fixed(rowEnergize, false)}},
	{typ: MaxLimW, value: legacyMaxLimW},
	{typ: FixedPFInjectW, value: legacyPowerFactor},
	{typ: TargetW, value: legacyTargetW},
}

// Requests have always been refused in these orders when they carry more
// than one member they should not: beside a functions set, and beside a
// type that does not take the member.
var (
	legacyBesideFunctionsOrder = []*legacyValue{legacyMaxLimW, legacyPowerFactor, legacyTargetW}
	legacyNotTakenOrder        = []*legacyValue{legacyTargetW, legacyMaxLimW, legacyPowerFactor}
)

// booleanKind is a true or false element.
var booleanKind = kind[bool, bool, bool]{
	shape:    shape{kind: "boolean"},
	parse:    func(in bool) (bool, *fault) { return in, nil },
	check:    func(bool) RefusalCode { return "" },
	view:     func(v bool) any { return v },
	baseView: func(v bool) bool { return v },
}

// perCentKind is a PerCent element, hundredths of a percent, within r.
func perCentKind(r intRange) kind[int64, sep2.PerCent, uint16] {
	return kind[int64, sep2.PerCent, uint16]{
		shape: shape{kind: "integer", bounds: r},
		parse: func(in int64) (sep2.PerCent, *fault) {
			if !r.contains(in) {
				return 0, &fault{bounds: r}
			}
			return sep2.PerCent(in), nil
		},
		check:    func(v sep2.PerCent) RefusalCode { return r.check(int64(v)) },
		view:     func(v sep2.PerCent) any { return uint16(v) },
		baseView: func(v sep2.PerCent) uint16 { return uint16(v) },
		log:      func(v sep2.PerCent) []LogAttr { return []LogAttr{{"value", uint16(v)}} },
	}
}

// PowerFactorInput is a fixed power factor as a request carries it.
type PowerFactorInput struct {
	Displacement *int64 `json:"displacement"`
	Excitation   *bool  `json:"excitation"`
}

// FunctionPowerFactorView is a fixed power factor in the functions view, the
// shape a request takes.
type FunctionPowerFactorView struct {
	Displacement uint16 `json:"displacement"`
	Excitation   bool   `json:"excitation"`
}

// FixedPowerFactorView is a fixed power factor in the derControlBase view.
type FixedPowerFactorView struct {
	Displacement uint16 `json:"displacement"`
	Excitation   bool   `json:"excitation"`
	Multiplier   int8   `json:"multiplier"`
}

// powerFactorKind is a FixedPowerFactor element whose displacement is within d
// and whose power of ten is always multiplier. Excitation has no safe
// default, since its two values inject and absorb reactive power, so a
// request must give it.
func powerFactorKind(d intRange, multiplier int8) kind[PowerFactorInput, sep2.FixedPowerFactor, FixedPowerFactorView] {
	return kind[PowerFactorInput, sep2.FixedPowerFactor, FixedPowerFactorView]{
		shape: shape{kind: "object", fields: []field{
			{name: "displacement", shape: shape{kind: "integer", bounds: d}},
			{name: "excitation", shape: shape{kind: "boolean"}},
		}},
		parse: func(in PowerFactorInput) (sep2.FixedPowerFactor, *fault) {
			switch {
			case in.Displacement == nil:
				return sep2.FixedPowerFactor{}, &fault{field: "displacement", missing: true}
			case in.Excitation == nil:
				return sep2.FixedPowerFactor{}, &fault{field: "excitation", missing: true}
			case !d.contains(*in.Displacement):
				return sep2.FixedPowerFactor{}, &fault{field: "displacement", bounds: d}
			}
			return sep2.FixedPowerFactor{Displacement: uint16(*in.Displacement), Excitation: *in.Excitation, Multiplier: multiplier}, nil
		},
		check: func(v sep2.FixedPowerFactor) RefusalCode {
			if v.Multiplier != multiplier {
				return RefusalValueOutOfRange
			}
			return d.check(int64(v.Displacement))
		},
		view: func(v sep2.FixedPowerFactor) any {
			return FunctionPowerFactorView{Displacement: v.Displacement, Excitation: v.Excitation}
		},
		baseView: func(v sep2.FixedPowerFactor) FixedPowerFactorView {
			return FixedPowerFactorView{Displacement: v.Displacement, Excitation: v.Excitation, Multiplier: v.Multiplier}
		},
		log: func(v sep2.FixedPowerFactor) []LogAttr {
			return []LogAttr{{"value", v.Displacement}, {"excitation", v.Excitation}}
		},
	}
}

// ActivePowerInput is an active power as a request carries it. A missing
// multiplier is 0.
type ActivePowerInput struct {
	Value      *int64 `json:"value"`
	Multiplier *int64 `json:"multiplier"`
}

// ActivePowerView is an active power, value x 10^multiplier watts, discharge
// positive.
type ActivePowerView struct {
	Value      int16 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

// activePowerKind is an ActivePower element with its value within value and its
// multiplier within multiplier.
func activePowerKind(value, multiplier intRange) kind[ActivePowerInput, sep2.ActivePower, ActivePowerView] {
	return kind[ActivePowerInput, sep2.ActivePower, ActivePowerView]{
		shape: shape{kind: "object", fields: []field{
			{name: "value", shape: shape{kind: "integer", bounds: value}},
			{name: "multiplier", shape: shape{kind: "integer", bounds: multiplier}},
		}},
		parse: func(in ActivePowerInput) (sep2.ActivePower, *fault) {
			if in.Value == nil {
				return sep2.ActivePower{}, &fault{field: "value", missing: true}
			}
			if !value.contains(*in.Value) {
				return sep2.ActivePower{}, &fault{field: "value", bounds: value}
			}
			var m int64
			if in.Multiplier != nil {
				m = *in.Multiplier
			}
			if !multiplier.contains(m) {
				return sep2.ActivePower{}, &fault{field: "multiplier", bounds: multiplier}
			}
			return sep2.ActivePower{Value: int16(*in.Value), Multiplier: int8(m)}, nil
		},
		check: func(v sep2.ActivePower) RefusalCode {
			if code := value.check(int64(v.Value)); code != "" {
				return code
			}
			return multiplier.check(int64(v.Multiplier))
		},
		view: func(v sep2.ActivePower) any { return ActivePowerView{Value: v.Value, Multiplier: v.Multiplier} },
		baseView: func(v sep2.ActivePower) ActivePowerView {
			return ActivePowerView{Value: v.Value, Multiplier: v.Multiplier}
		},
		log: func(v sep2.ActivePower) []LogAttr {
			return []LogAttr{{"value", v.Value}, {"multiplier", v.Multiplier}}
		},
	}
}

// intRange is an integer's inclusive bounds. typeName, when set, is how the
// catalog names a range that is the whole of a Go integer type.
type intRange struct {
	min, max int64
	typeName string
}

func (r intRange) contains(v int64) bool { return v >= r.min && v <= r.max }

func (r intRange) check(v int64) RefusalCode {
	if !r.contains(v) {
		return RefusalValueOutOfRange
	}
	return ""
}

func (r intRange) String() string { return fmt.Sprintf("%d to %d", r.min, r.max) }

// shape is a value kind's structure and bounds: the catalog's value text is
// formatted from it.
type shape struct {
	kind   string // "boolean", "integer" or "object"
	bounds intRange
	fields []field
}

// field is one member of an object value; name is its request key.
type field struct {
	name  string
	shape shape
}

// text is the catalog's value text: "boolean", "integer <min> to <max>", or
// "object {name shape, ...}".
func (s shape) text() string {
	switch s.kind {
	case "integer":
		return "integer " + s.bounds.String()
	case "object":
		parts := make([]string, len(s.fields))
		for i, f := range s.fields {
			parts[i] = f.name + " " + f.shape.memberText()
		}
		return "object {" + strings.Join(parts, ", ") + "}"
	}
	return s.kind
}

func (s shape) memberText() string {
	if s.kind != "integer" {
		return s.kind
	}
	if s.bounds.typeName != "" {
		return s.bounds.typeName
	}
	return s.bounds.String()
}
