package logsafe

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
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

var ownPkgPath = reflect.TypeOf(Fields{}).PkgPath()

type violation struct {
	file   string
	line   int
	reason string
}

func (v violation) String() string { return fmt.Sprintf("%s:%d: %s", v.file, v.line, v.reason) }

// logFuncs maps a standard-library logging function or method name to the
// index of its message argument, or -1 when every argument is a value.
var logFuncs = map[string]map[string]int{
	"log": {
		"Printf": 0, "Fatalf": 0, "Panicf": 0,
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

func loadPackages(t *testing.T, patterns ...string) []*packages.Package {
	t.Helper()
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: root,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("loading %v: %v", patterns, err)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("package %s does not type-check: %v", p.PkgPath, p.Errors[0])
		}
	}
	return pkgs
}

func calleeFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	default:
		return nil
	}
	f, _ := info.Uses[id].(*types.Func)
	return f
}

func isOwnFunc(f *types.Func, names ...string) bool {
	return f != nil && f.Pkg() != nil && f.Pkg().Path() == ownPkgPath && slices.Contains(names, f.Name())
}

func checkPackages(pkgs []*packages.Package) []violation {
	root, _ := filepath.Abs(moduleRoot)
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
				out = append(out, violation{rel, p.Fset.Position(n.Pos()).Line, reason})
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn := calleeFunc(p.TypesInfo, call)
				if fn == nil || fn.Pkg() == nil {
					return true
				}
				msgIdx, ok := logFuncs[fn.Pkg().Path()][fn.Name()]
				if !ok {
					return true
				}
				rest := call.Args
				if msgIdx >= 0 && len(call.Args) > msgIdx {
					if p.TypesInfo.Types[call.Args[msgIdx]].Value == nil {
						at(call.Args[msgIdx], "log message is not a constant")
					}
					rest = call.Args[msgIdx+1:]
				}
				for i, a := range rest {
					if call.Ellipsis.IsValid() && i == len(rest)-1 {
						if c, ok := ast.Unparen(a).(*ast.CallExpr); !ok || !isOwnFunc(calleeFunc(p.TypesInfo, c), "Args") {
							at(a, "spread is not logsafe.Fields.Args()")
						}
						continue
					}
					checkValue(p.TypesInfo, a, at)
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out
}

func checkValue(info *types.Info, a ast.Expr, at func(ast.Node, string)) {
	tv := info.Types[a]
	if tv.Value != nil {
		return
	}
	if c, ok := ast.Unparen(a).(*ast.CallExpr); ok {
		fn := calleeFunc(info, c)
		if isOwnFunc(fn, "Str", "Err") {
			return
		}
		if fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "log/slog" && slices.Contains(attrFuncs, fn.Name()) {
			for _, v := range c.Args[min(1, len(c.Args)):] {
				checkValue(info, v, at)
			}
			return
		}
	}
	if b, ok := types.Unalias(tv.Type).Underlying().(*types.Basic); ok && b.Info()&(types.IsNumeric|types.IsBoolean) != 0 {
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
	treeOnce.Do(func() { treeViol = checkPackages(loadPackages(t, "./internal/...", "./pkg/...")) })
	return treeViol
}

func TestRatchet_CheckerFlagsEachShapeAndPassesTheSafeOnes(t *testing.T) {
	pkgs := loadPackages(t, "./internal/logsafe/testdata/fixture")
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
	for _, v := range checkPackages(pkgs) {
		got = append(got, v.line)
	}
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
	for _, p := range loadPackages(t, "./internal/handler") {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if fn := calleeFunc(p.TypesInfo, c); fn != nil && fn.Pkg() != nil {
						if _, ok := logFuncs[fn.Pkg().Path()][fn.Name()]; ok {
							calls++
						}
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
