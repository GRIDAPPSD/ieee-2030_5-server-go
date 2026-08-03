// Package wadl locates the normative IEEE 2030.5 WADL (sep_wadl.xml) and
// drives a conformance sweep from it.
//
// The WADL is published with IEEE 2030.5 and is NOT distributed with this
// project. See the NOTICE file at the repository root for attribution and
// for how to obtain a copy at no charge through the IEEE GET Program.
//
// This file is the locator half, and it deliberately mirrors the schema
// locator in package schema clause for clause: same resolution order, same
// absent-versus-misconfigured split, same required switch, same normalized
// digest pin. Two artifacts supplied the same way should be configured the
// same way; a contributor who has learned SEP2_SCHEMA_PATH already knows
// SEP2_WADL_PATH.
//
// Absence is not failure. When no copy is configured, Load returns an error
// wrapping ErrNotFound and the WADL-gated tests skip, so a contributor
// without an IEEE copy still runs the suite green. Absence is also never
// silently satisfiable: set SEP2_WADL_REQUIRED (see Required) and a missing
// copy becomes a hard failure instead.
package wadl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// EnvPath names the environment variable holding the path to a copy of
	// sep_wadl.xml. It takes precedence over DefaultPath.
	EnvPath = "SEP2_WADL_PATH"

	// EnvRequired names the environment variable that converts an absent
	// WADL from a skip into a hard failure. See Required.
	EnvRequired = "SEP2_WADL_REQUIRED"

	// DefaultPath is the conventional location for a locally supplied copy,
	// relative to the module root. It is gitignored, so a developer can drop
	// a licensed copy there and have the harness work with no configuration
	// and no risk of committing the file.
	DefaultPath = "test/conformance/wadl/sep_wadl.xml"

	// NormalizedSHA256 identifies the exact document this harness drives
	// from: IEEE 2030.5-2018, Model Build 20180301, sep_wadl.xml. The digest
	// is taken over the normalized form (see Normalize) so that copies
	// differing only in line endings or in the presence of a UTF-8 BOM still
	// match. The copy this pin was taken from carries neither, but the
	// schema's copies in circulation differ in exactly those two ways, so
	// the same normalization is applied here rather than betting that the
	// WADL will never be reflowed by a checkout or an editor.
	NormalizedSHA256 = "93ee874d3a7e216dd1074781a0be390af2fa075f8f9145f5fbf08d509f4fa1c5"

	// NormalizedSize is the length in bytes of that same normalized form. It
	// is reported alongside a digest mismatch so a wrong-document error is
	// readable rather than just two unequal hex strings.
	NormalizedSize = 220733
)

// Namespace constants declared by the WADL. These are facts about the
// standard rather than about any particular copy of the file, so they stay
// available whether or not a copy is present.
const (
	// NamespaceWADL is the W3C-submitted WADL namespace the document uses.
	NamespaceWADL = "http://wadl.dev.java.net/2009/02"

	// NamespaceExt is the IEEE 2030.5 WADL extension namespace, which
	// carries wx:mode (the conformance requirement level) and wx:samplePath
	// (the concrete resource path). Both are what make this document usable
	// as a conformance oracle rather than merely as documentation.
	NamespaceExt = "urn:ieee:std:2030.5:wadlExt"
)

// ErrNotFound reports that no copy of the WADL is configured or present.
// Callers distinguish it with errors.Is and skip rather than fail: it means
// "not available here", not "wrong" or "broken".
var ErrNotFound = errors.New("IEEE 2030.5 WADL (sep_wadl.xml) not available: it is not distributed with this project; " +
	"set " + EnvPath + " to a copy, or place one at " + DefaultPath + " under the module root; " +
	"see the NOTICE file for how to obtain it at no charge from IEEE")

// IntegrityError reports that the file found is not the document this
// harness drives from. It is deliberately NOT an ErrNotFound: sweeping
// against the wrong WADL is worse than not sweeping at all, because it
// would report conformance against requirements the standard never stated.
type IntegrityError struct {
	// Path is the file that was read.
	Path string
	// SHA256 is the normalized digest actually computed.
	SHA256 string
	// Size is the normalized length actually read, in bytes.
	Size int
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("%s is not the IEEE 2030.5 WADL this harness drives from: "+
		"normalized sha256 %s (%d bytes), want %s (%d bytes); "+
		"the expected document is IEEE 2030.5-2018 Model Build 20180301 sep_wadl.xml (see test/conformance/README.md); "+
		"point %s at that document. The WADL-gated tests skip only when no copy is found at all, "+
		"never when the copy found is the wrong document",
		e.Path, e.SHA256, e.Size, NormalizedSHA256, NormalizedSize, EnvPath)
}

// Required reports whether the caller has demanded that a WADL copy be
// present, by setting EnvRequired to a truthy value.
//
// This is the switch that keeps absence from being silently satisfiable. CI
// that supplies a WADL sets it, so a rotated secret, a typo in the path
// variable, or a decode step that quietly produced nothing fails the run
// instead of skipping every gated test and reporting green.
//
// An unparseable value is an error rather than a shrug: a run configured
// with SEP2_WADL_REQUIRED=ture must not disarm the gate.
func Required() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(EnvRequired))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: use 1 or 0", EnvRequired, raw)
	}
	return v, nil
}

// Load returns the WADL bytes and the path they were read from.
//
// Resolution order is EnvPath, then DefaultPath under the module root. A
// missing copy returns an error wrapping ErrNotFound. A copy that is not the
// expected document returns *IntegrityError. An EnvPath that is set but does
// not resolve returns neither: an explicitly configured path that is wrong is
// a misconfiguration, and reporting it as "not available" would let a broken
// CI secret masquerade as an absent WADL.
func Load() (data []byte, path string, err error) {
	path, err = Resolve()
	if err != nil {
		return nil, "", err
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("reading IEEE 2030.5 WADL %s: %w", path, err)
	}
	norm := Normalize(data)
	sum := sha256.Sum256(norm)
	if got := hex.EncodeToString(sum[:]); got != NormalizedSHA256 {
		return nil, path, &IntegrityError{Path: path, SHA256: got, Size: len(norm)}
	}
	return data, path, nil
}

// Resolve returns the path of the WADL copy to read, without reading it.
func Resolve() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvPath)); p != "" {
		if err := checkFile(p); err != nil {
			return "", fmt.Errorf("%s=%q: %w", EnvPath, p, err)
		}
		return p, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", fmt.Errorf("%w (%v)", ErrNotFound, err)
	}
	p := filepath.Join(root, DefaultPath)
	if err := checkFile(p); err != nil {
		return "", fmt.Errorf("%w (looked for %s)", ErrNotFound, p)
	}
	return p, nil
}

// Normalize strips the UTF-8 BOM and every carriage return, yielding the
// form NormalizedSHA256 and NormalizedSize describe. This matches the
// schema locator's normalization exactly so that one rule ("we compare the
// normalized form") covers both supplied artifacts.
func Normalize(data []byte) []byte {
	out := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	return bytes.ReplaceAll(out, []byte("\r"), nil)
}

func checkFile(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, want a file", p)
	}
	return nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod. Go tests run with the working directory set to their own package
// directory, so DefaultPath cannot be resolved relative to it directly.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found above the working directory")
		}
		dir = parent
	}
}
