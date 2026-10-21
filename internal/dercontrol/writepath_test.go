package dercontrol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// parsePackageSources parses the non-test Go files of dir, comments dropped.
func parsePackageSources(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob %s: %v (%d files)", dir, err, len(paths))
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = f
	}
	return files
}

// inspectWithFunc visits every node of f with the name of its top-level function.
func inspectWithFunc(f *ast.File, visit func(fn string, n ast.Node)) {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if n != nil {
				visit(fd.Name.Name, n)
			}
			return true
		})
	}
}

// TestOneWritePath pins writeCopies as the only function that creates a
// control or its lifecycle record in this package.
func TestOneWritePath(t *testing.T) {
	sites := map[string][]string{}
	for path, f := range parsePackageSources(t, ".") {
		inspectWithFunc(f, func(fn string, n ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Create" {
				return
			}
			store, ok := sel.X.(*ast.SelectorExpr)
			if !ok || (store.Sel.Name != "lifecycles" && store.Sel.Name != "controls") {
				return
			}
			sites[store.Sel.Name] = append(sites[store.Sel.Name], filepath.Base(path)+":"+fn)
		})
	}
	for _, name := range []string{"lifecycles", "controls"} {
		if got := sites[name]; len(got) != 1 || got[0] != "across.go:writeCopies" {
			t.Errorf("%s.Create is called from %v, want only across.go:writeCopies", name, got)
		}
	}
}

// TestOneIntervalEnd pins IntervalEnd as the only place start plus duration
// is computed in the packages that handle DER control intervals.
func TestOneIntervalEnd(t *testing.T) {
	var found []string
	for _, dir := range []string{".", "../handler"} {
		for path, f := range parsePackageSources(t, dir) {
			inspectWithFunc(f, func(fn string, n ast.Node) {
				if isIntervalEndExpr(n) && fn != "IntervalEnd" {
					found = append(found, filepath.Base(path)+":"+fn)
				}
			})
		}
	}
	if len(found) != 0 {
		t.Errorf("interval end computed by hand in %v; use IntervalEnd", found)
	}
}

// isIntervalEndExpr matches X.Start + int64(X.Duration), however spaced.
func isIntervalEndExpr(n ast.Node) bool {
	bin, ok := n.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return false
	}
	l, ok := bin.X.(*ast.SelectorExpr)
	if !ok || l.Sel.Name != "Start" {
		return false
	}
	conv, ok := bin.Y.(*ast.CallExpr)
	if !ok || len(conv.Args) != 1 {
		return false
	}
	r, ok := conv.Args[0].(*ast.SelectorExpr)
	return ok && r.Sel.Name == "Duration"
}
