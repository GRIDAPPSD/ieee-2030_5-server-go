// Package extmodtest builds and runs a program as a separate Go module that
// requires this one, for tests proving an exported surface is usable from
// outside the module.
//
// Every package inside this module can import internal/, so no in-module test
// can show that a surface names no internal type. A real second module can:
// if any exported signature it uses leaks an internal type, it does not
// compile.
package extmodtest

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// The consumer's module path sits outside this module's on purpose: Go
	// enforces internal/ visibility by module path prefix, so a consumer under
	// github.com/GRIDAPPSD/ieee-2030_5-server-go/... could import internal/
	// legally and this check would pass while detecting nothing.
	consumerModulePath = "externalconsumer"
	serverModulePath   = "github.com/GRIDAPPSD/ieee-2030_5-server-go"
)

// Run builds source as the main package of a second module that requires this
// one, runs it, and returns its combined output. moduleRoot is this module's
// root directory. A build or run failure fails t.
func Run(t *testing.T, moduleRoot, source string) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), source)
	modMode := setUpConsumerModule(t, moduleRoot, dir)

	// -p 2 caps the nested build's parallelism. The suites around these tests
	// run under the race detector with several servers doing timed readiness
	// probes, and an uncapped cold compile saturates every core for long
	// enough to push those probes past their deadlines.
	cmd := exec.Command("go", "run", "-p", "2", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod="+modMode,
		// GOPROXY=off fails closed for github.com/GRIDAPPSD/* wherever no
		// ambient GOPRIVATE is inherited: true in CI since #335 dropped it
		// from ci.yml, false here since this host's GOENV sets one globally.
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "use of internal package") {
			t.Fatalf("the external consumer module failed to build: %v\n%s\n"+
				"An import naming an internal package means the exported surface leaks an internal type.",
				err, out)
		}
		t.Fatalf("the external consumer module failed to build or run (not a confirmed surface leak; "+
			"check module resolution and vendoring first): %v\n%s", err, out)
	}
	return string(out)
}

// setUpConsumerModule writes the throwaway module's go.mod and, when the nested
// build will run in vendor mode, a vendor tree of its own. It returns the -mod
// value that build has to run under.
func setUpConsumerModule(t *testing.T, moduleRoot, dir string) string {
	t.Helper()

	parentMod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	goVersion := goDirective(t, string(parentMod))

	vendorDir := filepath.Join(moduleRoot, "vendor")
	_, vendorStatErr := os.Stat(filepath.Join(vendorDir, "modules.txt"))

	// An ambient -mod=mod (set by core-freshness.yml's conformance job) means
	// go.mod has moved off the vendored tree; copying it anyway would fail the
	// nested build on vendoring, not on the surface this test measures.
	if vendorStatErr != nil || ambientModFlag(t) == "mod" {
		writeFile(t, filepath.Join(dir, "go.mod"),
			"module "+consumerModulePath+"\n\ngo "+goVersion+"\n\n"+
				"require "+serverModulePath+" v0.0.0\n\n"+
				"replace "+serverModulePath+" => "+moduleRoot+"\n")

		// Reuse this module's go.sum so the transitive hashes are already
		// present and current. Copying rather than pinning a second copy means
		// a core bump never leaves a stale checksum file behind to go red for
		// the wrong reason.
		sum, err := os.ReadFile(filepath.Join(moduleRoot, "go.sum"))
		if err != nil {
			t.Fatalf("read go.sum: %v", err)
		}
		writeFile(t, filepath.Join(dir, "go.sum"), string(sum))
		return "mod"
	}

	// A vendor-mode build never populates the module cache, and the consumer
	// lives outside this tree so it cannot see this module's vendor/ either.
	// Giving it a copy with this module vendored in under its real import path
	// leaves it needing neither. Carrying this module's requirements verbatim
	// is what keeps the copied modules.txt consistent with the consumer's
	// go.mod, which vendor mode checks.
	writeFile(t, filepath.Join(dir, "go.mod"),
		rewriteModuleLine(string(parentMod), consumerModulePath)+
			"\nrequire "+serverModulePath+" v0.0.0\n")

	dstVendor := filepath.Join(dir, "vendor")
	if err := os.CopyFS(dstVendor, os.DirFS(vendorDir)); err != nil {
		t.Fatalf("copy vendor tree: %v", err)
	}
	pkgs := copyModuleSource(t, moduleRoot, filepath.Join(dstVendor, filepath.FromSlash(serverModulePath)))
	appendVendoredModule(t, filepath.Join(dstVendor, "modules.txt"), goVersion, pkgs)
	return "vendor"
}

// ambientModFlag reports the -mod value the surrounding environment imposes on
// this module's builds, or "" when it imposes none. `go env GOFLAGS` is read
// rather than the GOFLAGS variable directly, because a `go env -w` default sets
// the mode just as effectively and os.Getenv would not see it.
func ambientModFlag(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "env", "GOFLAGS").Output()
	if err != nil {
		t.Fatalf("read GOFLAGS via go env: %v", err)
	}
	var mode string
	for _, field := range strings.Fields(string(out)) {
		if m, ok := strings.CutPrefix(field, "-mod="); ok {
			mode = m
		}
	}
	return mode
}

// copyModuleSource copies this module into dst as a vendor entry and returns
// the import paths of the packages it copied.
func copyModuleSource(t *testing.T, src, dst string) []string {
	t.Helper()

	pkgDirs := map[string]struct{}{}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A vendor entry carries no nested vendor tree and no VCS or
			// tooling directories.
			if rel != "." && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// go mod vendor omits go.mod, go.sum and test files from a vendored
		// module; a nested go.mod in particular would cut the copied packages
		// out of this module.
		name := d.Name()
		if name == "go.mod" || name == "go.sum" || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if strings.HasSuffix(name, ".go") {
			pkgDirs[filepath.ToSlash(filepath.Dir(rel))] = struct{}{}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), content, 0o600)
	})
	if err != nil {
		t.Fatalf("vendor this module into the consumer: %v", err)
	}

	pkgs := make([]string, 0, len(pkgDirs))
	for d := range pkgDirs {
		if d == "." {
			pkgs = append(pkgs, serverModulePath)
			continue
		}
		pkgs = append(pkgs, serverModulePath+"/"+d)
	}
	slices.Sort(pkgs)
	return pkgs
}

// appendVendoredModule records this module in the consumer's modules.txt.
// Vendor mode resolves imports through that file, so a package missing from it
// is a package the consumer cannot import.
func appendVendoredModule(t *testing.T, path, goVersion string, pkgs []string) {
	t.Helper()

	var entry strings.Builder
	entry.WriteString("# " + serverModulePath + " v0.0.0\n")
	entry.WriteString("## explicit; go " + goVersion + "\n")
	for _, p := range pkgs {
		entry.WriteString(p + "\n")
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read modules.txt: %v", err)
	}
	writeFile(t, path, string(existing)+entry.String())
}

func goDirective(t *testing.T, goMod string) string {
	t.Helper()
	for line := range strings.Lines(goMod) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("no go directive in go.mod")
	return ""
}

func rewriteModuleLine(goMod, path string) string {
	var out strings.Builder
	for line := range strings.Lines(goMod) {
		if strings.HasPrefix(line, "module ") {
			out.WriteString("module " + path + "\n")
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
