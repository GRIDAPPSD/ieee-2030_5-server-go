package handler_test

// #677 fix round item 4. Two of the six call sites that answer a 500 for a
// management-pair store failure (list-by-manager and the managed-device
// lookup) cannot be driven into that branch behaviorally: h.Managers is the
// concrete *memory.EndDeviceManagementStore, and its ManagedBy and ManagerOf
// never return an error the concrete type cannot already distinguish
// (ErrNotFound, handled separately, or nil). The four sites that a store
// failure can reach (create, remove, both rekey roles) are pinned above by
// TestCreateManagementPair_WriteFailureBodyHasNoPath and its three
// siblings. This file is the "shape" alternative item 4 allows for the
// other two: it proves, from the source rather than by running it, that the
// echoing form (writeError with http.StatusInternalServerError and
// err.Error() in the body) exists nowhere in admin_management.go except
// inside writeManagementInternalError itself, so a site that cannot be
// exercised today still cannot regress unnoticed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestNoInternalServerErrorOutsideSanitizingFunc parses admin_management.go
// and asserts that http.StatusInternalServerError appears in exactly one
// writeError call, the one inside writeManagementInternalError. Any other
// call site (present or added later) answering a 500 outside that function
// is the echoing-form regression the coverage lane's MEDIUM-2 named.
func TestNoInternalServerErrorOutsideSanitizingFunc(t *testing.T) {
	fset := token.NewFileSet()
	path := filepath.Join(handlerPackageDir(t), "admin_management.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	violations := internalServerErrorSitesOutsideSanitizer(fset, file)
	if len(violations) != 0 {
		t.Fatalf("writeError(..., http.StatusInternalServerError, ...) outside writeManagementInternalError:\n%s", joinLinesH(violations))
	}
}

// TestNoInternalServerErrorOutsideSanitizingFunc_CanFail is the control: a
// fabricated site that answers 500 with an echoed error, outside the
// sanitizing function, must be reported.
func TestNoInternalServerErrorOutsideSanitizingFunc_CanFail(t *testing.T) {
	const src = `package handler

import "net/http"

func rogue(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, "rogue: "+err.Error())
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rogue.go", src, 0)
	if err != nil {
		t.Fatalf("parse fabricated source: %v", err)
	}
	violations := internalServerErrorSitesOutsideSanitizer(fset, file)
	if len(violations) == 0 {
		t.Fatal("a writeError(..., http.StatusInternalServerError, ...) call outside writeManagementInternalError went unreported: the check cannot fail")
	}
}

func handlerPackageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	return filepath.Dir(file)
}

func joinLinesH(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// internalServerErrorSitesOutsideSanitizer walks every FuncDecl in file and
// reports a writeError call whose status argument is
// http.StatusInternalServerError, found outside a function named
// writeManagementInternalError.
func internalServerErrorSitesOutsideSanitizer(fset *token.FileSet, file *ast.File) []string {
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name == "writeManagementInternalError" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "writeError" || len(call.Args) < 2 {
				return true
			}
			if isStatusInternalServerError(call.Args[1]) {
				violations = append(violations, fset.Position(call.Pos()).String()+
					": writeError(..., http.StatusInternalServerError, ...) outside writeManagementInternalError")
			}
			return true
		})
	}
	return violations
}

func isStatusInternalServerError(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http" && sel.Sel.Name == "StatusInternalServerError"
}
