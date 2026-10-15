package bootfixture_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A program persisted before #743 has an href with no FSA segment, and no
// FSA lists it. When the fixture names the program, the fixture's fsa_id is
// where it was created, so reconcile moves its href and links there and
// keeps every other persisted field.
func TestReconcileMigratesPersistedProgramWithoutFSAToFixtureFSA(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)
	ctx := context.Background()
	h.mustBoot(fixtureV1)

	_, derps := h.reopen()
	prog := mustProgram(t, derps, "e1", "p1")
	prog.Href = "/edev/e1/derp/p1"
	prog.DERControlListLink = &sep2.ListLink{Href: "/edev/e1/derp/p1/derc", All: 2}
	prog.DefaultDERControlLink = &sep2.Link{Href: "/edev/e1/derp/p1/dderc"}
	prog.DERCurveListLink = &sep2.ListLink{Href: "/dc", All: 1}
	prog.Primacy = 7
	if err := derps.Update(ctx, "e1", "p1", prog); err != nil {
		t.Fatalf("write pre-#743 href: %v", err)
	}

	h.mustBoot(fixtureV1)
	h.requireLog("DERProgram", `"e1"`, `"p1"`, `"/edev/e1/derp/p1"`, `"f1"`)

	_, derps = h.reopen()
	got := mustProgram(t, derps, "e1", "p1")
	const want = "/edev/e1/fsa/f1/derp/p1"
	if got.Href != want {
		t.Errorf("p1 Href = %q, want %q", got.Href, want)
	}
	if got.DERControlListLink == nil || got.DERControlListLink.Href != want+"/derc" || got.DERControlListLink.All != 2 {
		t.Errorf("p1 DERControlListLink = %+v, want %q all 2", got.DERControlListLink, want+"/derc")
	}
	if got.DefaultDERControlLink == nil || got.DefaultDERControlLink.Href != want+"/dderc" {
		t.Errorf("p1 DefaultDERControlLink = %+v, want %q", got.DefaultDERControlLink, want+"/dderc")
	}
	if got.DERCurveListLink == nil || got.DERCurveListLink.Href != "/dc" {
		t.Errorf("p1 DERCurveListLink = %+v, want /dc untouched", got.DERCurveListLink)
	}
	if got.Primacy != 7 || got.MRID != "A1A1A1A1A1A1A1A1" {
		t.Errorf("p1 primacy %d mRID %q, want the persisted 7 and A1A1A1A1A1A1A1A1", got.Primacy, got.MRID)
	}
	if err := bootfixture.CheckDERProgramHrefs(ctx, derps); err != nil {
		t.Errorf("CheckDERProgramHrefs after migration: %v", err)
	}
}

// A program with no FSA segment that no fixture names has no record of its
// FSA, so the server refuses to boot and names it rather than serving it
// under no FSA.
func TestCheckDERProgramHrefsRefusesProgramsWithoutFSA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	programs := memory.NewScopedStore[sep2.DERProgram]()
	seed := map[[2]string]string{
		{"e1", "p1"}: "/edev/e1/fsa/f1/derp/p1",
		{"e9", "x"}:  "/edev/e9/derp/x",
		{"e9", "y"}:  "",
	}
	for k, href := range seed {
		p := sep2.DERProgram{MRID: "M-" + k[1]}
		p.Href = href
		if err := programs.Create(ctx, k[0], k[1], p); err != nil {
			t.Fatalf("seed %v: %v", k, err)
		}
	}
	err := bootfixture.CheckDERProgramHrefs(ctx, programs)
	if !errors.Is(err, bootfixture.ErrDERProgramWithoutFSA) {
		t.Fatalf("err = %v, want ErrDERProgramWithoutFSA", err)
	}
	for _, want := range []string{`edev="e9" mRID="M-x" href="/edev/e9/derp/x"`, `edev="e9" mRID="M-y" href=""`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), `M-p1`) {
		t.Errorf("err %q names the well-formed p1", err)
	}

	clean := memory.NewScopedStore[sep2.DERProgram]()
	p := sep2.DERProgram{}
	p.Href = "/edev/e1/fsa/f1/derp/p1"
	if err := clean.Create(ctx, "e1", "p1", p); err != nil {
		t.Fatalf("seed clean: %v", err)
	}
	if err := bootfixture.CheckDERProgramHrefs(ctx, clean); err != nil {
		t.Errorf("clean store: err = %v, want nil", err)
	}
}
