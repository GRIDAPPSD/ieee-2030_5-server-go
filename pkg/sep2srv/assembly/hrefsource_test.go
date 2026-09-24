package assembly_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Registry completeness.
//
// assembly.MintableHrefs is a declared registry, and a declared registry has
// one failure mode that matters: somebody adds a handler, mints an href, and
// does not declare it. That is precisely how the advertised-but-unrouted defect
// class grew past the list that was supposed to track it, so the registry
// cannot be the only line of defence.
//
// This file closes that hole by reading the source. Every string literal in
// pkg/sep2srv that folds to an href shape has to appear in the registry.
// Handlers build hrefs three ways and all three fold:
//
//	fmt.Sprintf("/edev/%s/frq/%s", edevID, frqID) -> /edev/{}/frq/{}
//	"/edev/" + id                                -> /edev/{}
//	"/edev/" + r.PathValue("id") + "/dstat"       -> /edev/{}/dstat
//
// # What this does not see
//
// A concatenation rooted in a VALUE rather than a literal, such as
// upt.Href + "/mr", cannot be folded here: the leading segments are not in the
// expression. Those are not silently dropped. They are collected as unresolved
// fragments and have to be acknowledged in valueRootedHrefs below with the
// shape they resolve to, so a new one fails this test until a human says what
// it produces.
//
// Two residual gaps are real and worth stating rather than implying away.
// First, an href assembled across function boundaries (built in one function,
// returned, and prefixed in another) folds to a fragment in each half and may
// match neither rule. Second, the scan only treats a literal as an href when
// its first segment is a family the router or the registry already knows, so a
// handler that invents a brand-new top-level family AND leaves it out of the
// registry AND leaves it unrouted escapes. Both are narrower than the hole they
// replace, which was "a hand-maintained list of instances".

// fmtVerb matches a printf verb so a format string can be folded to a shape.
var fmtVerb = regexp.MustCompile(`%[#+\-0-9.*]*[a-zA-Z]`)

// TestMintableHrefs_RegistryCoversEverySourceLiteral fails when the source
// mints an href shape the registry does not declare.
//
// It is the completeness half of the mechanism: assembly.AssertMintableHrefs
// proves every DECLARED shape routes, and this proves every MINTED shape is
// declared. Neither is sufficient alone. Together they say that every href
// shape the source can construct resolves against the router, which is the
// property the defect class violates.
func TestMintableHrefs_RegistryCoversEverySourceLiteral(t *testing.T) {
	t.Parallel()

	declared := make(map[string]bool)
	for _, h := range assembly.MintableHrefs() {
		declared[h.Shape] = true
	}

	found, fragments := scanHrefLiterals(t, "..", hrefFamilies(t))

	shapes := make([]string, 0, len(found))
	for shape := range found {
		shapes = append(shapes, shape)
	}
	sort.Strings(shapes)
	t.Logf("source scan found %d href shape(s) and %d value-rooted fragment(s)", len(shapes), len(fragments))

	for _, shape := range shapes {
		if declared[shape] {
			continue
		}
		t.Errorf("source mints href shape %q at %s but assembly.MintableHrefs does not declare it.\n"+
			"Declare it, so the boot-time assertion can prove it routes.",
			shape, strings.Join(found[shape], ", "))
	}

	// Value-rooted fragments have to be acknowledged, and what they were
	// acknowledged AS has to itself be declared, otherwise the
	// acknowledgement is a way to launder a shape past the registry.
	for frag, positions := range fragments {
		resolved, ok := valueRootedHrefs[frag]
		if !ok {
			t.Errorf("source builds href fragment %q at %s from a value rather than a literal.\n"+
				"Add it to valueRootedHrefs naming the shape it resolves to, so it is covered by the registry.",
				frag, strings.Join(positions, ", "))
			continue
		}
		if !declared[resolved] {
			t.Errorf("fragment %q is acknowledged as resolving to %q, which assembly.MintableHrefs does not declare",
				frag, resolved)
		}
	}

	// Guard the acknowledgement list against rot in the other direction: an
	// entry for a fragment the source no longer builds is a stale exemption
	// that would silently cover a future fragment of the same text.
	for frag := range valueRootedHrefs {
		if _, ok := fragments[frag]; !ok {
			t.Errorf("valueRootedHrefs has %q but the source no longer builds it; delete the entry", frag)
		}
	}
}

// valueRootedHrefs acknowledges each href built by appending to a value rather
// than to a literal, naming the shape it resolves to. The scan cannot fold
// these, so each one is a human assertion, and each has to name a shape the
// registry declares.
var valueRootedHrefs = map[string]string{
	// metering.go: upt.MeterReadingListLink = &sep2.ListLink{Href: upt.Href + "/mr"}
	// upt.Href is UsagePointHref(id), which is "/upt/" + id.
	"{}/mr": "/upt/{}/mr",

	// der/links.go: FillAbsentDERLinks derives each DER sub-resource link from
	// base, the DER's own canonical href, which is "/edev/{id}/der/{derId}".
	// These four are what make the acknowledgement rule bite on that function: a
	// fifth suffix added there without a route mounted for it fails this test,
	// which is the coupling section 4.4 requires between mounting a function set
	// and advertising it.
	"{}/dera":   "/edev/{}/der/{}/dera",
	"{}/dercap": "/edev/{}/der/{}/dercap",
	"{}/derg":   "/edev/{}/der/{}/derg",
	"{}/ders":   "/edev/{}/der/{}/ders",

	// enddevice.go: stampFlowReservationLinks derives both list hrefs from
	// dev.Href, the EndDevice's own canonical href, which is "/edev/{id}".
	// Applied on every serve so a device seeded before these links existed
	// still gets them, not only a device created after (#693).
	"{}/frq": "/edev/{}/frq",
	"{}/frp": "/edev/{}/frp",
}

// hrefFamilies returns the set of first path segments that count as an href
// family, taken from the router's own patterns unioned with the registry.
//
// Deriving it rather than hardcoding it means a family added to the router is
// in scope for this scan without anybody remembering to widen a list here.
func hrefFamilies(t *testing.T) map[string]bool {
	t.Helper()

	fam := make(map[string]bool)
	add := func(path string) {
		if seg := firstSegment(path); seg != "" {
			fam[seg] = true
		}
	}
	for _, p := range fullyWiredPatterns(t) {
		// Patterns are "METHOD /path"; take the path half.
		if i := strings.IndexByte(p, '/'); i >= 0 {
			add(p[i:])
		}
	}
	for _, h := range assembly.MintableHrefs() {
		add(h.Shape)
	}
	if len(fam) == 0 {
		t.Fatal("no href families derived; the scan would find nothing and pass vacuously")
	}
	return fam
}

func firstSegment(path string) string {
	path = strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path = path[:i]
	}
	return path
}

// scanHrefLiterals parses every non-test Go file under root and folds string
// expressions into href shapes.
//
// It returns the shapes it resolved (shape -> source positions) and the
// value-rooted fragments it could not resolve (fragment -> source positions).
func scanHrefLiterals(t *testing.T, root string, families map[string]bool) (map[string][]string, map[string][]string) {
	t.Helper()

	shapes := make(map[string][]string)
	fragments := make(map[string][]string)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parsing %s: %w", path, perr)
		}

		// Some literals are folded by an enclosing expression and must not
		// also be considered on their own, or the scan reports a phantom
		// shape that no code ever puts on the wire:
		//
		//   - an operand of "+", because "/edev/" alone is a prefix;
		//   - a fmt.Sprintf format string, because on its own it still
		//     carries "%s" rather than a wildcard.
		//
		// The Sprintf case is not cosmetic. Left in, it reports
		// "/edev/%s/frq/%s" as an undeclared shape alongside the declared
		// "/edev/{}/frq/{}", and a reader would sooner widen the registry to
		// silence it than notice the two are the same href.
		foldedByParent := make(map[ast.Expr]bool)
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op == token.ADD {
					foldedByParent[x.X] = true
					foldedByParent[x.Y] = true
				}
			case *ast.CallExpr:
				if isSprintf(x.Fun) && len(x.Args) > 0 {
					foldedByParent[x.Args[0]] = true
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok || foldedByParent[e] {
				return true
			}
			switch e.(type) {
			case *ast.BasicLit, *ast.BinaryExpr, *ast.CallExpr:
			default:
				return true
			}

			tmpl, lit := foldTemplate(e)
			if !lit {
				return true
			}
			pos := fset.Position(e.Pos()).String()
			switch {
			case isHrefShape(tmpl, families):
				shapes[tmpl] = appendUnique(shapes[tmpl], pos)
			case isValueRootedFragment(e, tmpl):
				fragments[tmpl] = appendUnique(fragments[tmpl], pos)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	return shapes, fragments
}

// foldTemplate reduces a string expression to a shape, substituting
// assembly.HrefWildcard for every part whose value is not known statically. The
// second result reports whether any string literal contributed, which is what
// separates "an href built from literals" from "some expression".
func foldTemplate(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return assembly.HrefWildcard, false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return assembly.HrefWildcard, false
		}
		return v, true

	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return assembly.HrefWildcard, false
		}
		l, lLit := foldTemplate(x.X)
		r, rLit := foldTemplate(x.Y)
		return collapse(l + r), lLit || rLit

	case *ast.CallExpr:
		// fmt.Sprintf is the dominant minting form; its format string is
		// the shape once the verbs are wildcarded.
		if !isSprintf(x.Fun) || len(x.Args) == 0 {
			return assembly.HrefWildcard, false
		}
		lit, ok := x.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return assembly.HrefWildcard, false
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return assembly.HrefWildcard, false
		}
		return collapse(fmtVerb.ReplaceAllString(v, assembly.HrefWildcard)), true

	default:
		return assembly.HrefWildcard, false
	}
}

func isSprintf(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sprintf" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "fmt"
}

// collapse folds adjacent wildcards into one. "/edev/" + a + b describes the
// same shape as "/edev/" + a, since neither says how many segments the values
// span, and leaving them adjacent would produce a shape no probe could match.
func collapse(s string) string {
	dbl := assembly.HrefWildcard + assembly.HrefWildcard
	for strings.Contains(s, dbl) {
		s = strings.ReplaceAll(s, dbl, assembly.HrefWildcard)
	}
	return s
}

// isHrefShape reports whether a folded template is a complete href: absolute,
// in a known family, and not a bare prefix.
func isHrefShape(tmpl string, families map[string]bool) bool {
	if !strings.HasPrefix(tmpl, "/") || strings.HasSuffix(tmpl, "/") {
		return false
	}
	if strings.HasPrefix(tmpl, "/"+assembly.HrefWildcard) {
		return false
	}
	return families[firstSegment(tmpl)]
}

// isValueRootedFragment reports whether an expression looks like an href built
// by appending to a value: the fold is not absolute, but a real path literal
// took part. A bare "/" separator does not count, or every composite store key
// in the package would be reported.
func isValueRootedFragment(e ast.Expr, tmpl string) bool {
	if strings.HasPrefix(tmpl, "/") || !strings.Contains(tmpl, "/") {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.HasPrefix(v, "/") && strings.Trim(v, "/") != "" {
			found = true
		}
		return true
	})
	return found
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// TestFoldTemplate covers the folding rules directly, because the scan above
// only proves the registry and the source agree TODAY. If folding silently
// stopped recognising a minting form, the scan would find fewer shapes and
// still pass, which is the failure mode a completeness check must not have.
func TestFoldTemplate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		expr     string
		want     string
		wantLit  bool
		wantHref bool
	}{
		{
			name:     "sprintf with two verbs",
			expr:     `fmt.Sprintf("/edev/%s/frq/%s", edevID, frqID)`,
			want:     "/edev/{}/frq/{}",
			wantLit:  true,
			wantHref: true,
		},
		{
			name:     "sprintf with a width flag",
			expr:     `fmt.Sprintf("/edev/%04d/log/%s", n, id)`,
			want:     "/edev/{}/log/{}",
			wantLit:  true,
			wantHref: true,
		},
		{
			name:     "literal prefix plus a value",
			expr:     `"/edev/" + id`,
			want:     "/edev/{}",
			wantLit:  true,
			wantHref: true,
		},
		{
			name:     "value between two literals",
			expr:     `"/edev/" + r.PathValue("id") + "/dstat"`,
			want:     "/edev/{}/dstat",
			wantLit:  true,
			wantHref: true,
		},
		{
			name:     "adjacent values collapse to one wildcard",
			expr:     `"/edev/" + a + b`,
			want:     "/edev/{}",
			wantLit:  true,
			wantHref: true,
		},
		{
			name:     "bare literal href",
			expr:     `"/sdev/sdi"`,
			want:     "/sdev/sdi",
			wantLit:  true,
			wantHref: true,
		},
		{
			name: "trailing slash is a prefix, not an href",
			expr: `"/edev/"`,
			want: "/edev/",
		},
		{
			name: "value-rooted concatenation does not resolve",
			expr: `upt.Href + "/mr"`,
			want: "{}/mr",
		},
		{
			name: "composite store key is not an href",
			expr: `r.PathValue("uptId") + "/" + r.PathValue("mrId")`,
			want: "{}/{}",
		},
		{
			name: "a call that is not Sprintf yields no literal",
			expr: `strings.ToUpper(x)`,
			want: assembly.HrefWildcard,
		},
		{
			name: "a non-string literal yields no literal",
			expr: `42`,
			want: assembly.HrefWildcard,
		},
	}

	families := hrefFamilies(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e, err := parser.ParseExpr(tc.expr)
			if err != nil {
				t.Fatalf("parsing %q: %v", tc.expr, err)
			}
			got, lit := foldTemplate(e)
			if got != tc.want {
				t.Errorf("fold(%q) = %q, want %q", tc.expr, got, tc.want)
			}
			if tc.wantLit && !lit {
				t.Errorf("fold(%q) reported no literal contribution", tc.expr)
			}
			if isHrefShape(got, families) != tc.wantHref {
				t.Errorf("isHrefShape(%q) = %v, want %v", got, !tc.wantHref, tc.wantHref)
			}
		})
	}
}

// TestIsValueRootedFragment_SeparatorIsNotAnHref keeps the acknowledgement rule
// from swallowing every composite store key in the package. A bare "/" is a
// separator; "/mr" is a path.
func TestIsValueRootedFragment_SeparatorIsNotAnHref(t *testing.T) {
	t.Parallel()

	cases := []struct {
		expr string
		want bool
	}{
		{`upt.Href + "/mr"`, true},
		{`r.PathValue("uptId") + "/" + r.PathValue("mrId")`, false},
		{`"/upt/" + id`, false},
	}
	for _, tc := range cases {
		e, err := parser.ParseExpr(tc.expr)
		if err != nil {
			t.Fatalf("parsing %q: %v", tc.expr, err)
		}
		tmpl, _ := foldTemplate(e)
		if got := isValueRootedFragment(e, tmpl); got != tc.want {
			t.Errorf("isValueRootedFragment(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}
