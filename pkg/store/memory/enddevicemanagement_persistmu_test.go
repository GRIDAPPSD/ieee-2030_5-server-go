package memory_test

// #677 fix round item 3: loadFromFile is the one write path mutate does not
// own (item 6's documented exemption), so this checks the one thing the
// method-set pin cannot: that it still takes persistMu before touching the
// maps, the same lock every mutate-routed write holds.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoadFromFileTakesPersistMu is item 6's proof: the one write path
// mutate does not own (loadFromFile, the constructor's seam) must still take
// persistMu before it touches the maps, so a reload path added later does
// not inherit the gap the review named as a counterexample.
func TestLoadFromFileTakesPersistMu(t *testing.T) {
	fset := token.NewFileSet()
	path := filepath.Join(packageDir(t), "enddevicemanagement_persistence.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if !loadFromFileLocksPersistMu(file) {
		t.Fatal("loadFromFile does not take persistMu before writing managerOf/managedBy")
	}
}

// TestLoadFromFileTakesPersistMu_CanFail is the control for the check above.
func TestLoadFromFileTakesPersistMu_CanFail(t *testing.T) {
	const src = `package memory

func (s *EndDeviceManagementStore) loadFromFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managerOf = nil
	return nil
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rogue.go", src, 0)
	if err != nil {
		t.Fatalf("parse fabricated source: %v", err)
	}
	if loadFromFileLocksPersistMu(file) {
		t.Fatal("a loadFromFile with no persistMu.Lock() call was reported as holding it: the check cannot fail")
	}
}

func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	return filepath.Dir(file)
}

// loadFromFileLocksPersistMu reports whether file's loadFromFile method
// calls persistMu.Lock() (by any receiver name) anywhere in its body.
func loadFromFileLocksPersistMu(file *ast.File) bool {
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.FuncDecl)
		if !ok || decl.Name.Name != "loadFromFile" || decl.Body == nil {
			return true
		}
		ast.Inspect(decl.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Lock" {
				return true
			}
			owner, ok := sel.X.(*ast.SelectorExpr)
			if ok && owner.Sel.Name == "persistMu" {
				found = true
			}
			return true
		})
		return false
	})
	return found
}
