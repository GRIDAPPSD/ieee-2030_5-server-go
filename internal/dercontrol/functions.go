package dercontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// FunctionInfo describes one catalog function for GET
// /api/der/control-functions. Value names the request value's shape.
type FunctionInfo struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
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
// of its element and V its derControlBase view. Its bounds live in its
// closures' captured ranges and in shape, built from the same intRange.
type kind[I, T, V any] struct {
	shape    shape
	parse    func(in I) (T, *fault)
	check    func(v T) RefusalCode // for a value that did not come through parse
	view     func(v T) any         // the functions view value
	baseView func(v T) V
	log      func(v T) []LogAttr // nil logs nothing
}

// fault is why parse refused a value: a missing member, or one outside
// bounds. field names the object member, or is empty for the whole value.
type fault struct {
	field   string
	missing bool
	bounds  intRange
}

func (f *fault) code() RefusalCode {
	if f.missing {
		return RefusalMissingValue
	}
	return RefusalValueOutOfRange
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

// function is a catalog row with its value types erased.
type function interface {
	info() FunctionInfo
	present(b *sep2.DERControlBase) bool
	clear(b *sep2.DERControlBase)
	decode(raw json.RawMessage, dst *sep2.DERControlBase) *ValueRefusal
	check(b *sep2.DERControlBase) RefusalCode
	copyTo(dst, src *sep2.DERControlBase)
	value(b *sep2.DERControlBase) any
	setView(b *sep2.DERControlBase, v *DERControlBaseView)
	logAttrs(b *sep2.DERControlBase) []LogAttr
}

type typedRow[I, T, V any] struct {
	name, description string
	kind              kind[I, T, V]
	label             label
	elem              func(*sep2.DERControlBase) **T
	view              func(*DERControlBaseView) **V
}

// row is one catalog function. Its refusals are worded "functions.<name>"
// with code stem "function_value" unless worded says otherwise.
func row[I, T, V any](name, description string, k kind[I, T, V], elem func(*sep2.DERControlBase) **T, view func(*DERControlBaseView) **V) *typedRow[I, T, V] {
	return &typedRow[I, T, V]{name: name, description: description, kind: k, elem: elem, view: view,
		label: label{path: "functions." + name, code: "function_value"}}
}

// worded gives a row the wording its refusals had before function sets
// existed, which the wire keeps.
func (r *typedRow[I, T, V]) worded(l label) *typedRow[I, T, V] {
	r.label = l
	return r
}

func (r *typedRow[I, T, V]) info() FunctionInfo {
	return FunctionInfo{Name: r.name, Value: r.kind.shape.text(), Description: r.description}
}

func (r *typedRow[I, T, V]) present(b *sep2.DERControlBase) bool { return *r.elem(b) != nil }

func (r *typedRow[I, T, V]) clear(b *sep2.DERControlBase) { *r.elem(b) = nil }

func (r *typedRow[I, T, V]) decode(raw json.RawMessage, dst *sep2.DERControlBase) *ValueRefusal {
	var in *I
	if !decodeStrict(raw, &in) || in == nil {
		return &ValueRefusal{"function_value_type", "functions." + r.name + ": wrong value type"}
	}
	v, f := r.kind.parse(*in)
	if f != nil {
		return r.label.refusal(f)
	}
	*r.elem(dst) = &v
	return nil
}

func (r *typedRow[I, T, V]) check(b *sep2.DERControlBase) RefusalCode {
	return r.kind.check(**r.elem(b))
}

func (r *typedRow[I, T, V]) copyTo(dst, src *sep2.DERControlBase) {
	v := **r.elem(src)
	*r.elem(dst) = &v
}

func (r *typedRow[I, T, V]) value(b *sep2.DERControlBase) any { return r.kind.view(**r.elem(b)) }

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
	inRequest func(CreateRequest) bool
	inBody    func(LegacyValues) bool
	// build sets the request's value on dst, or returns why it cannot.
	build func(req CreateRequest, dst *sep2.DERControlBase) RefusalCode
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
		inRequest: func(req CreateRequest) bool { return fromRequest(req) != nil },
		inBody:    func(v LegacyValues) bool { return fromBody(v) != nil },
		build: func(req CreateRequest, dst *sep2.DERControlBase) RefusalCode {
			v, f := r.kind.parse(*fromRequest(req))
			if f != nil {
				return f.code()
			}
			*r.elem(dst) = &v
			return ""
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

// DecodeFunctions decodes a request's functions member into the draft base
// a CreateRequest's Functions carries, in catalog order. A name outside the
// catalog is skipped: the caller refuses unknown names first, with
// IsFunction.
func DecodeFunctions(members map[string]json.RawMessage) (*sep2.DERControlBase, *ValueRefusal) {
	draft := &sep2.DERControlBase{}
	for _, f := range catalog {
		raw, ok := members[f.info().Name]
		if !ok {
			continue
		}
		if ref := f.decode(raw, draft); ref != nil {
			return nil, ref
		}
	}
	return draft, nil
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
		if code := t.value.build(req, b); code != "" {
			return nil, refuse(code)
		}
	}
	return b, nil
}

// baseFromFunctions builds the base for a functions set: every element it
// carries must be a catalog function within its bounds.
func baseFromFunctions(req CreateRequest) (*sep2.DERControlBase, error) {
	for _, m := range legacyBesideFunctionsOrder {
		if m.inRequest(req) {
			return nil, refuse(RefusalUnexpectedValue)
		}
	}
	draft := req.Functions
	rest := draft.Copy()
	b := &sep2.DERControlBase{}
	for _, f := range catalog {
		if !f.present(draft) {
			continue
		}
		if code := f.check(draft); code != "" {
			return nil, refuse(code)
		}
		f.copyTo(b, draft)
		f.clear(&rest)
	}
	// Copying only the catalog's elements would silently drop any other.
	if !reflect.ValueOf(rest).IsZero() {
		return nil, refuse(RefusalUnexpectedValue)
	}
	if len(FunctionNames(b)) == 0 {
		return nil, refuse(RefusalMissingValue)
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
