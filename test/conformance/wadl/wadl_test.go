package wadl_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
)

// These tests exercise the locator itself and deliberately never need a real
// copy of sep_wadl.xml. They are the part of the WADL story that must stay
// green for a contributor who has no IEEE copy at all.

// emptyModule returns a directory that looks like a module root but holds no
// WADL, and makes it the working directory.
func emptyModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// TestResolveDistinguishesAbsentFromMisconfigured is the load-bearing
// behaviour of the locator, and it is the reason SEP2_WADL_PATH is not just a
// path variable. Absence is a skip, so it must be reported as ErrNotFound; a
// path that was explicitly configured and does not resolve is an operator
// error, so it must NOT be, or a broken CI secret would masquerade as "no WADL
// here" and skip the gate while reporting green.
func TestResolveDistinguishesAbsentFromMisconfigured(t *testing.T) {
	dir := emptyModule(t)

	tests := []struct {
		name        string
		path        string
		wantMissing bool // errors.Is(err, ErrNotFound)
	}{
		{
			name:        "unset falls through to the default path and finds nothing",
			path:        "",
			wantMissing: true,
		},
		{
			name:        "configured path that does not exist",
			path:        filepath.Join(dir, "no-such-file.xml"),
			wantMissing: false,
		},
		{
			name:        "configured path that is a directory",
			path:        dir,
			wantMissing: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(wadl.EnvPath, tc.path)

			got, err := wadl.Resolve()
			if err == nil {
				t.Fatalf("Resolve() = %q, want an error", got)
			}
			if missing := errors.Is(err, wadl.ErrNotFound); missing != tc.wantMissing {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v; err = %v", missing, tc.wantMissing, err)
			}
		})
	}
}

// TestResolveDefaultPath asserts the conventional in-repo location resolves
// relative to the module root, not to the test's own package directory. Tests
// run with the working directory set to their package, so a plain relative
// path would only work for tests in the root.
func TestResolveDefaultPath(t *testing.T) {
	dir := emptyModule(t)
	t.Setenv(wadl.EnvPath, "")

	want := filepath.Join(dir, wadl.DefaultPath)
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("<application/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nested deeper than the root, the way a package's tests run.
	sub := filepath.Join(dir, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	got, err := wadl.Resolve()
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	if got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

// TestLoadRejectsWrongDocument proves the integrity check has teeth: a file
// that is not the pinned WADL fails with a message naming what was found,
// rather than parsing and sweeping against requirements the standard never
// stated.
func TestLoadRejectsWrongDocument(t *testing.T) {
	emptyModule(t)

	path := filepath.Join(t.TempDir(), "wrong.xml")
	body := []byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\r\n<application/>\r\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wadl.EnvPath, path)

	_, gotPath, err := wadl.Load()
	if err == nil {
		t.Fatal("Load() succeeded on a document that is not the IEEE 2030.5 WADL")
	}
	if errors.Is(err, wadl.ErrNotFound) {
		t.Errorf("wrong document reported as absent: %v", err)
	}
	var ie *wadl.IntegrityError
	if !errors.As(err, &ie) {
		t.Fatalf("Load() error = %T (%v), want *wadl.IntegrityError", err, err)
	}
	if ie.Path != path {
		t.Errorf("IntegrityError.Path = %q, want %q", ie.Path, path)
	}
	if ie.SHA256 == wadl.NormalizedSHA256 {
		t.Error("IntegrityError reports the expected digest, so nothing was actually wrong")
	}
	// Normalization strips the BOM (3 bytes) and both CRs.
	if got, want := ie.Size, len(body)-3-2; got != want {
		t.Errorf("IntegrityError.Size = %d, want %d (normalized length)", got, want)
	}
	if gotPath != path {
		t.Errorf("Load() path = %q, want %q", gotPath, path)
	}
}

// TestRequired covers the switch that keeps an absent WADL from being silently
// satisfiable. A value that is not a boolean is an error rather than a false:
// SEP2_WADL_REQUIRED=ture must not quietly disarm the gate.
func TestRequired(t *testing.T) {
	tests := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: "", want: false},
		{raw: "1", want: true},
		{raw: "true", want: true},
		{raw: "TRUE", want: true},
		{raw: " 1 ", want: true},
		{raw: "0", want: false},
		{raw: "false", want: false},
		{raw: "ture", wantErr: true},
		{raw: "yes", wantErr: true},
	}

	for _, tc := range tests {
		t.Run("value "+tc.raw, func(t *testing.T) {
			t.Setenv(wadl.EnvRequired, tc.raw)

			got, err := wadl.Required()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Required() = %v, want an error for %q", got, tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("Required(): %v", err)
			}
			if got != tc.want {
				t.Errorf("Required() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRequiredWithAbsenceIsFatal is the fourth contract case, and the one the
// other three do not cover: with EnvRequired set and no copy present, the gate
// must FAIL rather than skip.
//
// MustLoadModel calls t.Fatalf, so it is exercised on a synthetic *testing.T
// through a subtest whose outcome is inspected, rather than called directly
// (which would abort this test instead of asserting about it).
func TestRequiredWithAbsenceIsFatal(t *testing.T) {
	// A path that is set but unresolvable is the misconfiguration case, and
	// it must fail whether or not EnvRequired is set. Absence with
	// EnvRequired set must fail too. Both are asserted through Resolve and
	// Required, which are what MustLoadModel branches on.
	dir := emptyModule(t)
	t.Setenv(wadl.EnvPath, "")
	t.Setenv(wadl.EnvRequired, "1")

	required, err := wadl.Required()
	if err != nil {
		t.Fatalf("Required(): %v", err)
	}
	if !required {
		t.Fatal("Required() = false with SEP2_WADL_REQUIRED=1")
	}

	_, rerr := wadl.Resolve()
	if rerr == nil {
		t.Fatalf("Resolve() succeeded in an empty module rooted at %s", dir)
	}
	if !errors.Is(rerr, wadl.ErrNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrNotFound", rerr)
	}
	// required && ErrNotFound is precisely the branch MustLoadModel turns
	// into t.Fatalf rather than t.Skipf.
}

// TestNormalize pins the transformation the pinned digest is taken over.
func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bom and crlf", in: "\xef\xbb\xbf<a>\r\n</a>\r\n", want: "<a>\n</a>\n"},
		{name: "already normalized", in: "<a>\n</a>\n", want: "<a>\n</a>\n"},
		{name: "lone cr", in: "<a>\r</a>", want: "<a></a>"},
		{name: "bom only at the start", in: "<a>\xef\xbb\xbf</a>", want: "<a>\xef\xbb\xbf</a>"},
		{name: "empty", in: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := string(wadl.Normalize([]byte(tc.in))); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
