package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// testHarness bundles an Issuer with the real memory-backed stores behind
// it, so tests can seed a DERProgram and inspect what Issue actually wrote,
// rather than a hand-rolled fake that could drift from the real contract.
type testHarness struct {
	issuer     *Issuer
	programs   *memory.ScopedStore[sep2.DERProgram]
	controls   *memory.ScopedStore[sep2.DERControl]
	lifecycles *memory.ScopedStore[LifecycleRecord]
}

func newHarness(cfg Config) *testHarness {
	h := &testHarness{
		programs:   memory.NewScopedStore[sep2.DERProgram](),
		controls:   memory.NewScopedStore[sep2.DERControl](),
		lifecycles: memory.NewScopedStore[LifecycleRecord](),
	}
	h.issuer = NewIssuer(h.programs, h.controls, h.lifecycles, cfg)
	return h
}

func testPEN(v uint32) *uint32 { return &v }

// seedProgram stores a DERProgram at (edev, derp) whose DERControlListLink
// points at linkHref. Most tests want the link's own fsa segment to equal
// derpFSA; acceptance-criterion-1 tests deliberately pass a different one.
func (h *testHarness) seedProgram(t *testing.T, edev, derp, linkHref string) {
	t.Helper()
	p := sep2.DERProgram{
		DERControlListLink: &sep2.ListLink{Href: linkHref},
	}
	if err := h.programs.Create(context.Background(), edev, derp, p); err != nil {
		t.Fatalf("seed program: %v", err)
	}
}

func programHref(edev, fsa, derp string) string {
	return "/edev/" + edev + "/fsa/" + fsa + "/derp/" + derp
}

func controlListHref(edev, fsa, derp string) string {
	return programHref(edev, fsa, derp) + "/derc"
}

func uint16ptr(v uint16) *uint16 { return &v }

// assertRefusal fails the test unless err is a *RefusalError with the given
// code, per acceptance criterion 9 (every refusal is a distinct, mappable
// error).
func assertRefusal(t *testing.T, err error, want RefusalCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want refusal %q", want)
	}
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v (%T), want *RefusalError", err, err)
	}
	if refusal.Code != want {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, want)
	}
}

// assertNoNewControl fails the test unless every scope the harness's
// control store knows holds zero controls, per acceptance criterion 9
// ("a test asserts the store holds no new record after each refusal").
// Tests call it against a harness that issued nothing successfully before
// the refusal under test.
func assertNoNewControl(t *testing.T, h *testHarness) {
	t.Helper()
	ctx := context.Background()
	parents, err := h.controls.Parents(ctx)
	if err != nil {
		t.Fatalf("Parents() error = %v", err)
	}
	for _, p := range parents {
		n, err := h.controls.Count(ctx, p)
		if err != nil {
			t.Fatalf("Count(%q) error = %v", p, err)
		}
		if n != 0 {
			t.Fatalf("scope %q holds %d controls, want 0 after refusal", p, n)
		}
	}
}
