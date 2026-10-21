package dercontrol

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// elementIdent matches a Go identifier naming a DERControlBase element, and
// elementText an element's wire name inside a string literal.
var (
	elementIdent = regexp.MustCompile(`^(OpMod[A-Za-z]+|RampTms)$`)
	elementText  = regexp.MustCompile(`\b(opMod[A-Za-z]+|rampTms)\b`)
)

// elementOwners are the only non-test files that may name a DERControlBase
// element. link.go reads opModTargetW for what it means to a grant, not as a
// catalog function.
var elementOwners = map[string]bool{"catalog.go": true, "link.go": true}

// A function is one catalog row: any other non-test file in this package or
// the admin handler package that names an element, as an identifier or in a
// string literal, is a second copy of a function the catalog owns. Its scope
// is those two packages, the catalog's consumers; comments are not read.
func TestNoElementOutsideCatalog(t *testing.T) {
	var found []string
	scanned := 0
	for _, dir := range []string{".", filepath.Join("..", "handler")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || (dir == "." && elementOwners[filepath.Base(f)]) {
				continue
			}
			scanned++
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(af, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if elementIdent.MatchString(x.Name) {
						found = append(found, fset.Position(x.Pos()).String()+": "+x.Name)
					}
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						break
					}
					text, err := strconv.Unquote(x.Value)
					if err != nil {
						t.Fatalf("%s: %v", fset.Position(x.Pos()), err)
					}
					if elementText.MatchString(text) {
						found = append(found, fset.Position(x.Pos()).String()+": "+x.Value)
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no files")
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("%d element identifiers outside catalog.go in %d scanned files:\n%s", len(found), scanned, strings.Join(found, "\n"))
	}
}

// notOffered declines each core DERControlBase element the catalog has no
// row for, with the reason. A row for one of these removes it here.
var notOffered = map[string]string{
	"opModFreqWatt":               "curve: needs a stored-curve value kind",
	"opModHFRTMayTrip":            "curve: needs a stored-curve value kind",
	"opModHFRTMustTrip":           "curve: needs a stored-curve value kind",
	"opModHVRTMayTrip":            "curve: needs a stored-curve value kind",
	"opModHVRTMomentaryCessation": "curve: needs a stored-curve value kind",
	"opModHVRTMustTrip":           "curve: needs a stored-curve value kind",
	"opModLFRTMayTrip":            "curve: needs a stored-curve value kind",
	"opModLFRTMustTrip":           "curve: needs a stored-curve value kind",
	"opModLVRTMayTrip":            "curve: needs a stored-curve value kind",
	"opModLVRTMomentaryCessation": "curve: needs a stored-curve value kind",
	"opModLVRTMustTrip":           "curve: needs a stored-curve value kind",
	"opModVoltVar":                "curve: needs a stored-curve value kind",
	"opModVoltWatt":               "curve: needs a stored-curve value kind",
	"opModWattPF":                 "curve: needs a stored-curve value kind",
	"opModWattVar":                "curve: needs a stored-curve value kind",
}

// Every element the pinned core models is a catalog row or is declined with
// a reason, so a core release that adds an element fails here until it is
// one or the other.
func TestCatalogOwnsElements(t *testing.T) {
	rows := map[string]bool{}
	for _, f := range catalog {
		rows[f.info().Name] = true
	}
	elements := map[string]bool{}
	typ := reflect.TypeFor[sep2.DERControlBase]()
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("xml"), ",")
		if name == "" {
			t.Errorf("field %s has no element name", typ.Field(i).Name)
			continue
		}
		elements[name] = true
		reason, declined := notOffered[name]
		switch {
		case rows[name] && declined:
			t.Errorf("%s is a row and also declined", name)
		case !rows[name] && !declined:
			t.Errorf("%s is neither a row nor declined", name)
		case declined && reason == "":
			t.Errorf("%s is declined with no reason", name)
		}
	}
	for name := range rows {
		if !elements[name] {
			t.Errorf("row %s names no DERControlBase element", name)
		}
	}
	for name := range notOffered {
		if !elements[name] {
			t.Errorf("declined %s names no DERControlBase element", name)
		}
	}
	if len(elements) != len(rows)+len(notOffered) {
		t.Errorf("%d elements, %d rows, %d declined", len(elements), len(rows), len(notOffered))
	}
}

// The functions set and the derControlBase view have one field per row, in
// catalog order, named as the row is.
func TestStructsFollowCatalog(t *testing.T) {
	var names []string
	for _, f := range Catalog() {
		names = append(names, f.Name)
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[FunctionSet](), reflect.TypeFor[DERControlBaseView]()} {
		var fields []string
		for i := range typ.NumField() {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			fields = append(fields, name)
		}
		if !reflect.DeepEqual(fields, names) {
			t.Errorf("%s fields = %v, catalog = %v", typ.Name(), fields, names)
		}
	}
}

// Every row's value, and every member of an object value, has a kind, so a
// descriptor built from the shapes never meets one it cannot name.
func TestShapesHaveKinds(t *testing.T) {
	var walk func(where string, s shape)
	walk = func(where string, s shape) {
		if s.kind.String() == "" || s.kind.noun() == "" {
			t.Errorf("%s: no kind", where)
		}
		switch s.kind {
		case kindInteger:
			if s.bounds.min > s.bounds.max {
				t.Errorf("%s: bounds %s", where, s.bounds)
			}
		case kindObject:
			if len(s.fields) == 0 {
				t.Errorf("%s: object with no members", where)
			}
		}
		for _, f := range s.fields {
			walk(where+"."+f.name, f.shape)
		}
	}
	for _, f := range catalog {
		walk(f.info().Name, f.shapeOf())
	}
}

// A member the shape marks required is refused as missing when a request
// leaves it out, and any other member may be left out.
func TestObjectMembersRequiredAsMarked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape shape
		full  map[string]any
	}{
		{FuncFixedPFInjectW, rowFixedPFInjectW.kind.shape, map[string]any{"displacement": 900, "excitation": true}},
		{FuncTargetW, rowTargetW.kind.shape, map[string]any{"value": 1, "multiplier": 1}},
		{FuncFixedPFAbsorbW, rowFixedPFAbsorbW.kind.shape, map[string]any{"displacement": 900, "excitation": false}},
		{FuncFixedVar, rowFixedVar.kind.shape, map[string]any{"refType": 2, "value": -500}},
		{FuncFreqDroop, rowFreqDroop.kind.shape, map[string]any{"dBOF": 36, "dBUF": 36, "kOF": 5, "kUF": 5, "openLoopTms": 500}},
		{FuncTargetVar, rowTargetVar.kind.shape, map[string]any{"value": 1, "multiplier": 1}},
	} {
		for _, f := range tc.shape.fields {
			value := map[string]any{}
			for k, v := range tc.full {
				if k != f.name {
					value[k] = v
				}
			}
			raw, err := json.Marshal(map[string]any{tc.name: value})
			if err != nil {
				t.Fatal(err)
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(raw, &members); err != nil {
				t.Fatal(err)
			}
			_, ref := DecodeFunctions(members)
			refusedMissing := ref != nil && strings.HasSuffix(ref.Code, "_required")
			if refusedMissing != f.required {
				t.Errorf("%s without %s: refusal %+v, required %v", tc.name, f.name, ref, f.required)
			}
		}
	}
}

// An object kind's fields are the request keys its input decodes, in order,
// so a descriptor built from the kind names what a request sends.
func TestObjectKindsNameTheirRequestKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape shape
		input reflect.Type
	}{
		{FuncFixedPFInjectW, rowFixedPFInjectW.kind.shape, reflect.TypeFor[PowerFactorInput]()},
		{FuncTargetW, rowTargetW.kind.shape, reflect.TypeFor[ActivePowerInput]()},
		{FuncFixedPFAbsorbW, rowFixedPFAbsorbW.kind.shape, reflect.TypeFor[PowerFactorInput]()},
		{FuncFixedVar, rowFixedVar.kind.shape, reflect.TypeFor[FixedVarInput]()},
		{FuncFreqDroop, rowFreqDroop.kind.shape, reflect.TypeFor[FreqDroopInput]()},
		{FuncTargetVar, rowTargetVar.kind.shape, reflect.TypeFor[ActivePowerInput]()},
	} {
		var keys, fields []string
		for i := range tc.input.NumField() {
			key, _, _ := strings.Cut(tc.input.Field(i).Tag.Get("json"), ",")
			keys = append(keys, key)
		}
		for _, f := range tc.shape.fields {
			fields = append(fields, f.name)
		}
		if tc.shape.kind != kindObject || !reflect.DeepEqual(fields, keys) {
			t.Errorf("%s: kind %q fields %v, request keys %v", tc.name, tc.shape.kind, fields, keys)
		}
	}
}
