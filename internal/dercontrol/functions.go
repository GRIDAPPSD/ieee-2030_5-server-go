package dercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// FunctionInfo describes one catalog function for GET
// /api/der/control-functions. Value names the request value's shape in prose
// and the embedded Descriptor names it for a program.
type FunctionInfo struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Descriptor
}

// Descriptor is a value kind as the catalog serves it: an integer carries
// Min and Max, an object its Fields in request order, a curve its CurveType.
type Descriptor struct {
	Kind      string      `json:"kind"`
	Min       *int64      `json:"min,omitempty"`
	Max       *int64      `json:"max,omitempty"`
	Fields    []FieldInfo `json:"fields,omitempty"`
	CurveType *uint8      `json:"curveType,omitempty"`
}

// FieldInfo is one member of an object value, named as a request names it.
// Required marks a member a request may not leave out.
type FieldInfo struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Required bool   `json:"required,omitempty"`
	Min      *int64 `json:"min,omitempty"`
	Max      *int64 `json:"max,omitempty"`
}

// ValueRefusal is a refused request value as wire text: Code is the stable
// identifier and Message the fixed public message. Neither carries request
// text.
type ValueRefusal struct {
	Code, Message string
}

// LogAttr is one audit log attribute of a control's value.
type LogAttr struct {
	Key   string
	Value any
}

// kind is one value shape: I is how a request carries it, T the core type
// of its element and V its derControlBase view. Its bounds live in shape and
// in its closures' captured ranges, built from the same intRange. Every
// value, from a request body or a Go caller, is checked by parse.
type kind[I, T, V any] struct {
	shape    shape
	parse    func(in I) (T, *fault)
	input    func(v T) I // the value as a request gives it: the functions view
	baseView func(v T) V
	log      func(v T) []LogAttr // nil logs nothing
	// stored checks a parsed value against what it names in a store,
	// answering a refusal worded by l, or an error when the store cannot be
	// read. It is nil for a kind whose value names nothing stored.
	stored func(ctx context.Context, curves CurveReader, v T, l label) (*ValueRefusal, error)
}

// fault is why parse refused a value: a missing member, or one outside
// bounds. field names the object member, or is empty for the whole value.
type fault struct {
	field   string
	missing bool
	bounds  intRange
}

// refusal is the issuer's refusal for f, carrying l's wording as Detail.
func (f *fault) refusal(l label) *RefusalError {
	code := RefusalValueOutOfRange
	if f.missing {
		code = RefusalMissingValue
	}
	return &RefusalError{Code: code, Detail: l.refusal(f)}
}

// label is the wording of a row's value refusals: path prefixes the message
// and code is the refusal code's stem. fields overrides an object member's
// wording; a member without one extends path and keeps code.
type label struct {
	path, code string
	fields     map[string]label
}

func (l label) member(name string) label {
	if m, ok := l.fields[name]; ok {
		return m
	}
	return label{path: l.path + "." + name, code: l.code}
}

func (l label) refusal(f *fault) *ValueRefusal {
	if f.field != "" {
		l = l.member(f.field)
	}
	if f.missing {
		return &ValueRefusal{l.code + "_required", l.path + ": required"}
	}
	return &ValueRefusal{l.code + "_range", l.path + ": must be " + f.bounds.String()}
}

// wrongType is the refusal for a member at l whose JSON type is not k.
func (l label) wrongType(k valueKind) ValueRefusal {
	return ValueRefusal{l.code + "_type", l.path + ": must be " + k.noun()}
}

// function is a catalog row with its value types erased.
type function interface {
	info() FunctionInfo
	shapeOf() shape
	present(b *sep2.DERControlBase) bool
	inSet(s *FunctionSet) bool
	decode(ctx context.Context, curves CurveReader, raw json.RawMessage, dst *FunctionSet) (*ValueRefusal, error)
	build(src *FunctionSet, dst *sep2.DERControlBase) error
	value(b *sep2.DERControlBase) any
	setView(b *sep2.DERControlBase, v *DERControlBaseView)
	logAttrs(b *sep2.DERControlBase) []LogAttr
}

type typedRow[I, T, V any] struct {
	name, description string
	kind              kind[I, T, V]
	label             label
	elem              func(*sep2.DERControlBase) **T
	member            func(*FunctionSet) **I
	view              func(*DERControlBaseView) **V
}

// row is one catalog function. Its refusals are worded "functions.<name>"
// with code stem "function_value" unless worded says otherwise.
func row[I, T, V any](name, description string, k kind[I, T, V], elem func(*sep2.DERControlBase) **T, member func(*FunctionSet) **I, view func(*DERControlBaseView) **V) *typedRow[I, T, V] {
	return &typedRow[I, T, V]{name: name, description: description, kind: k, elem: elem, member: member, view: view,
		label: label{path: "functions." + name, code: "function_value"}}
}

// worded gives a row the wording its refusals had before function sets
// existed, which the wire keeps.
func (r *typedRow[I, T, V]) worded(l label) *typedRow[I, T, V] {
	r.label = l
	return r
}

func (r *typedRow[I, T, V]) info() FunctionInfo {
	return FunctionInfo{Name: r.name, Value: r.kind.shape.text(), Description: r.description, Descriptor: r.kind.shape.describe()}
}

func (r *typedRow[I, T, V]) present(b *sep2.DERControlBase) bool { return *r.elem(b) != nil }

func (r *typedRow[I, T, V]) shapeOf() shape { return r.kind.shape }

func (r *typedRow[I, T, V]) inSet(s *FunctionSet) bool { return *r.member(s) != nil }

// decode reads a request's value into dst, refusing it in the row's wording
// before the issuer sees it. A value naming a stored resource is checked
// against curves here, since the issuer reads no curve store.
func (r *typedRow[I, T, V]) decode(ctx context.Context, curves CurveReader, raw json.RawMessage, dst *FunctionSet) (*ValueRefusal, error) {
	var in *I
	if !decodeStrict(raw, &in) || in == nil {
		return &ValueRefusal{"function_value_type", "functions." + r.name + ": wrong value type"}, nil
	}
	v, f := r.kind.parse(*in)
	if f != nil {
		return r.label.refusal(f), nil
	}
	if r.kind.stored != nil {
		if ref, err := r.kind.stored(ctx, curves, v, r.label); ref != nil || err != nil {
			return ref, err
		}
	}
	*r.member(dst) = in
	return nil, nil
}

func (r *typedRow[I, T, V]) build(src *FunctionSet, dst *sep2.DERControlBase) error {
	v, f := r.kind.parse(**r.member(src))
	if f != nil {
		return f.refusal(r.label)
	}
	*r.elem(dst) = &v
	return nil
}

func (r *typedRow[I, T, V]) value(b *sep2.DERControlBase) any { return r.kind.input(**r.elem(b)) }

func (r *typedRow[I, T, V]) setView(b *sep2.DERControlBase, v *DERControlBaseView) {
	x := r.kind.baseView(**r.elem(b))
	*r.view(v) = &x
}

func (r *typedRow[I, T, V]) logAttrs(b *sep2.DERControlBase) []LogAttr {
	if r.kind.log == nil {
		return nil
	}
	return r.kind.log(**r.elem(b))
}

// decodeStrict decodes one JSON value into dst, refusing unknown object keys
// and trailing data.
func decodeStrict(raw json.RawMessage, dst any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// legacyValue is a top-level value member a legacy type takes, read from a
// CreateRequest or from a request body.
type legacyValue struct {
	row       function
	label     label
	shape     shape
	inRequest func(CreateRequest) bool
	inBody    func(LegacyValues) bool
	// build sets the request's value on dst, or returns why it cannot.
	build func(req CreateRequest, dst *sep2.DERControlBase) error
	// take parses the body's value into req, worded as the row's refusals.
	take func(v LegacyValues, req *CreateRequest) ([]LogAttr, *ValueRefusal)
}

// legacy ties a legacy value member to the row it fills: fromRequest and
// fromBody read the member as the row's input, put stores a parsed value
// back on a CreateRequest.
func legacy[I, T, V any](r *typedRow[I, T, V], fromRequest func(CreateRequest) *I, fromBody func(LegacyValues) *I, put func(*CreateRequest, T)) *legacyValue {
	return &legacyValue{
		row:       r,
		label:     r.label,
		shape:     r.kind.shape,
		inRequest: func(req CreateRequest) bool { return fromRequest(req) != nil },
		inBody:    func(v LegacyValues) bool { return fromBody(v) != nil },
		build: func(req CreateRequest, dst *sep2.DERControlBase) error {
			v, f := r.kind.parse(*fromRequest(req))
			if f != nil {
				return f.refusal(r.label)
			}
			*r.elem(dst) = &v
			return nil
		},
		take: func(body LegacyValues, req *CreateRequest) ([]LogAttr, *ValueRefusal) {
			v, f := r.kind.parse(*fromBody(body))
			if f != nil {
				return nil, r.label.refusal(f)
			}
			put(req, v)
			if r.kind.log == nil {
				return nil, nil
			}
			return r.kind.log(v), nil
		},
	}
}

// fixedValue is an element a valueless legacy type sets to one value.
type fixedValue struct {
	set func(*sep2.DERControlBase)
	is  func(*sep2.DERControlBase) bool
}

func fixed[I, T comparable, V any](r *typedRow[I, T, V], v T) fixedValue {
	return fixedValue{
		set: func(b *sep2.DERControlBase) {
			x := v
			*r.elem(b) = &x
		},
		is: func(b *sep2.DERControlBase) bool {
			p := *r.elem(b)
			return p != nil && *p == v
		},
	}
}

// legacyType is one closed legacy type: the elements it fixes, or the one
// value member it takes.
type legacyType struct {
	typ   ControlType
	fixed []fixedValue
	value *legacyValue
}

// builds reports whether b is exactly what t builds, for some value of its
// member. b carries n catalog functions.
func (t legacyType) builds(b *sep2.DERControlBase, n int) bool {
	if t.value != nil {
		return n == 1 && t.value.row.present(b)
	}
	if n != len(t.fixed) {
		return false
	}
	for _, f := range t.fixed {
		if !f.is(b) {
			return false
		}
	}
	return true
}

func legacyTypeOf(typ ControlType) (legacyType, bool) {
	for _, t := range legacyTypes {
		if t.typ == typ {
			return t, true
		}
	}
	return legacyType{}, false
}

// Catalog returns the functions a control can carry, in a stable order.
func Catalog() []FunctionInfo {
	out := make([]FunctionInfo, len(catalog))
	for i, f := range catalog {
		out[i] = f.info()
	}
	return out
}

// IsFunction reports whether name is a catalog function.
func IsFunction(name string) bool {
	for _, f := range catalog {
		if f.info().Name == name {
			return true
		}
	}
	return false
}

// FunctionNames lists the catalog functions b carries, in catalog order.
func FunctionNames(b *sep2.DERControlBase) []string {
	var names []string
	if b == nil {
		return names
	}
	for _, f := range catalog {
		if f.present(b) {
			names = append(names, f.info().Name)
		}
	}
	return names
}

// functionSetKey reduces b to its function set for the supersede scan's
// equality test: equal keys mean equal sets (2018 rule t). A base carrying
// an element outside the catalog keys as "other".
func functionSetKey(b *sep2.DERControlBase) string {
	if b == nil {
		return ""
	}
	if names := FunctionNames(b); len(names) > 0 {
		return strings.Join(names, ",")
	}
	return "other"
}

// DecodeFunctions decodes a request's functions member into the set a
// CreateRequest's Functions carries, in catalog order. A name outside the
// catalog is skipped: the caller refuses unknown names first, with
// IsFunction. A curve value must name a curve curves holds, of its
// element's curveType; a nil curves holds none. An error means a store could
// not be read, and nothing was decided about the request.
func DecodeFunctions(ctx context.Context, members map[string]json.RawMessage, curves CurveReader) (*FunctionSet, *ValueRefusal, error) {
	draft := &FunctionSet{}
	for _, f := range catalog {
		raw, ok := members[f.info().Name]
		if !ok {
			continue
		}
		if ref, err := f.decode(ctx, curves, raw, draft); ref != nil || err != nil {
			return nil, ref, err
		}
	}
	return draft, nil, nil
}

// TakeLegacy sets req's Type to the legacy type named name and its value
// from the body's members, and returns the value's log attributes. known is
// false, and req untouched, when name is not a legacy type. A member the type
// does not take, a missing member, and a value outside its bounds are
// refused.
func TakeLegacy(name string, v LegacyValues, req *CreateRequest) (known bool, attrs []LogAttr, ref *ValueRefusal) {
	t, ok := legacyTypeOf(ControlType(name))
	if !ok {
		return false, nil, nil
	}
	req.Type = t.typ
	for _, m := range legacyNotTakenOrder {
		if m != t.value && m.inBody(v) {
			return true, nil, &ValueRefusal{m.label.code + "_not_allowed", m.label.path + ": not allowed for this type"}
		}
	}
	if t.value == nil {
		return true, nil, nil
	}
	if !t.value.inBody(v) {
		return true, nil, &ValueRefusal{t.value.label.code + "_required", t.value.label.path + ": required for type " + name}
	}
	attrs, ref = t.value.take(v, req)
	return true, attrs, ref
}

// WrongTypeRefusals is the refusal for each legacy value member, and each
// member of one, whose JSON type is wrong, keyed by its path in the body.
func WrongTypeRefusals() map[string]ValueRefusal {
	out := map[string]ValueRefusal{}
	for _, m := range legacyBesideFunctionsOrder {
		s := m.shape
		out[m.label.path] = m.label.wrongType(s.kind)
		for _, f := range s.fields {
			l := m.label.member(f.name)
			out[l.path] = l.wrongType(f.shape.kind)
		}
	}
	return out
}

// RefuseBesideFunctions returns the refusal for the first legacy value
// member v carries, which a body with a functions member may not.
func RefuseBesideFunctions(v LegacyValues) *ValueRefusal {
	for _, m := range legacyBesideFunctionsOrder {
		if m.inBody(v) {
			return &ValueRefusal{m.label.code + "_beside_functions", m.label.path + ": not allowed with functions"}
		}
	}
	return nil
}

// buildLegacy builds the base for a legacy type from req's value members.
func buildLegacy(req CreateRequest) (*sep2.DERControlBase, error) {
	t, ok := legacyTypeOf(req.Type)
	if !ok {
		return nil, refuse(RefusalUnknownType)
	}
	for _, m := range legacyNotTakenOrder {
		if m != t.value && m.inRequest(req) {
			return nil, refuse(RefusalUnexpectedValue)
		}
	}
	b := &sep2.DERControlBase{}
	for _, f := range t.fixed {
		f.set(b)
	}
	if t.value != nil {
		if !t.value.inRequest(req) {
			return nil, refuse(RefusalMissingValue)
		}
		if err := t.value.build(req, b); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// baseFromFunctions builds the base for a functions set, checking each
// member as a request body's would be.
func baseFromFunctions(req CreateRequest) (*sep2.DERControlBase, error) {
	for _, m := range legacyBesideFunctionsOrder {
		if m.inRequest(req) {
			return nil, refuse(RefusalUnexpectedValue)
		}
	}
	b, err := BuildBase(req.Functions)
	if err != nil {
		return nil, err
	}
	if len(FunctionNames(&b)) == 0 {
		return nil, refuse(RefusalMissingValue)
	}
	return &b, nil
}

// MemberRefusal is a function set's member refused by BuildBase: Function is
// the catalog name of the member, and the wrapped *RefusalError carries the
// wording the admin routes give the same value.
type MemberRefusal struct {
	Function string
	*RefusalError
}

func (e *MemberRefusal) Unwrap() error { return e.RefusalError }

// BuildBase builds the base a functions set carries, checking each member
// against the catalog as a request body's would be. A set with no members
// builds the empty base. A refused member is a *MemberRefusal.
func BuildBase(set *FunctionSet) (sep2.DERControlBase, error) {
	var b sep2.DERControlBase
	if set == nil {
		return b, nil
	}
	for _, f := range catalog {
		if !f.inSet(set) {
			continue
		}
		if err := f.build(set, &b); err != nil {
			var ref *RefusalError
			if errors.As(err, &ref) {
				return sep2.DERControlBase{}, &MemberRefusal{Function: f.info().Name, RefusalError: ref}
			}
			return sep2.DERControlBase{}, err
		}
	}
	return b, nil
}

// FunctionValues is b's functions keyed by element name, in the value shapes
// a request takes. It is never nil.
func FunctionValues(b *sep2.DERControlBase) map[string]any {
	v := map[string]any{}
	if b == nil {
		return v
	}
	for _, f := range catalog {
		if f.present(b) {
			v[f.info().Name] = f.value(b)
		}
	}
	return v
}

// BaseView is b's catalog functions as the derControlBase view reports them.
func BaseView(b *sep2.DERControlBase) DERControlBaseView {
	var v DERControlBaseView
	if b == nil {
		return v
	}
	for _, f := range catalog {
		if f.present(b) {
			f.setView(b, &v)
		}
	}
	return v
}

// LogAttrs is the audit value of b: the attributes of its first function in
// catalog order that logs any.
func LogAttrs(b *sep2.DERControlBase) []LogAttr {
	if b == nil {
		return nil
	}
	for _, f := range catalog {
		if !f.present(b) {
			continue
		}
		if attrs := f.logAttrs(b); len(attrs) > 0 {
			return attrs
		}
	}
	return nil
}

// TypeName names b by the legacy type that builds exactly that base,
// "combined" for any other set of catalog functions, and "other" for a base
// with none, so a lone opModConnect is not mistaken for a legacy connect.
func TypeName(b *sep2.DERControlBase) string {
	if b == nil {
		return ""
	}
	n := len(FunctionNames(b))
	if n == 0 {
		return "other"
	}
	for _, t := range legacyTypes {
		if t.builds(b, n) {
			return string(t.typ)
		}
	}
	return "combined"
}
