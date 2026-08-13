package assembly_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Path-value declaration guard.
//
// A handler that calls r.PathValue("id") on a route whose pattern declares no
// {id} gets the empty string back. No panic, no error, no log line: the handler
// carries on and scopes a store lookup by "", which is a different collection
// from the one the client asked for. Two live instances existed when this guard
// was written, both from scopedListHandler being mounted on patterns that name
// their wildcard something other than "id" (GET /upt/{uptId}/mr and
// GET /msg/{msgId}/tm); both are fixed, and this guard is what keeps a third
// from being mounted quietly.
//
// This shares the AST harness with the mintable-href scan in hrefsource_test.go
// rather than the runtime probe in hrefs.go, and the reason is worth stating.
// The href check compares two things that both exist at runtime, a shape and a
// route, so it can be a boot-time assertion. This one compares a route pattern
// against the body of the function mounted on it, and the body is not a runtime
// value. There is nothing to probe: an empty PathValue is indistinguishable at
// runtime from a genuinely empty segment. So the guard is necessarily
// test-time, and it is the source scan, not the router assertion, that it
// extends.
//
// # Resolution depth
//
// Handlers are mounted three ways in assembly.go, and the guard follows all
// three within this package:
//
//   - an inline func literal, whose body is scanned directly;
//   - a call to a package-local helper, generic or not, whose declaration is
//     found and scanned, transitively, so scopedListHandlerDeep is followed
//     into deepScopeKey;
//   - a call to a helper that takes the path-value NAME as a string parameter,
//     as scopedResourceHandlerDeep does, where the literal passed at the call
//     site is bound to the parameter before the body is scanned.
//
// A handler from another package is not followed. Cross-package resolution
// needs go/types and a loaded program, which is a much heavier dependency than
// the defect justifies, and the two known instances are both package-local.
// Those calls are counted and reported so the coverage of this guard is a
// number rather than an impression.

// patternWildcard matches a {name} or {name...} segment in a ServeMux pattern.
var patternWildcard = regexp.MustCompile(`\{([a-zA-Z0-9_]+)(\.\.\.)?\}`)

// knownPathValueMismatches is the RATCHET for this guard, in the same shape and
// for the same reason as knownUnroutedHrefs in hrefs.go: the instances that
// exist today are tolerated by name, anything new fails, and an entry that
// starts passing fails the test so the set has to shrink.
//
// It is EMPTY, and that is the fixed state of this defect class rather than an
// absence of coverage. The two entries it carried, GET /upt/{uptId}/mr and
// GET /msg/{msgId}/tm, were both scopedListHandler mounts reading a hardcoded
// "id"; that helper was given the parentParam argument its sibling
// scopedResourceHandler already had, so there is no longer a mount that can read
// a name without naming it. An entry added here again is a route knowingly
// serving the wrong scope, and it needs the card that will remove it.
//
// Keys are "PATTERN reads NAME".
var knownPathValueMismatches = map[string]string{}

// TestRoutePatternsDeclareEveryPathValueTheirHandlersRead is the guard.
func TestRoutePatternsDeclareEveryPathValueTheirHandlersRead(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "assembly.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing assembly.go: %v", err)
	}
	funcs := packageFuncs(file)

	var routes, unresolved int
	actual := make(map[string]string)

	for _, call := range handleFuncCalls(file) {
		routes++
		declared := make(map[string]bool)
		for _, m := range patternWildcard.FindAllStringSubmatch(call.pattern, -1) {
			declared[m[1]] = true
		}

		reads, resolvedOK := pathValueReads(call.handler, funcs, nil, map[string]bool{})
		if !resolvedOK {
			unresolved++
		}

		sort.Strings(reads)
		for _, name := range reads {
			if declared[name] {
				continue
			}
			key := fmt.Sprintf("%s reads %q", call.pattern, name)
			actual[key] = fset.Position(call.pos).String()
		}
	}

	if routes == 0 {
		t.Fatal("no HandleFunc calls found in assembly.go; the guard would pass vacuously")
	}
	t.Logf("routes=%d cross-package handlers not followed=%d mismatches=%d", routes, unresolved, len(actual))

	for key, pos := range actual {
		if _, ok := knownPathValueMismatches[key]; !ok {
			t.Errorf("NEW path-value mismatch at %s: %s.\n"+
				"The handler reads a path value the pattern does not declare, so it gets \"\" and scopes by the empty string.\n"+
				"Rename the wildcard to match, make the handler read the declared name, or add it to knownPathValueMismatches with the card that will fix it.",
				pos, key)
		}
	}
	for key, reason := range knownPathValueMismatches {
		if _, ok := actual[key]; !ok {
			t.Errorf("knownPathValueMismatches entry %q no longer holds; delete the entry so the ratchet keeps shrinking.\nrecorded reason: %s",
				key, reason)
		}
	}
}

// handleFuncCall is one mux.HandleFunc(pattern, handler) registration.
type handleFuncCall struct {
	pattern string
	handler ast.Expr
	pos     token.Pos
}

// handleFuncCalls finds every HandleFunc call with a literal pattern. A
// non-literal pattern would mean routes are computed at runtime, which this
// package does not do; if that ever changes the guard silently narrows, which
// is why the route count is asserted non-zero and logged.
func handleFuncCalls(file *ast.File) []handleFuncCall {
	var out []handleFuncCall
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		pattern, ok := stringLiteral(call.Args[0])
		if !ok {
			return true
		}
		out = append(out, handleFuncCall{pattern: pattern, handler: call.Args[1], pos: call.Pos()})
		return true
	})
	return out
}

// packageFuncs indexes the top-level function declarations in a file by name so
// a handler expression can be resolved to a body.
func packageFuncs(file *ast.File) map[string]*ast.FuncDecl {
	out := make(map[string]*ast.FuncDecl)
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil {
			out[fd.Name.Name] = fd
		}
	}
	return out
}

// pathValueReads returns the path-value names an expression's handler reads,
// and whether every function it went through was resolvable.
//
// bindings maps a parameter name to the string literal supplied for it at the
// call site, which is what lets a helper taking the path-value name as an
// argument be checked. visited breaks recursion.
func pathValueReads(e ast.Expr, funcs map[string]*ast.FuncDecl, bindings map[string]string, visited map[string]bool) ([]string, bool) {
	switch x := e.(type) {
	case *ast.FuncLit:
		return scanBodyForPathValues(x.Body, funcs, bindings, visited)

	case *ast.CallExpr:
		name, ok := calleeName(x.Fun)
		if !ok {
			// A method value, a field, or something else this guard does
			// not follow.
			return nil, false
		}
		decl, ok := funcs[name]
		if !ok || decl.Body == nil {
			// Another package. Not followed: see the file comment.
			return nil, false
		}
		if visited[name] {
			return nil, true
		}
		visited[name] = true
		return scanBodyForPathValues(decl.Body, funcs, bindArgs(decl, x.Args), visited)

	default:
		return nil, false
	}
}

// scanBodyForPathValues collects PathValue names from a function body and
// follows calls to other package-local functions.
func scanBodyForPathValues(body *ast.BlockStmt, funcs map[string]*ast.FuncDecl, bindings map[string]string, visited map[string]bool) ([]string, bool) {
	var names []string
	resolved := true

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PathValue" && len(call.Args) == 1 {
			if lit, ok := stringLiteral(call.Args[0]); ok {
				names = appendUniqueString(names, lit)
				return true
			}
			// The name came from a variable. If it is a parameter bound to
			// a literal at the call site, that literal is the name;
			// otherwise the read is unresolvable and the guard says so
			// rather than passing on silence.
			if id, ok := call.Args[0].(*ast.Ident); ok {
				if v, bound := bindings[id.Name]; bound {
					names = appendUniqueString(names, v)
					return true
				}
			}
			resolved = false
			return true
		}

		if name, ok := calleeName(call.Fun); ok {
			if decl, found := funcs[name]; found && decl.Body != nil && !visited[name] {
				visited[name] = true
				inner, innerOK := scanBodyForPathValues(decl.Body, funcs, bindArgs(decl, call.Args), visited)
				for _, v := range inner {
					names = appendUniqueString(names, v)
				}
				resolved = resolved && innerOK
			}
		}
		return true
	})
	return names, resolved
}

// calleeName extracts the package-local function name from a call's Fun,
// unwrapping a generic instantiation. scopedListHandler[A, B](...) parses as an
// IndexListExpr over the identifier, and scopedResourceHandlerDeep[T](...) as
// an IndexExpr; both have to unwrap or every generic helper would be treated as
// unresolvable.
func calleeName(fun ast.Expr) (string, bool) {
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name, true
	case *ast.IndexExpr:
		return calleeName(x.X)
	case *ast.IndexListExpr:
		return calleeName(x.X)
	default:
		return "", false
	}
}

// bindArgs pairs a declaration's string parameters with the literals passed for
// them, so a helper that takes a path-value name as an argument can be checked
// against the pattern it is mounted on.
func bindArgs(decl *ast.FuncDecl, args []ast.Expr) map[string]string {
	out := make(map[string]string)
	if decl.Type.Params == nil {
		return out
	}
	i := 0
	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			if i < len(args) {
				if lit, ok := stringLiteral(args[i]); ok {
					out[name.Name] = lit
				}
			}
			i++
		}
	}
	return out
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

func appendUniqueString(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// TestPathValueGuard_ResolvesTheHelperFormsItClaimsTo asserts the resolver
// actually follows the three mounting forms. Without this, a resolver that
// silently stopped following package-local calls would report zero mismatches
// and the guard would pass by finding nothing.
func TestPathValueGuard_ResolvesTheHelperFormsItClaimsTo(t *testing.T) {
	t.Parallel()

	const src = `package p

func direct(r *http.Request) string { return r.PathValue("direct") }

func viaHelper(r *http.Request) string { return helper(r) }

func helper(r *http.Request) string { return r.PathValue("nested") }

func generic[T any](r *http.Request, param string) string {
	return r.PathValue(param) + r.PathValue("fixed")
}

func mount(mux Mux) {
	mux.HandleFunc("GET /a/{direct}", func(w http.ResponseWriter, r *http.Request) { direct(r) })
	mux.HandleFunc("GET /b/{x}", viaHelper)
	mux.HandleFunc("GET /c/{x}", generic[int](r, "bound"))
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mount.go", src, 0)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	funcs := packageFuncs(file)
	calls := handleFuncCalls(file)
	if len(calls) != 3 {
		t.Fatalf("fixture should yield 3 registrations, got %d", len(calls))
	}

	// A func literal that calls a package-local function is followed into it.
	got, ok := pathValueReads(calls[0].handler, funcs, nil, map[string]bool{})
	if !ok || !containsString(got, "direct") {
		t.Errorf("func literal form: got %v resolved=%v, want the nested read %q", got, ok, "direct")
	}

	// A bare identifier handler is NOT a call, so it is reported unresolved
	// rather than silently treated as reading nothing. Recording that as a
	// known limitation is the point of the unresolved count in the guard.
	if _, ok := pathValueReads(calls[1].handler, funcs, nil, map[string]bool{}); ok {
		t.Error("a bare identifier handler should be reported unresolved, not resolved to no reads")
	}

	// A generic instantiation is unwrapped, and a name passed as a string
	// argument is bound to the parameter before the body is scanned.
	got, ok = pathValueReads(calls[2].handler, funcs, nil, map[string]bool{})
	if !ok {
		t.Errorf("generic form: expected resolution, got %v", got)
	}
	for _, want := range []string{"bound", "fixed"} {
		if !containsString(got, want) {
			t.Errorf("generic form: got %v, want it to include %q", got, want)
		}
	}
}

// TestPathValueGuard_PatternWildcardParsing pins the pattern parsing, since a
// regexp that stopped matching would make every route look like it declares
// nothing, turning the guard into noise, or match too much and turn it off.
func TestPathValueGuard_PatternWildcardParsing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		want    []string
	}{
		{"GET /edev/{id}/fsa/{fsaId}", []string{"id", "fsaId"}},
		{"GET /upt/{uptId}/mr/{mrId}/r", []string{"uptId", "mrId"}},
		{"GET /dcap", nil},
		{"GET /files/{path...}", []string{"path"}},
	}
	for _, tc := range cases {
		var got []string
		for _, m := range patternWildcard.FindAllStringSubmatch(tc.pattern, -1) {
			got = append(got, m[1])
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%q: got %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func containsString(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
