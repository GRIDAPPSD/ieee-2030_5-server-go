package memory_test

// #677 fix round item 3: the structural proof for item 2. mutate's own
// package comment states the property in prose; this file checks it against
// the source rather than trusting the prose to stay true. Two production
// files are in scope, the only two that reference managerOf/managedBy:
// enddevicemanagement.go (mutate and its callers) and
// enddevicemanagement_persistence.go (loadFromFile, item 6's exemption).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMutateOwnsEveryManagerWrite parses enddevicemanagement.go and asserts
// that every write to managerOf or managedBy sits inside a func literal
// passed as an argument to a call to mutate, or inside loadFromFile's body
// (item 6's documented exemption, checked separately below). A method added
// later that writes the maps directly, bypassing mutate, is caught here
// instead of by a reviewer happening to notice.
func TestMutateOwnsEveryManagerWrite(t *testing.T) {
	fset := token.NewFileSet()
	path := filepath.Join(packageDir(t), "enddevicemanagement.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	violations := lockOrderViolations(fset, file)
	if len(violations) != 0 {
		t.Fatalf("writes to managerOf/managedBy outside mutate's apply closure:\n%s", joinLines(violations))
	}
}

// TestMutateOwnsEveryManagerWrite_CanFail is the control the check itself
// needs: an enforcement rule nobody has seen fail is indistinguishable from
// no rule at all. A fabricated method writing s.managerOf directly, neither
// inside a mutate call nor named loadFromFile, must be reported.
func TestMutateOwnsEveryManagerWrite_CanFail(t *testing.T) {
	const src = `package memory

func (s *EndDeviceManagementStore) Rogue(managedLFDI, managerLFDI string) {
	s.mu.Lock()
	s.managerOf[managedLFDI] = managerLFDI
	s.mu.Unlock()
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rogue.go", src, 0)
	if err != nil {
		t.Fatalf("parse fabricated source: %v", err)
	}
	violations := lockOrderViolations(fset, file)
	if len(violations) == 0 {
		t.Fatal("a direct write to managerOf outside mutate and outside loadFromFile went unreported: the check cannot fail")
	}
}

// TestLoadFromFileHoldsPersistMu is item 6's proof: the one write path
// mutate does not own (loadFromFile, the constructor's seam) must still take
// persistMu before it touches the maps, so a reload path added later does
// not inherit the gap the review named as a counterexample.
func TestLoadFromFileHoldsPersistMu(t *testing.T) {
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

// TestLoadFromFileHoldsPersistMu_CanFail is the control for the check above.
func TestLoadFromFileHoldsPersistMu_CanFail(t *testing.T) {
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

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// region is a [start, end] span of source positions a write site is allowed
// to fall within.
type region struct{ start, end token.Pos }

func coveredByAny(regions []region, pos token.Pos) bool {
	for _, r := range regions {
		if pos >= r.start && pos <= r.end {
			return true
		}
	}
	return false
}

// isMutateCall reports whether fun is a call to a method or function named
// mutate: s.mutate(...) in the real source, or a bare mutate(...) so the
// check does not depend on the receiver's name.
func isMutateCall(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "mutate"
	case *ast.Ident:
		return f.Name == "mutate"
	}
	return false
}

// managedMapSelector reports whether e selects a field named managerOf or
// managedBy off anything: s.managerOf, s.managedBy, and so on.
func managedMapSelector(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "managerOf" || sel.Sel.Name == "managedBy"
}

// writesManagedMap reports whether e is an lvalue or a delete() first
// argument that reaches into managerOf or managedBy: the field itself
// (whole-map assignment) or an index into it.
func writesManagedMap(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return managedMapSelector(x)
	case *ast.IndexExpr:
		return managedMapSelector(x.X)
	}
	return false
}

// lockOrderViolations reports every write to managerOf/managedBy in file
// that falls outside loadFromFile's body and outside every func literal
// passed as an argument to a mutate call. It works in two passes: first it
// collects every allowed region (loadFromFile's body, and every FuncLit
// argument to a mutate call, which by construction covers the apply closure
// nested inside the build closure too, since the apply closure's source
// range sits inside the build closure's), then it flags any write whose
// position is not covered by one of them.
func lockOrderViolations(fset *token.FileSet, file *ast.File) []string {
	var allowed []region
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name.Name == "loadFromFile" && node.Body != nil {
				allowed = append(allowed, region{node.Body.Pos(), node.Body.End()})
			}
		case *ast.CallExpr:
			if isMutateCall(node.Fun) {
				for _, arg := range node.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						allowed = append(allowed, region{lit.Pos(), lit.End()})
					}
				}
			}
		}
		return true
	})

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if writesManagedMap(lhs) && !coveredByAny(allowed, lhs.Pos()) {
					violations = append(violations, fmt.Sprintf(
						"%s: assignment to a management map outside mutate's apply closure or loadFromFile",
						fset.Position(lhs.Pos())))
				}
			}
		case *ast.CallExpr:
			if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "delete" && len(node.Args) > 0 {
				if writesManagedMap(node.Args[0]) && !coveredByAny(allowed, node.Args[0].Pos()) {
					violations = append(violations, fmt.Sprintf(
						"%s: delete() on a management map outside mutate's apply closure or loadFromFile",
						fset.Position(node.Pos())))
				}
			}
		}
		return true
	})
	return violations
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
