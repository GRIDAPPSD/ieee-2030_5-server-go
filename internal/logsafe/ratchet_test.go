package logsafe

import (
	"bufio"
	"cmp"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

// The ratchet: a log call must take a constant message, and every dynamic
// value must pass through this package or be a number or bool. Files that
// still hold raw calls are counted in ratchet_baseline.txt; a file may only
// stay level or fall, and a file not listed holds none.

var updateBaseline = flag.Bool("update-ratchet", false, "rewrite ratchet_baseline.txt from the current tree")

const baselineFile = "ratchet_baseline.txt"

// moduleRoot is the repository root, two levels above this package.
const moduleRoot = "../.."

const ownPkgPath = "github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"

type violation struct {
	file   string
	line   int
	col    int
	reason string
}

func (v violation) String() string { return fmt.Sprintf("%s:%d: %s", v.file, v.line, v.reason) }

// logFuncs maps a standard-library logging function or method name to the
// index of its message argument, or -1 when every argument is a value.
// log.Output takes the finished line as its message, so a dynamic line is a
// dynamic message.
var logFuncs = map[string]map[string]int{
	"log": {
		"Printf": 0, "Fatalf": 0, "Panicf": 0, "Output": 1,
		"Print": -1, "Println": -1, "Fatal": -1, "Fatalln": -1, "Panic": -1, "Panicln": -1,
	},
	"log/slog": {
		"Debug": 0, "Info": 0, "Warn": 0, "Error": 0,
		"DebugContext": 1, "InfoContext": 1, "WarnContext": 1, "ErrorContext": 1,
		"Log": 2, "LogAttrs": 2,
		"With": -1,
	},
}

// attrFuncs are the slog constructors whose arguments after the key are
// values.
var attrFuncs = []string{"String", "Int", "Int64", "Uint64", "Float64", "Bool", "Duration", "Time", "Any", "Group"}

// target says where a logging call's message and values sit: the message is
// args[msgIdx] (none when -1) and the values are args[from:].
type target struct{ msgIdx, from int }

// loadSet is one type-checked view of the tree. Build constraints select
// files, so each tag or platform that hides log calls from the default view
// is loaded on its own and the findings are merged.
type loadSet struct {
	name     string
	tags     string
	env      []string
	patterns []string
}

var loadSets = []loadSet{
	{name: "default", patterns: []string{"./cmd/...", "./internal/...", "./pkg/..."}},
	{name: "csip_test_hooks", tags: "csip_test_hooks", patterns: []string{"./cmd/...", "./internal/...", "./pkg/..."}},
	{name: "windows", env: []string{"GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0"}, patterns: []string{"./cmd/..."}},
}

// cgoSets are packages whose build tag pulls in cgo and a system library. They
// are parsed and type-checked here with go/types and FakeImportC instead of
// through go list, so the count is the same on a host without the library
// (CI has no HELICS) and nothing is skipped.
var cgoSets = []struct {
	name string
	dir  string
	tags []string
}{
	{name: "helics", dir: "internal/helics", tags: []string{"helics"}},
}

// loadCgoPackage returns the package at dir as it builds under tags, with C
// references left untyped. A type error that is not about C fails the test,
// so a package that stops checking cannot go silent.
func loadCgoPackage(t *testing.T, name, dir string, tags []string) *packages.Package {
	t.Helper()
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := build.Default
	ctx.BuildTags = append(slices.Clone(ctx.BuildTags), tags...)
	ctx.CgoEnabled = true
	bp, err := ctx.ImportDir(filepath.Join(root, dir), 0)
	if err != nil {
		t.Fatalf("cgo set %s: %v", name, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, n := range slices.Concat(bp.GoFiles, bp.CgoFiles) {
		f, err := parser.ParseFile(fset, filepath.Join(bp.Dir, n), nil, 0)
		if err != nil {
			t.Fatalf("cgo set %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("cgo set %s: no files under tags %v", name, tags)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		FakeImportC: true,
		Importer:    importer.ForCompiler(fset, "source", nil),
		Error:       func(err error) { t.Errorf("cgo set %s: type error: %v", name, err) },
	}
	if _, err := conf.Check(bp.ImportPath, fset, files, info); err != nil && !t.Failed() {
		t.Fatalf("cgo set %s: %v", name, err)
	}
	return &packages.Package{PkgPath: bp.ImportPath, Fset: fset, Syntax: files, TypesInfo: info}
}

func loadPackages(t *testing.T, set loadSet) []*packages.Package {
	t.Helper()
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: root,
		Env: append(os.Environ(), set.env...),
	}
	if set.tags != "" {
		cfg.BuildFlags = []string{"-tags=" + set.tags}
	}
	pkgs, err := packages.Load(cfg, set.patterns...)
	if err != nil {
		t.Fatalf("loading %s %v: %v", set.name, set.patterns, err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("load set %s matched no packages", set.name)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("load set %s: package %s does not type-check: %v", set.name, p.PkgPath, p.Errors[0])
		}
	}
	return pkgs
}

func calleeObj(info *types.Info, call *ast.CallExpr) types.Object {
	var id *ast.Ident
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	default:
		return nil
	}
	return info.Uses[id]
}

func isOwnFunc(obj types.Object, names ...string) bool {
	f, _ := obj.(*types.Func)
	return f != nil && f.Pkg() != nil && f.Pkg().Path() == ownPkgPath && slices.Contains(names, f.Name())
}

// printfLike reports a func-typed variable shaped like log.Printf, how a
// function value such as `logf = log.Printf` is called.
func printfLike(obj types.Object) bool {
	v, ok := obj.(*types.Var)
	if !ok {
		return false
	}
	sig, ok := v.Type().Underlying().(*types.Signature)
	if !ok || !sig.Variadic() || sig.Params().Len() != 2 || sig.Results().Len() != 0 {
		return false
	}
	first, ok := sig.Params().At(0).Type().Underlying().(*types.Basic)
	return ok && first.Kind() == types.String
}

// paramIndex returns the index of the first parameter of fn that e names, or
// -1; a message built from one parameter ("prefix: "+format) counts.
func paramIndex(info *types.Info, fn *types.Func, e ast.Expr) int {
	sig := fn.Type().(*types.Signature)
	idx := -1
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && idx < 0 {
			for i := 0; i < sig.Params().Len(); i++ {
				if info.Uses[id] == sig.Params().At(i) {
					idx = i
				}
			}
		}
		return true
	})
	return idx
}

// classify returns where a call's message and values sit when the call logs:
// a standard-library logging function, a known forwarding wrapper, or a
// variable holding a log.Printf-shaped function.
func classify(info *types.Info, call *ast.CallExpr, wrappers map[*types.Func]target) (target, bool) {
	obj := calleeObj(info, call)
	if f, ok := obj.(*types.Func); ok && f.Pkg() != nil {
		if idx, ok := logFuncs[f.Pkg().Path()][f.Name()]; ok {
			return target{idx, idx + 1}, true
		}
		if w, ok := wrappers[f]; ok {
			return w, true
		}
	}
	if printfLike(obj) {
		return target{0, 1}, true
	}
	return target{}, false
}

// findWrappers returns the functions that forward their variadic parameter to
// a logging call, directly or through another wrapper, and the forwarding
// calls themselves. A wrapper logs nothing of its own: its call sites are
// where a value reaches a log, so they are counted and the forward is not.
func findWrappers(pkgs []*packages.Package) (map[*types.Func]target, map[*ast.CallExpr]bool) {
	wrappers := map[*types.Func]target{}
	forwards := map[*ast.CallExpr]bool{}
	for changed := true; changed; {
		changed = false
		for _, p := range pkgs {
			for _, f := range p.Syntax {
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					fn, _ := p.TypesInfo.Defs[fd.Name].(*types.Func)
					if fn == nil || !fn.Type().(*types.Signature).Variadic() {
						continue
					}
					sig := fn.Type().(*types.Signature)
					variadic := sig.Params().At(sig.Params().Len() - 1)
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok || !call.Ellipsis.IsValid() || forwards[call] {
							return true
						}
						spread, ok := ast.Unparen(call.Args[len(call.Args)-1]).(*ast.Ident)
						if !ok || p.TypesInfo.Uses[spread] != variadic {
							return true
						}
						tg, ok := classify(p.TypesInfo, call, wrappers)
						if !ok {
							return true
						}
						forwards[call] = true
						w := target{msgIdx: -1, from: sig.Params().Len() - 1}
						if tg.msgIdx >= 0 && tg.msgIdx < len(call.Args) {
							if i := paramIndex(p.TypesInfo, fn, call.Args[tg.msgIdx]); i >= 0 {
								w = target{msgIdx: i, from: i + 1}
							}
						}
						if wrappers[fn] != w {
							wrappers[fn] = w
							changed = true
						}
						return true
					})
				}
			}
		}
	}
	return wrappers, forwards
}

func checkPackages(pkgs []*packages.Package) []violation {
	root, _ := filepath.Abs(moduleRoot)
	wrappers, forwards := findWrappers(pkgs)
	var out []violation
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			abs := p.Fset.Position(f.Pos()).Filename
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				rel = abs
			}
			rel = filepath.ToSlash(rel)
			at := func(n ast.Node, reason string) {
				pos := p.Fset.Position(n.Pos())
				out = append(out, violation{rel, pos.Line, pos.Column, reason})
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || forwards[call] {
					return true
				}
				tg, ok := classify(p.TypesInfo, call, wrappers)
				if !ok {
					return true
				}
				if tg.msgIdx >= 0 && len(call.Args) > tg.msgIdx && p.TypesInfo.Types[call.Args[tg.msgIdx]].Value == nil {
					at(call.Args[tg.msgIdx], "log message is not a constant")
				}
				for i := min(tg.from, len(call.Args)); i < len(call.Args); i++ {
					if call.Ellipsis.IsValid() && i == len(call.Args)-1 {
						at(call.Args[i], "spread values are not passed through logsafe")
						continue
					}
					checkValue(p.TypesInfo, call.Args[i], at)
				}
				return true
			})
		}
	}
	return out
}

// sortedUnique merges the findings of several loads: a file built under more
// than one set is counted once per site (line and column), so two values on
// one line still count twice.
func sortedUnique(in []violation) []violation {
	slices.SortFunc(in, func(a, b violation) int {
		return cmp.Or(strings.Compare(a.file, b.file), cmp.Compare(a.line, b.line), cmp.Compare(a.col, b.col), strings.Compare(a.reason, b.reason))
	})
	return slices.Compact(in)
}

// textMethod reports a String or Error method on t, which fmt calls in place
// of printing the number.
func textMethod(t types.Type) bool {
	ms := types.NewMethodSet(t)
	for _, name := range []string{"String", "Error"} {
		if sel := ms.Lookup(nil, name); sel != nil {
			sig, ok := sel.Type().(*types.Signature)
			if ok && sig.Params().Len() == 0 && sig.Results().Len() == 1 {
				return true
			}
		}
	}
	return false
}

// standardType reports a named type from the standard library, whose String
// method is fixed text (time.Duration).
func standardType(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	first, _, _ := strings.Cut(n.Obj().Pkg().Path(), "/")
	return !strings.Contains(first, ".")
}

func checkValue(info *types.Info, a ast.Expr, at func(ast.Node, string)) {
	tv := info.Types[a]
	if tv.Value != nil {
		return
	}
	if c, ok := ast.Unparen(a).(*ast.CallExpr); ok {
		obj := calleeObj(info, c)
		if isOwnFunc(obj, "Str", "Err") {
			return
		}
		if fn, ok := obj.(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == "log/slog" && slices.Contains(attrFuncs, fn.Name()) {
			for _, v := range c.Args[min(1, len(c.Args)):] {
				checkValue(info, v, at)
			}
			return
		}
	}
	if b, ok := types.Unalias(tv.Type).Underlying().(*types.Basic); ok && b.Info()&(types.IsNumeric|types.IsBoolean) != 0 {
		if textMethod(tv.Type) && !standardType(tv.Type) {
			at(a, "numeric log value of type "+tv.Type.String()+" has a String or Error method that can print any text")
		}
		return
	}
	at(a, "dynamic log value of type "+tv.Type.String()+" is not passed through logsafe")
}

var (
	treeOnce sync.Once
	treeViol []violation
)

func treeViolations(t *testing.T) []violation {
	t.Helper()
	treeOnce.Do(func() {
		var all []violation
		for _, set := range loadSets {
			all = append(all, checkPackages(loadPackages(t, set))...)
		}
		for _, c := range cgoSets {
			all = append(all, checkPackages([]*packages.Package{loadCgoPackage(t, c.name, c.dir, c.tags)})...)
		}
		treeViol = sortedUnique(all)
	})
	return treeViol
}

func TestRatchet_CheckerFlagsEachShapeAndPassesTheSafeOnes(t *testing.T) {
	pkgs := loadPackages(t, loadSet{name: "fixture", patterns: []string{"./internal/logsafe/testdata/fixture"}})
	src, err := os.ReadFile(filepath.Join("testdata", "fixture", "fixture.go"))
	if err != nil {
		t.Fatal(err)
	}
	var want []int
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasSuffix(line, "// want") {
			want = append(want, i+1)
		}
	}
	var got []int
	for _, v := range sortedUnique(checkPackages(pkgs)) {
		got = append(got, v.line)
	}
	got = slices.Compact(got)
	if len(want) < 10 {
		t.Fatalf("fixture marks %d flagged lines, want at least 10", len(want))
	}
	if !slices.Equal(got, want) {
		t.Errorf("flagged lines = %v, want %v", got, want)
	}
}

func TestRatchet_NoFileGainsARawLogValue(t *testing.T) {
	viol := treeViolations(t)
	counts := map[string]int{}
	byFile := map[string][]violation{}
	for _, v := range viol {
		counts[v.file]++
		byFile[v.file] = append(byFile[v.file], v)
	}
	if *updateBaseline {
		writeBaseline(t, counts)
		return
	}
	base := readBaseline(t)
	files := map[string]bool{}
	for f := range counts {
		files[f] = true
	}
	for f := range base {
		files[f] = true
	}
	for f := range files {
		switch {
		case counts[f] > base[f]:
			t.Errorf("%s holds %d raw log values, baseline %d; route each through logsafe (internal/logsafe) or use a constant message:", f, counts[f], base[f])
			for _, v := range byFile[f] {
				t.Errorf("  %s", v)
			}
		case counts[f] < base[f]:
			t.Errorf("%s holds %d raw log values, baseline %d: lower the baseline (go test ./internal/logsafe -run Ratchet -update-ratchet)", f, counts[f], base[f])
		}
	}
	if len(viol) == 0 && len(base) == 0 {
		t.Log("ratchet is at zero: the baseline file can be deleted")
	}
}

func TestRatchet_ScansTheTree(t *testing.T) {
	// Guards the ratchet against passing because it saw nothing: the tree
	// logs through log and slog in many files.
	calls := 0
	for _, p := range loadPackages(t, loadSet{name: "handler", patterns: []string{"./internal/handler"}}) {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if _, ok := classify(p.TypesInfo, c, nil); ok {
						calls++
					}
				}
				return true
			})
		}
	}
	if calls < 20 {
		t.Errorf("found %d logging calls in internal/handler, want at least 20", calls)
	}
}

func readBaseline(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-only file, nothing to flush
	out := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("%s: bad line %q", baselineFile, line)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 1 {
			t.Fatalf("%s: bad count in %q", baselineFile, line)
		}
		out[fields[0]] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeBaseline(t *testing.T, counts map[string]int) {
	t.Helper()
	files := make([]string, 0, len(counts))
	for f := range counts {
		files = append(files, f)
	}
	sort.Strings(files)
	var b strings.Builder
	b.WriteString("# Raw log values per file, counted by ratchet_test.go. A count may only fall.\n")
	b.WriteString("# Regenerate: go test ./internal/logsafe -run Ratchet -update-ratchet\n")
	for _, f := range files {
		fmt.Fprintf(&b, "%s %d\n", f, counts[f])
	}
	if err := os.WriteFile(baselineFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
