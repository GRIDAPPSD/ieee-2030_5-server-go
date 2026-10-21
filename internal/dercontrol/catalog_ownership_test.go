package dercontrol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// elementIdent matches a Go identifier naming a DERControlBase element.
var elementIdent = regexp.MustCompile(`^(OpMod[A-Za-z]+|RampTms)$`)

// elementOwners are the only non-test files that may name a DERControlBase
// element. link.go reads opModTargetW for what it means to a grant, not as a
// catalog function.
var elementOwners = map[string]bool{"catalog.go": true, "link.go": true}

// A function is one catalog row: any other non-test file in this package or
// the admin handler package that names an element is a second copy of a
// function the catalog owns.
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
				if id, ok := n.(*ast.Ident); ok && elementIdent.MatchString(id.Name) {
					found = append(found, fset.Position(id.Pos()).String()+": "+id.Name)
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
	"opModFixedPFAbsorbW":         "fixed power factor while absorbing: not yet offered",
	"opModFixedVar":               "needs a FixedVar value kind",
	"opModFixedW":                 "needs a SignedPerCent value kind",
	"opModFreqDroop":              "needs a FreqDroopType value kind",
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
	"opModTargetVar":              "needs a ReactivePower value kind",
	"opModVoltVar":                "curve: needs a stored-curve value kind",
	"opModVoltWatt":               "curve: needs a stored-curve value kind",
	"opModWattPF":                 "curve: needs a stored-curve value kind",
	"opModWattVar":                "curve: needs a stored-curve value kind",
	"rampTms":                     "a ramp time, not a function: not yet offered",
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

// The derControlBase view has one field per row, in catalog order, named as
// the row is.
func TestBaseViewFollowsCatalog(t *testing.T) {
	typ := reflect.TypeFor[DERControlBaseView]()
	var fields []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	var names []string
	for _, f := range Catalog() {
		names = append(names, f.Name)
	}
	if !reflect.DeepEqual(fields, names) {
		t.Fatalf("DERControlBaseView fields = %v, catalog = %v", fields, names)
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
	} {
		var keys, fields []string
		for i := range tc.input.NumField() {
			key, _, _ := strings.Cut(tc.input.Field(i).Tag.Get("json"), ",")
			keys = append(keys, key)
		}
		for _, f := range tc.shape.fields {
			fields = append(fields, f.name)
		}
		if tc.shape.kind != "object" || !reflect.DeepEqual(fields, keys) {
			t.Errorf("%s: kind %q fields %v, request keys %v", tc.name, tc.shape.kind, fields, keys)
		}
	}
}
