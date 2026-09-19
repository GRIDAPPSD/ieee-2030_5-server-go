package sep2admin_test

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// unexportedFieldPhrase is the compiler message printed ONLY when a field
// named in a composite literal from outside this package is genuinely
// unexported. Checking for the field's own name instead is a false-green
// trap: if the field is later exported by mistake, the near-miss message
// still contains that name as a substring (proven by mutation, #368 fix
// round 1), so a same-name check stays green through the break. This
// phrase does not.
const unexportedFieldPhrase = "cannot refer to unexported field"

// TestGraftCannotNameTheCoreBand is the compile-failure proof for
// criterion 3: Placement.group is unexported, so ExtensionSlot is the only
// way to obtain a Placement from outside this package. testdata's
// graftcannotsetgroup program textually attempts to set group directly.
// Proven by mutation (#368 fix round 1): exporting group as Group leaves
// the substring "group" in the compiler's near-miss hint ("but does have
// Group"), so a check for that substring alone stays green when the seal
// is broken. unexportedFieldPhrase does not: it is absent from the
// near-miss hint and present only when group is genuinely unreachable.
func TestGraftCannotNameTheCoreBand(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotsetgroup"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotsetgroup compiled; Placement.group is reachable from outside package sep2admin:\n%s", out)
	}
	if !strings.Contains(string(out), unexportedFieldPhrase) {
		t.Fatalf("build failed for a reason unrelated to the unexported field (want %q): %v\n%s", unexportedFieldPhrase, err, out)
	}
}

// TestGraftCannotSetAnyBodyField is the compile-failure proof that all
// three of Body's unexported fields are sealed, not only kind (#368 fix
// round 2, item 2): the guarantee at descriptor.go's doc comment, "no
// second field to set," rests on all three, and a seal test pinning one
// leaves the other two open to exactly the smuggling round 2 found by
// mutation. The table means a fourth field added to Body later is
// covered by one new case and one new testdata program, not by someone
// remembering to write a fourth near-identical test function, and the
// NumField guard below (#368 fix round 3, item 2) fails loudly if that
// field arrives without its case, so the table is iterated against
// Body's real shape rather than only enumerated. See
// unexportedFieldPhrase for why the assertion is the phrase, not the
// field's own name.
func TestGraftCannotSetAnyBodyField(t *testing.T) {
	cases := []struct {
		field string
		dir   string
	}{
		{field: "kind", dir: "graftcannotsetbodykind"},
		{field: "table", dir: "graftcannotsettable"},
		{field: "definitionList", dir: "graftcannotsetdefinitionlist"},
	}

	if got, want := reflect.TypeOf(sep2admin.Body{}).NumField(), len(cases); got != want {
		t.Fatalf("Body has %d fields but this table covers %d; add a testdata program and a case to TestGraftCannotSetAnyBodyField", got, want)
	}

	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			dir, err := filepath.Abs(filepath.Join("testdata", c.dir))
			if err != nil {
				t.Fatalf("resolve testdata dir: %v", err)
			}

			cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s compiled; Body.%s is reachable from outside package sep2admin:\n%s", c.dir, c.field, out)
			}
			if !strings.Contains(string(out), unexportedFieldPhrase) {
				t.Fatalf("build failed for a reason unrelated to the unexported field (want %q): %v\n%s", unexportedFieldPhrase, err, out)
			}
		})
	}
}

// TestEmbeddingCannotSmuggleAFieldIntoBody is the compile-failure proof
// for the finding that broke Body's previous seal: a type in another
// package that embeds the exported TableBody used to get bodyKind()
// promoted, satisfying the old Body interface while carrying a field the
// schema never declared. NewTableBody now takes a TableBody by value, and
// an embedding type is not itself a TableBody, so passing one is a type
// mismatch caught at compile time. testdata's graftcannotembedintobody
// program is exactly that reproduction.
func TestEmbeddingCannotSmuggleAFieldIntoBody(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotembedintobody"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotembedintobody compiled; a type embedding TableBody can smuggle a field into Body again:\n%s", out)
	}
	const want = "as sep2admin.TableBody value in argument to sep2admin.NewTableBody"
	if !strings.Contains(string(out), want) {
		t.Fatalf("build failed for a reason unrelated to the TableBody argument type (want %q): %v\n%s", want, err, out)
	}
}

// TestBodyConstructorsRejectPointers is the compile-failure proof for
// #368 item 3: under the previous design, both TableBody and *TableBody
// implemented Body (bodyKind had a value receiver), and a nil *TableBody
// assigned to Body panicked json.Marshal. NewTableBody takes a TableBody
// by value, so there is no longer any way to construct a Body from a
// pointer: the class of bug is structurally closed, not merely undefended.
func TestBodyConstructorsRejectPointers(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotpassbodypointer"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotpassbodypointer compiled; a *TableBody can reach Descriptor.Body again:\n%s", out)
	}
	const wantPtr = "as sep2admin.TableBody value in argument to sep2admin.NewTableBody"
	if !strings.Contains(string(out), wantPtr) {
		t.Fatalf("build failed for a reason unrelated to the TableBody argument type (want %q): %v\n%s", wantPtr, err, out)
	}
}
