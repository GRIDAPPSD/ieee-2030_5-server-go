package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGitignoreRootCertsAnchored asks git itself, in a throwaway repo
// holding a copy of .gitignore, which paths are ignored. The root certs/
// folder must be ignored while internal/certs/, a source package, must not
// be, and the key and fixture rules must keep their meaning.
func TestGitignoreRootCertsAnchored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), raw, 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	cases := []struct {
		path    string
		ignored bool
	}{
		{"certs/x.crt", true},
		{"internal/certs/new_test.go", false},
		{"internal/certs/x.key", true},
		{"testdata/csip-pki/root_ca.pem", false},
	}
	for _, c := range cases {
		// check-ignore exits 0 when ignored, 1 when not, 128 on error.
		err := exec.Command("git", "-C", repo, "check-ignore", "-q", "--no-index", c.path).Run()
		got := err == nil
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 1 {
			t.Fatalf("git check-ignore %s: exit %d", c.path, ee.ExitCode())
		} else if err != nil && !ok {
			t.Fatalf("git check-ignore %s: %v", c.path, err)
		}
		if got != c.ignored {
			t.Errorf("ignored(%s) = %v, want %v", c.path, got, c.ignored)
		}
	}
}
