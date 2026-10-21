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
	FuncFixedPFAbsorbW = "opModFixedPFAbsorbW"
	FuncFixedVar       = "opModFixedVar"
	FuncFixedW         = "opModFixedW"
	FuncFreqDroop      = "opModFreqDroop"
	FuncTargetVar      = "opModTargetVar"
	FuncRampTms        = "rampTms"
)

var (
	rowConnect = row(FuncConnect, "Connect (true) or disconnect (false) the DER.", booleanKind,
		func(b *sep2.DERControlBase) **bool { return &b.OpModConnect },
		func(s *FunctionSet) **bool { return &s.OpModConnect },
		func(v *DERControlBaseView) **bool { return &v.OpModConnect })
	rowEnergize = row(FuncEnergize, "Energize (true) or de-energize (false) the DER.", booleanKind,
		func(b *sep2.DERControlBase) **bool { return &b.OpModEnergize },
		func(s *FunctionSet) **bool { return &s.OpModEnergize },
		func(v *DERControlBaseView) **bool { return &v.OpModEnergize })
	rowMaxLimW = row(FuncMaxLimW, "Active power ceiling in hundredths of a percent of setMaxW.",
		integerKind[sep2.PerCent, uint16](perCent),
		func(b *sep2.DERControlBase) **sep2.PerCent { return &b.OpModMaxLimW },
		func(s *FunctionSet) **int64 { return &s.OpModMaxLimW },
		func(v *DERControlBaseView) **uint16 { return &v.OpModMaxLimW }).
		worded(label{path: "maxLimW", code: "max_lim_w"})
	rowFixedPFInjectW = row(FuncFixedPFInjectW, "Fixed power factor while injecting active power; displacement is thousandths.",
		thousandthsPF,
		func(b *sep2.DERControlBase) **sep2.FixedPowerFactor { return &b.OpModFixedPFInjectW },
		func(s *FunctionSet) **PowerFactorInput { return &s.OpModFixedPFInjectW },
		func(v *DERControlBaseView) **FixedPowerFactorView { return &v.OpModFixedPFInjectW }).
		worded(label{path: "powerFactor", code: "power_factor", fields: map[string]label{
			"displacement": {path: "powerFactor.displacement", code: "displacement"},
			"excitation":   {path: "powerFactor.excitation", code: "excitation"},
		}})
	// The multiplier is a PowerOfTenMultiplierType, within the range the
	// standard allows.
	rowTargetW = row(FuncTargetW, "Active power target, value x 10^multiplier watts, discharge positive.",
		powerKind[sep2.ActivePower](int16Range, powerOfTen),
		func(b *sep2.DERControlBase) **sep2.ActivePower { return &b.OpModTargetW },
		func(s *FunctionSet) **ActivePowerInput { return &s.OpModTargetW },
		func(v *DERControlBaseView) **ActivePowerView { return &v.OpModTargetW }).
		worded(label{path: "targetW", code: "target_w", fields: map[string]label{
			"value":      {path: "targetW.value", code: "target_w_value"},
			"multiplier": {path: "targetW.multiplier", code: "target_w_multiplier"},
		}})
	// Rows added after the first five follow them, so the catalog list, the
	// views and the audit value of every existing control keep their order.
	rowFixedPFAbsorbW = row(FuncFixedPFAbsorbW, "Fixed power factor while absorbing active power; displacement is thousandths.",
		thousandthsPF,
		func(b *sep2.DERControlBase) **sep2.FixedPowerFactor { return &b.OpModFixedPFAbsorbW },
		func(s *FunctionSet) **PowerFactorInput { return &s.OpModFixedPFAbsorbW },
		func(v *DERControlBaseView) **FixedPowerFactorView { return &v.OpModFixedPFAbsorbW })
	rowFixedVar = row(FuncFixedVar, "Reactive power setpoint in hundredths of a percent of what refType names: 1 setMaxW, 2 setMaxVar, 3 statVarAvail.",
		fixedVarKind(intRange{min: 1, max: 3}, signedPerCent),
		func(b *sep2.DERControlBase) **sep2.FixedVar { return &b.OpModFixedVar },
		func(s *FunctionSet) **FixedVarInput { return &s.OpModFixedVar },
		func(v *DERControlBaseView) **FixedVarView { return &v.OpModFixedVar })
	rowFixedW = row(FuncFixedW, "Active power setpoint in hundredths of a percent, discharge positive.",
		integerKind[sep2.SignedPerCent, int16](signedPerCent),
		func(b *sep2.DERControlBase) **sep2.SignedPerCent { return &b.OpModFixedW },
		func(s *FunctionSet) **int64 { return &s.OpModFixedW },
		func(v *DERControlBaseView) **int16 { return &v.OpModFixedW })
	rowFreqDroop = row(FuncFreqDroop, "Frequency droop: dead bands dBOF and dBUF in thousandths of a hertz, droops kOF and kUF in thousandths (unitless), open loop response time openLoopTms in hundredths of a second, 0 for no limit.",
		freqDroopKind(intRange{min: 0, max: math.MaxUint32, typeName: "uint32"}, uint16Range),
		func(b *sep2.DERControlBase) **sep2.FreqDroopType { return &b.OpModFreqDroop },
		func(s *FunctionSet) **FreqDroopInput { return &s.OpModFreqDroop },
		func(v *DERControlBaseView) **FreqDroopView { return &v.OpModFreqDroop })
	rowTargetVar = row(FuncTargetVar, "Reactive power target, value x 10^multiplier var.",
		powerKind[sep2.ReactivePower](int16Range, powerOfTen),
		func(b *sep2.DERControlBase) **sep2.ReactivePower { return &b.OpModTargetVar },
		func(s *FunctionSet) **ActivePowerInput { return &s.OpModTargetVar },
		func(v *DERControlBaseView) **ActivePowerView { return &v.OpModTargetVar })
	rowRampTms = row(FuncRampTms, "Ramp time to the control's settings, in hundredths of a second.",
		integerKind[uint16, uint16](uint16Range),
		func(b *sep2.DERControlBase) **uint16 { return &b.RampTms },
		func(s *FunctionSet) **int64 { return &s.RampTms },
		func(v *DERControlBaseView) **uint16 { return &v.RampTms })
)

// Bounds shared by more than one row. PerCent and SignedPerCent copy core's,
// which core enforces when it encodes; TestServedIntegerBoundsEncode fails
// if one here is wider. PowerOfTenMultiplierType is the range the standard
// allows.
var (
	perCent       = intRange{min: 0, max: 10000}
	signedPerCent = intRange{min: -10000, max: 10000}
	powerOfTen    = intRange{min: -9, max: 9}
	int16Range    = intRange{min: math.MinInt16, max: math.MaxInt16, typeName: "int16"}
	uint16Range   = intRange{min: 0, max: math.MaxUint16, typeName: "uint16"}
	// The displacement is in thousandths, which is what the fixed multiplier
	// says on the wire.
	thousandthsPF = powerFactorKind(intRange{min: 1, max: 1000}, -3)
)

// catalog is every function a control can carry, in the order GET
// /api/der/control-functions lists them and the views report them.
var catalog = []function{rowConnect, rowEnergize, rowMaxLimW, rowFixedPFInjectW, rowTargetW,
	rowFixedPFAbsorbW, rowFixedVar, rowFixedW, rowFreqDroop, rowTargetVar, rowRampTms}

// FunctionSet is the set of functions one control carries, each in the
// shape a request gives it, so a member left out is refused as missing and
// never read as zero. Its fields follow catalog order;
// TestStructsFollowCatalog fails if a row has no field here.
type FunctionSet struct {
	OpModConnect        *bool             `json:"opModConnect,omitempty"`
	OpModEnergize       *bool             `json:"opModEnergize,omitempty"`
	OpModMaxLimW        *int64            `json:"opModMaxLimW,omitempty"`
	OpModFixedPFInjectW *PowerFactorInput `json:"opModFixedPFInjectW,omitempty"`
	OpModTargetW        *ActivePowerInput `json:"opModTargetW,omitempty"`
	OpModFixedPFAbsorbW *PowerFactorInput `json:"opModFixedPFAbsorbW,omitempty"`
	OpModFixedVar       *FixedVarInput    `json:"opModFixedVar,omitempty"`
	OpModFixedW         *int64            `json:"opModFixedW,omitempty"`
	OpModFreqDroop      *FreqDroopInput   `json:"opModFreqDroop,omitempty"`
	OpModTargetVar      *ActivePowerInput `json:"opModTargetVar,omitempty"`
	RampTms             *int64            `json:"rampTms,omitempty"`
}

// DERControlBaseView is the JSON form of the DERControlBase values the admin
// API creates. Its fields follow catalog order; TestStructsFollowCatalog
// fails if a row has no field here.
type DERControlBaseView struct {
	OpModConnect        *bool                 `json:"opModConnect,omitempty"`
	OpModEnergize       *bool                 `json:"opModEnergize,omitempty"`
	OpModMaxLimW        *uint16               `json:"opModMaxLimW,omitempty"`
	OpModFixedPFInjectW *FixedPowerFactorView `json:"opModFixedPFInjectW,omitempty"`
	OpModTargetW        *ActivePowerView      `json:"opModTargetW,omitempty"`
	OpModFixedPFAbsorbW *FixedPowerFactorView `json:"opModFixedPFAbsorbW,omitempty"`
	OpModFixedVar       *FixedVarView         `json:"opModFixedVar,omitempty"`
	OpModFixedW         *int16                `json:"opModFixedW,omitempty"`
	OpModFreqDroop      *FreqDroopView        `json:"opModFreqDroop,omitempty"`
	OpModTargetVar      *ActivePowerView      `json:"opModTargetVar,omitempty"`
	RampTms             *uint16               `json:"rampTms,omitempty"`
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
	shape:    shape{kind: kindBoolean},
	parse:    func(in bool) (bool, *fault) { return in, nil },
	input:    func(v bool) bool { return v },
	baseView: func(v bool) bool { return v },
}

// wireInt is the core integer types an integer element or its view takes.
type wireInt interface {
	~uint16 | ~int16
}

// integerKind is an integer element of core type T, within r, that the
// derControlBase view reports as V.
func integerKind[T, V wireInt](r intRange) kind[int64, T, V] {
	return kind[int64, T, V]{
		shape: shape{kind: kindInteger, bounds: r},
		parse: func(in int64) (T, *fault) {
			if !r.contains(in) {
				return 0, &fault{bounds: r}
			}
			return T(in), nil
		},
		input:    func(v T) int64 { return int64(v) },
		baseView: func(v T) V { return V(v) },
		log:      func(v T) []LogAttr { return []LogAttr{{"value", V(v)}} },
	}
}

// PowerFactorInput is a fixed power factor as a request carries it, and as
// the functions view reports it. It has no multiplier: the kind fixes it.
type PowerFactorInput struct {
	Displacement *int64 `json:"displacement"`
	Excitation   *bool  `json:"excitation"`
}

// FixedPowerFactorView is a fixed power factor in the derControlBase view,
// which reports the element as the wire carries it, multiplier included.
type FixedPowerFactorView struct {
	Displacement uint16 `json:"displacement"`
	Excitation   bool   `json:"excitation"`
	Multiplier   int8   `json:"multiplier"`
}

// powerFactorKind is a FixedPowerFactor element whose displacement is within
// d and whose power of ten is always multiplier. Excitation has no safe
// default, since its two values inject and absorb reactive power, so it is
// required.
func powerFactorKind(d intRange, multiplier int8) kind[PowerFactorInput, sep2.FixedPowerFactor, FixedPowerFactorView] {
	return kind[PowerFactorInput, sep2.FixedPowerFactor, FixedPowerFactorView]{
		shape: shape{kind: kindObject,
			fields: []field{
				{name: "displacement", required: true, shape: shape{kind: kindInteger, bounds: d}},
				{name: "excitation", required: true, shape: shape{kind: kindBoolean}},
			},
			fixed: []constant{{name: "multiplier", value: int64(multiplier)}},
		},
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
		input: func(v sep2.FixedPowerFactor) PowerFactorInput {
			d, e := int64(v.Displacement), v.Excitation
			return PowerFactorInput{Displacement: &d, Excitation: &e}
		},
		baseView: func(v sep2.FixedPowerFactor) FixedPowerFactorView {
			return FixedPowerFactorView{Displacement: v.Displacement, Excitation: v.Excitation, Multiplier: v.Multiplier}
		},
		log: func(v sep2.FixedPowerFactor) []LogAttr {
			return []LogAttr{{"value", v.Displacement}, {"excitation", v.Excitation}}
		},
	}
}

// ActivePowerInput is an active or reactive power as a request carries it,
// and as the functions view reports it. A missing multiplier is 0.
type ActivePowerInput struct {
	Value      *int64 `json:"value"`
	Multiplier *int64 `json:"multiplier"`
}

// ActivePowerView is an active or reactive power in the derControlBase view,
// value x 10^multiplier watts or var.
type ActivePowerView struct {
	Value      int16 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

// powerParts is the shape core gives ActivePower and ReactivePower, so one
// kind serves both.
type powerParts = struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int16 `xml:"value"`
}

type power interface{ ~powerParts }

// powerKind is an ActivePower or ReactivePower element with its value within
// value and its multiplier within multiplier.
func powerKind[T power](value, multiplier intRange) kind[ActivePowerInput, T, ActivePowerView] {
	return kind[ActivePowerInput, T, ActivePowerView]{
		shape: shape{kind: kindObject, fields: []field{
			{name: "value", required: true, shape: shape{kind: kindInteger, bounds: value}},
			{name: "multiplier", shape: shape{kind: kindInteger, bounds: multiplier}},
		}},
		parse: func(in ActivePowerInput) (T, *fault) {
			if in.Value == nil {
				return T{}, &fault{field: "value", missing: true}
			}
			if !value.contains(*in.Value) {
				return T{}, &fault{field: "value", bounds: value}
			}
			var m int64
			if in.Multiplier != nil {
				m = *in.Multiplier
			}
			if !multiplier.contains(m) {
				return T{}, &fault{field: "multiplier", bounds: multiplier}
			}
			return T(powerParts{Value: int16(*in.Value), Multiplier: int8(m)}), nil
		},
		input: func(v T) ActivePowerInput {
			p := powerParts(v)
			x, m := int64(p.Value), int64(p.Multiplier)
			return ActivePowerInput{Value: &x, Multiplier: &m}
		},
		baseView: func(v T) ActivePowerView {
			p := powerParts(v)
			return ActivePowerView{Value: p.Value, Multiplier: p.Multiplier}
		},
		log: func(v T) []LogAttr {
			p := powerParts(v)
			return []LogAttr{{"value", p.Value}, {"multiplier", p.Multiplier}}
		},
	}
}

// FixedVarInput is a FixedVar as a request carries it, and as the functions
// view reports it.
type FixedVarInput struct {
	RefType *int64 `json:"refType"`
	Value   *int64 `json:"value"`
}

// FixedVarView is a FixedVar in the derControlBase view.
type FixedVarView struct {
	RefType uint8 `json:"refType"`
	Value   int16 `json:"value"`
}

// fixedVarKind is a FixedVar element. refTypes is the DERUnitRefType values
// opModFixedVar allows, which core also enforces when it encodes one.
func fixedVarKind(refTypes, value intRange) kind[FixedVarInput, sep2.FixedVar, FixedVarView] {
	s := shape{kind: kindObject, fields: []field{
		{name: "refType", required: true, shape: shape{kind: kindInteger, bounds: refTypes}},
		{name: "value", required: true, shape: shape{kind: kindInteger, bounds: value}},
	}}
	return kind[FixedVarInput, sep2.FixedVar, FixedVarView]{
		shape: s,
		parse: func(in FixedVarInput) (sep2.FixedVar, *fault) {
			if f := s.checkIntegers(in.RefType, in.Value); f != nil {
				return sep2.FixedVar{}, f
			}
			return sep2.FixedVar{RefType: uint8(*in.RefType), Value: sep2.SignedPerCent(*in.Value)}, nil
		},
		input: func(v sep2.FixedVar) FixedVarInput {
			r, x := int64(v.RefType), int64(v.Value)
			return FixedVarInput{RefType: &r, Value: &x}
		},
		baseView: func(v sep2.FixedVar) FixedVarView {
			return FixedVarView{RefType: v.RefType, Value: int16(v.Value)}
		},
		log: func(v sep2.FixedVar) []LogAttr {
			return []LogAttr{{"refType", v.RefType}, {"value", int16(v.Value)}}
		},
	}
}

// FreqDroopInput is a FreqDroopType as a request carries it, and as the
// functions view reports it.
type FreqDroopInput struct {
	DBOF        *int64 `json:"dBOF"`
	DBUF        *int64 `json:"dBUF"`
	KOF         *int64 `json:"kOF"`
	KUF         *int64 `json:"kUF"`
	OpenLoopTms *int64 `json:"openLoopTms"`
}

// FreqDroopView is a FreqDroopType in the derControlBase view.
type FreqDroopView struct {
	DBOF        uint32 `json:"dBOF"`
	DBUF        uint32 `json:"dBUF"`
	KOF         uint16 `json:"kOF"`
	KUF         uint16 `json:"kUF"`
	OpenLoopTms uint16 `json:"openLoopTms"`
}

// freqDroopKind is a FreqDroopType element: its dead bands within band and
// its droops and response time within small, the integer types the schema
// gives them.
func freqDroopKind(band, small intRange) kind[FreqDroopInput, sep2.FreqDroopType, FreqDroopView] {
	s := shape{kind: kindObject, fields: []field{
		{name: "dBOF", required: true, shape: shape{kind: kindInteger, bounds: band}},
		{name: "dBUF", required: true, shape: shape{kind: kindInteger, bounds: band}},
		{name: "kOF", required: true, shape: shape{kind: kindInteger, bounds: small}},
		{name: "kUF", required: true, shape: shape{kind: kindInteger, bounds: small}},
		{name: "openLoopTms", required: true, shape: shape{kind: kindInteger, bounds: small}},
	}}
	return kind[FreqDroopInput, sep2.FreqDroopType, FreqDroopView]{
		shape: s,
		parse: func(in FreqDroopInput) (sep2.FreqDroopType, *fault) {
			if f := s.checkIntegers(in.DBOF, in.DBUF, in.KOF, in.KUF, in.OpenLoopTms); f != nil {
				return sep2.FreqDroopType{}, f
			}
			return sep2.FreqDroopType{DBOF: uint32(*in.DBOF), DBUF: uint32(*in.DBUF), KOF: uint16(*in.KOF), KUF: uint16(*in.KUF), OpenLoopTms: uint16(*in.OpenLoopTms)}, nil
		},
		input: func(v sep2.FreqDroopType) FreqDroopInput {
			a, b, c, d, e := int64(v.DBOF), int64(v.DBUF), int64(v.KOF), int64(v.KUF), int64(v.OpenLoopTms)
			return FreqDroopInput{DBOF: &a, DBUF: &b, KOF: &c, KUF: &d, OpenLoopTms: &e}
		},
		baseView: func(v sep2.FreqDroopType) FreqDroopView {
			return FreqDroopView(v)
		},
		log: func(v sep2.FreqDroopType) []LogAttr {
			return []LogAttr{{"dBOF", v.DBOF}, {"dBUF", v.DBUF}, {"kOF", v.KOF}, {"kUF", v.KUF}, {"openLoopTms", v.OpenLoopTms}}
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

func (r intRange) String() string { return fmt.Sprintf("%d to %d", r.min, r.max) }

// valueKind is the kind of a value or of an object member. The zero value is
// no kind; TestShapesHaveKinds refuses a row or member that has it.
type valueKind uint8

const (
	kindBoolean valueKind = iota + 1
	kindInteger
	kindObject
	kindCurve
	// kindCount ends the range, so a test can visit every kind.
	kindCount
)

func (k valueKind) String() string {
	switch k {
	case kindBoolean:
		return "boolean"
	case kindInteger:
		return "integer"
	case kindObject:
		return "object"
	case kindCurve:
		return "curve"
	}
	return ""
}

// noun is the kind as a wrong-type refusal names it.
func (k valueKind) noun() string {
	switch k {
	case kindBoolean:
		return "a boolean"
	case kindInteger:
		return "an integer"
	case kindObject:
		return "an object"
	case kindCurve:
		return "a curve id"
	}
	return ""
}

// shape describes a value kind: its kind, an integer's bounds, an object's
// members in request order, the members an object always carries on the
// wire whatever the request says, and a curve's DERCurveType. The catalog's
// value text and descriptor are both read from it.
type shape struct {
	kind      valueKind
	bounds    intRange
	fields    []field
	fixed     []constant
	curveType uint8
}

// field is one member of an object value; name is its request key, and a
// required member a request leaves out is refused.
type field struct {
	name     string
	required bool
	shape    shape
}

// constant is a member an object always carries with one value.
type constant struct {
	name  string
	value int64
}

// text is the catalog's value text: "boolean", "integer <min> to <max>", or
// "object {name shape, ...}".
func (s shape) text() string {
	switch s.kind {
	case kindInteger:
		return "integer " + s.bounds.String()
	case kindObject:
		parts := make([]string, len(s.fields))
		for i, f := range s.fields {
			parts[i] = f.name + " " + f.shape.memberText()
		}
		return "object {" + strings.Join(parts, ", ") + "}"
	}
	return s.kind.String()
}

func (s shape) memberText() string {
	if s.kind != kindInteger {
		return s.kind.String()
	}
	if s.bounds.typeName != "" {
		return s.bounds.typeName
	}
	return s.bounds.String()
}

// checkIntegers checks an object whose members are all integers, given in
// field order: a required member left out first, then one out of bounds.
func (s shape) checkIntegers(values ...*int64) *fault {
	for i, f := range s.fields {
		if values[i] == nil && f.required {
			return &fault{field: f.name, missing: true}
		}
	}
	for i, f := range s.fields {
		if values[i] != nil && !f.shape.bounds.contains(*values[i]) {
			return &fault{field: f.name, bounds: f.shape.bounds}
		}
	}
	return nil
}

// describe is the descriptor the catalog serves for s: the same kind and
// bounds that parse enforces and text prints.
func (s shape) describe() Descriptor {
	d := Descriptor{Kind: s.kind.String()}
	switch s.kind {
	case kindInteger:
		d.Min, d.Max = &s.bounds.min, &s.bounds.max
	case kindObject:
		d.Fields = make([]FieldInfo, len(s.fields))
		for i, f := range s.fields {
			m := f.shape.describe()
			d.Fields[i] = FieldInfo{Name: f.name, Kind: m.Kind, Required: f.required, Min: m.Min, Max: m.Max}
		}
	case kindCurve:
		d.CurveType = &s.curveType
	}
	return d
}
