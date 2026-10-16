package auth

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConstantTimeEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "0123456789abcdef", "0123456789abcdef", true},
		{"equal empty", "", "", true},
		{"same length differs", "0123456789abcdef", "0123456789abcdeg", false},
		{"prefix is not equal", "0123456789abcdef", "0123456789abcdef0", false},
		{"longer vs shorter", "x", "0123456789abcdef", false},
		{"empty vs non-empty", "", "x", false},
		{"multibyte equal", "\u00e9\u00e9", "\u00e9\u00e9", true},
	}
	for _, tc := range tests {
		if got := ConstantTimeEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: ConstantTimeEqual(%q, %q) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestConstantTimeEqualHasNoLengthShortcut pins the shape of the fix: timing
// cannot be asserted reliably, so the test reads the function and fails if it
// regains a len() call or any branch, which is how a length-revealing early
// return would be written. Equal results alone cannot catch that regression.
func TestConstantTimeEqualHasNoLengthShortcut(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "admin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Name.Name == "ConstantTimeEqual" {
			fn = d
		}
	}
	if fn == nil {
		t.Fatal("ConstantTimeEqual not found in admin.go")
	}
	hashed := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt:
			t.Errorf("%s: ConstantTimeEqual branches; a branch on the inputs can reveal their length", fset.Position(n.Pos()))
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "len" {
				t.Errorf("%s: ConstantTimeEqual calls len", fset.Position(n.Pos()))
			}
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sum256" {
				hashed = true
			}
		}
		return true
	})
	if !hashed {
		t.Error("ConstantTimeEqual no longer hashes both inputs to a fixed length")
	}
}

func TestCheckAdminKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		want error
	}{
		{"unset", "", ErrAdminKeyUnset},
		{"blank", "   \t", ErrAdminKeyBlank},
		{"one short of the floor", "0123456789abcde", ErrAdminKeyShort},
		{"multibyte counted as characters", "\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9", ErrAdminKeyShort},
		{"at the floor", "0123456789abcdef", nil},
		{"leading and trailing space still counts", " 0123456789abcde ", nil},
	}
	for _, tc := range tests {
		if got := CheckAdminKey(tc.key); !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
			t.Errorf("%s: CheckAdminKey = %v, want %v", tc.name, got, tc.want)
		}
	}
}
