package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// #991: each of these concerns has one owner. A second copy of the literal or
// call fails here, naming where it sits.

// sourceUses scans the package's non-test files and returns, per file, how
// many string literals contain lit and how many calls name call.
func sourceUses(t *testing.T, lit, call string) (lits, calls map[string]int) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	lits, calls = map[string]int{}, map[string]int{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if s, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING && strings.Contains(s, lit) {
					lits[name]++
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == call {
					calls[name]++
				}
			}
			return true
		})
	}
	if scanned < 10 {
		t.Fatalf("scanned %d source files, want at least 10", scanned)
	}
	return lits, calls
}

func total(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

func TestOneOwner_StoresWiredAnswer(t *testing.T) {
	lits, _ := sourceUses(t, "program stores are not wired", "")
	if total(lits) != 1 || lits["admin_derprogram.go"] != 1 {
		t.Errorf("the unwired-stores message is spelled in %v, want once in admin_derprogram.go (storesWired)", lits)
	}
}

func TestOneOwner_RefusalEventsAreNotSpelledByHand(t *testing.T) {
	lits, _ := sourceUses(t, "der_device_control_refused", "")
	if total(lits) != 1 || lits["admin_device_control.go"] != 1 {
		t.Errorf("der_device_control_refused is spelled in %v, want once in admin_device_control.go", lits)
	}
}

func TestOneOwner_RollbackFailureIsLoggedByPartial(t *testing.T) {
	_, calls := sourceUses(t, "", "rollbackAfter")
	if total(calls) != 1 || calls["admin_derprogram.go"] != 1 {
		t.Errorf("rollbackAfter is called from %v, want once in admin_derprogram.go (partial)", calls)
	}
}

func TestOneOwner_PrimacyBoundsAreNotRespelled(t *testing.T) {
	for _, lit := range []string{"65 to 191", "3 to 64", "0, 1, 2 or"} {
		if lits, _ := sourceUses(t, lit, ""); total(lits) != 0 {
			t.Errorf("literal %q is spelled in %v, want the bounds formatted from the constants", lit, lits)
		}
	}
}
