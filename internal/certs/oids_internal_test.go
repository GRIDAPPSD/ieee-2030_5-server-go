package certs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// TestDeviceTypeCatalogComplete parses oids.go's DeviceType const block at
// test time and fails if a constant is declared there without a matching
// entry in deviceTypeCatalog, or the reverse. This is the backstop for the
// one drift AllDeviceTypes's reference to the named constants cannot catch
// on its own: a constant added to the const block and never wired into the
// catalog at all.
func TestDeviceTypeCatalogComplete(t *testing.T) {
	declared := declaredDeviceTypeValues(t, "oids.go")

	catalogValues := make([]int, 0, len(deviceTypeCatalog))
	for _, dt := range deviceTypeCatalog {
		catalogValues = append(catalogValues, int(dt))
	}

	sort.Ints(declared)
	sort.Ints(catalogValues)

	if !reflect.DeepEqual(declared, catalogValues) {
		t.Fatalf("oids.go declares DeviceType values %v but deviceTypeCatalog lists %v: a constant was added or removed without updating deviceTypeCatalog", declared, catalogValues)
	}
}

// declaredDeviceTypeValues parses path and returns the integer value of
// every const declared with the explicit type DeviceType.
func declaredDeviceTypeValues(t *testing.T, path string) []int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var values []int
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "DeviceType" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					continue
				}
				n, err := strconv.Atoi(lit.Value)
				if err != nil {
					t.Fatalf("parse const value %q: %v", lit.Value, err)
				}
				values = append(values, n)
			}
		}
	}
	return values
}

// TestAllDeviceTypesMatchesConstants pins the field-level shape AllDeviceTypes
// returns: each entry's Value is the constant's own int value, not a second
// literal.
func TestAllDeviceTypesMatchesConstants(t *testing.T) {
	want := []DeviceTypeInfo{
		{Value: int(DeviceTypeGeneric), Name: "generic", Label: "Generic"},
		{Value: int(DeviceTypeMobile), Name: "mobile", Label: "Mobile"},
		{Value: int(DeviceTypePostMfg), Name: "post_manufacture", Label: "Post-Manufacture"},
	}
	got := AllDeviceTypes()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllDeviceTypes() = %+v, want %+v", got, want)
	}
}
