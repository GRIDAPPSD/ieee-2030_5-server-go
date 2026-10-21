package dercontrol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// RefusalCodes is the list a caller proves its wire map against, so it must
// hold every RefusalCode constant the package declares.
func TestRefusalCodes_ListsEveryDeclaredCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var declared []RefusalCode
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "RefusalCode" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s: a RefusalCode constant is not a string literal", name)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					declared = append(declared, RefusalCode(s))
				}
			}
		}
	}
	// The control: a scan that found nothing would pass the loop below.
	if len(declared) < 17 {
		t.Fatalf("found %d declared RefusalCode constants, want at least 17", len(declared))
	}
	for _, c := range declared {
		if !slices.Contains(RefusalCodes, c) {
			t.Errorf("RefusalCodes lacks %q", c)
		}
	}
	if len(RefusalCodes) != len(declared) {
		t.Errorf("RefusalCodes has %d entries, %d codes are declared", len(RefusalCodes), len(declared))
	}
}
