package dercontrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Acceptance criterion 1: the issuer resolves the storage scope from the
// stored DERProgram's DERControlListLink href, never from the fsaId in the
// request's program href.
func TestIssue_ScopeFromControlListLink_NotRequestFSAID(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	// Two FSAs under one device: the program is reachable at both, but its
	// own DERControlListLink names fsa "0".
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "1", "p1"), // request says fsa "1"
		Type:            Connect,
		DurationSeconds: 3600,
	}

	res, err := h.issuer.Issue(context.Background(), req)
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}
	if res.Scope.FSAID != "0" {
		t.Fatalf("Scope.FSAID = %q, want %q (the link's fsa, not the request's)", res.Scope.FSAID, "0")
	}
	if !strings.HasPrefix(res.Href, controlListHref("dev1", "0", "p1")+"/") {
		t.Fatalf("Href = %q, want prefix %q", res.Href, controlListHref("dev1", "0", "p1")+"/")
	}
	// The control must be findable under the link's scope key, not the
	// request's fsa.
	got, err := h.controls.Get(context.Background(), "dev1/0/p1", res.ID)
	if err != nil {
		t.Fatalf("control not stored under link scope: %v", err)
	}
	if got.MRID != res.Control.MRID {
		t.Fatalf("stored MRID = %q, want %q", got.MRID, res.Control.MRID)
	}
	if _, err := h.controls.Get(context.Background(), "dev1/1/p1", res.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("control unexpectedly reachable under request's fsa scope, err = %v", err)
	}
}

func TestIssue_RefusesNoControlListLink(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", "") // Href empty -> parses as invalid

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalNoControlListLink)
	assertNoNewControl(t, h)
}

// seedProgram always sets a non-nil *ListLink, even with an empty Href,
// so no other test exercises the nil-link check itself: only the parse
// that follows it. A program with a literally nil DERControlListLink
// proves the check guards that next line's dereference.
func TestIssue_RefusesNilControlListLink(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	if err := h.programs.Create(context.Background(), "dev1", "p1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalNoControlListLink)
	assertNoNewControl(t, h)
}

func TestIssue_RefusesControlListLinkNamingAnotherDevice(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	// The link claims a different device than the one the program was
	// loaded under.
	h.seedProgram(t, "dev1", "p1", controlListHref("dev2", "0", "p1"))

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalNoControlListLink)
	assertNoNewControl(t, h)
}

func TestIssue_RefusesControlListLinkNamingAnotherProgram(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	// The link claims a different DERProgram than the one it was loaded from.
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p2"))

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalNoControlListLink)
	assertNoNewControl(t, h)
}

func TestIssue_RefusesControlListLinkWithoutDercSuffix(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	// No device ever reaches a control list at the program's own href: the
	// link must end in "/derc".
	h.seedProgram(t, "dev1", "p1", programHref("dev1", "0", "p1"))

	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalNoControlListLink)
	assertNoNewControl(t, h)
}

func TestIssue_RefusesProgramNotFound(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "nope"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalProgramNotFound)
}

func TestIssue_RefusesInvalidProgramHref(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	req := CreateRequest{
		DERProgramHref:  "/not/a/program/href",
		Type:            Connect,
		DurationSeconds: 3600,
	}
	_, err := h.issuer.Issue(context.Background(), req)
	assertRefusal(t, err, RefusalInvalidProgramHref)
}
