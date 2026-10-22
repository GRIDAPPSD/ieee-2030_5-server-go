package devicelog

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func evalDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPrepareDirCreatesAndResolves(t *testing.T) {
	base := evalDir(t, t.TempDir())
	want := filepath.Join(base, "a", "b")
	got, err := PrepareDir(want, nil)
	if err != nil {
		t.Fatalf("PrepareDir: %v", err)
	}
	if got != want {
		t.Errorf("resolved = %q; want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("directory not created: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(want, ".*")); len(left) != 0 {
		t.Errorf("probe file left behind: %v", left)
	}
}

func TestPrepareDirResolvesSymlinkedRoot(t *testing.T) {
	base := evalDir(t, t.TempDir())
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareDir(link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Errorf("resolved = %q; want the link target %q", got, real)
	}
}

func TestPrepareDirUnusable(t *testing.T) {
	base := evalDir(t, t.TempDir())
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"path is a file":   file,
		"parent is a file": filepath.Join(file, "sub"),
		"empty":            "",
		"nul in path":      filepath.Join(base, "a\x00b"),
	} {
		if _, err := PrepareDir(dir, nil); !errors.Is(err, ErrDirUnusable) {
			t.Errorf("%s: err = %v; want ErrDirUnusable", name, err)
		}
	}
}

func TestPrepareDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	base := evalDir(t, t.TempDir())
	ro := filepath.Join(base, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, err := PrepareDir(ro, nil); !errors.Is(err, ErrDirUnusable) {
		t.Errorf("err = %v; want ErrDirUnusable", err)
	}
}

func TestPrepareDirRefusesProtectedPaths(t *testing.T) {
	base := evalDir(t, t.TempDir())
	certs := filepath.Join(base, "tls")
	if err := os.Mkdir(certs, 0o700); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(base, "alias")
	if err := os.Symlink(certs, viaLink); err != nil {
		t.Fatal(err)
	}
	notYet := filepath.Join(base, "future-tls")

	refused := map[string]string{
		"equal":                certs,
		"under":                filepath.Join(certs, "logs"),
		"deep under":           filepath.Join(certs, "a", "b", "logs"),
		"dot-dot back in":      filepath.Join(base, "x", "..", "tls", "logs"),
		"symlink into":         filepath.Join(viaLink, "logs"),
		"symlink itself":       viaLink,
		"under a missing path": filepath.Join(notYet, "logs"),
	}
	for name, dir := range refused {
		_, err := PrepareDir(dir, []string{certs, notYet})
		if !errors.Is(err, ErrDirProtected) {
			t.Errorf("%s (%s): err = %v; want ErrDirProtected", name, dir, err)
		}
	}
	for _, p := range []string{filepath.Join(certs, "logs"), filepath.Join(certs, "a"), filepath.Join(notYet, "logs")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was created before the refusal", p)
		}
	}

	if _, err := PrepareDir(filepath.Join(base, "tls-logs"), []string{certs}); err != nil {
		t.Errorf("sibling sharing a name prefix refused: %v", err)
	}
	if _, err := PrepareDir(filepath.Join(base, "logs"), []string{"", certs}); err != nil {
		t.Errorf("unrelated dir refused: %v", err)
	}
}

func TestGitignoreShipsFolderAndIgnoresContents(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not inside a git work tree: %v", err)
	}
	root := strings.TrimSpace(string(top))
	cases := []struct {
		path    string
		ignored bool
	}{
		{"etc/sep2/logs/.gitkeep", false},
		{"etc/sep2/logs/" + lfdiA + ".jsonl", true},
		{"etc/sep2/logs/" + lfdiA + ".1.jsonl", true},
		{"etc/sep2/logs/stray.txt", true},
		{"etc/sep2/other/.gitkeep", false},
	}
	for _, c := range cases {
		err := exec.Command("git", "-C", root, "check-ignore", "-q", "--no-index", c.path).Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			if !c.ignored {
				t.Errorf("%s is ignored; want tracked", c.path)
			}
		case errors.As(err, &ee) && ee.ExitCode() == 1:
			if c.ignored {
				t.Errorf("%s is not ignored; want ignored", c.path)
			}
		default:
			t.Fatalf("git check-ignore %s: %v", c.path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc/sep2/logs/.gitkeep")); err != nil {
		t.Errorf("the shipped folder marker is missing: %v", err)
	}
}

func TestPrepareDirWritableByOthers(t *testing.T) {
	base := evalDir(t, t.TempDir())
	world := filepath.Join(base, "world")
	group := filepath.Join(base, "group")
	for _, d := range []string{world, group} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(world, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(group, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDir(world, nil); !errors.Is(err, ErrDirUnusable) {
		t.Errorf("world-writable: err = %v; want ErrDirUnusable", err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if _, err := PrepareDir(group, nil); err != nil {
		t.Errorf("group-writable refused: %v", err)
	}
	if !strings.Contains(logs.String(), "group-writable") {
		t.Errorf("log %q has no group-writable warning", logs.String())
	}
}
