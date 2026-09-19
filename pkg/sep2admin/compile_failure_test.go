package sep2admin_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unexportedFieldPhrase is the phrase this Go toolchain prints ONLY when a
// composite literal from outside the declaring package names a field that
// is genuinely unexported: `cannot refer to unexported field <name> in
// struct literal of type <pkg>.<Type>`. It is NOT the phrase to check for
// a field that used to be unexported and was exported by mistake: that
// state prints `unknown field <name> in struct literal ..., but does have
// <Name>`, which still contains the field's lowercase name as a substring
// (proven by mutation, #368 fix round 1), so a substring check for the
// field's own name passes in both the sealed and the broken state and
// cannot tell them apart. Checking for this phrase instead can: it
// appears only when the field truly cannot be named from here.
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

// TestGraftCannotSetBodyKind is the compile-failure proof that Body.kind
// is unreachable from outside this package: the same mechanism as
// TestGraftCannotNameTheCoreBand, applied to Body's own seal (#368 fix
// round 1, item 1). testdata's graftcannotsetbodykind program textually
// attempts to set kind directly. See unexportedFieldPhrase for why the
// assertion is the phrase, not the field's own name.
func TestGraftCannotSetBodyKind(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotsetbodykind"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotsetbodykind compiled; Body.kind is reachable from outside package sep2admin:\n%s", out)
	}
	if !strings.Contains(string(out), unexportedFieldPhrase) {
		t.Fatalf("build failed for a reason unrelated to the unexported field (want %q): %v\n%s", unexportedFieldPhrase, err, out)
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
