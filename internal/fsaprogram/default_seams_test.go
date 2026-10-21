package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1043 round 1: an orphaned default is replaced, a half-applied create has
// one contract, and the default href has one owner.

func TestDefaultHref_IsTheProgramHrefPlusDderc(t *testing.T) {
	if got, want := DefaultHref("/edev/1/fsa/f/derp/p"), "/edev/1/fsa/f/derp/p/dderc"; got != want {
		t.Errorf("DefaultHref = %q, want %q", got, want)
	}
}

func TestEditCreatingDefault_ReplacesAnOrphanedDefaultAndLinksIt(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	p, _ := f.programs.Get(ctx, "e1", "p1")
	p.Href = "/edev/e1/fsa/f/derp/p1"
	if err := f.programs.Update(ctx, "e1", "p1", p); err != nil {
		t.Fatal(err)
	}

	seed := sep2.DefaultDERControl{MRID: "MINTED", SetGradW: u16(7)}
	ch, err := EditCreatingDefault(ctx, f.programs, f.defaults, f.ref, "scope", nil, seed)
	if err != nil || ch != (Changes{Program: true, Default: true}) {
		t.Fatalf("EditCreatingDefault over an orphan = %+v, %v", ch, err)
	}
	d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
	p, _ = f.programs.Get(ctx, "e1", "p1")
	if d.MRID != "D1" || d.SetGradW == nil || *d.SetGradW != 7 || versionOf(d.Version) != 5 || d.Href != "/edev/e1/fsa/f/derp/p1/dderc" {
		t.Errorf("default = %+v, want mRID D1 kept, setGradW 7, version 5 above the old 2", d)
	}
	if p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != d.Href || versionOf(p.Version) != 5 {
		t.Errorf("program link %v version %d, want %s and 5", p.DefaultDERControlLink, versionOf(p.Version), d.Href)
	}
}

func TestEditCreatingDefault_FailedProgramWriteRestoresAnOrphan(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	_, err := EditCreatingDefault(ctx, failingProgramUpdate{ProgramStore: f.programs, err: errors.New("disk")}, f.defaults, f.ref, "scope", nil,
		sep2.DefaultDERControl{MRID: "MINTED", SetGradW: u16(7)})
	if err == nil || errors.Is(err, ErrHalfApplied) {
		t.Fatalf("err = %v, want a clean refusal", err)
	}
	d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
	if d.MRID != "D1" || versionOf(d.Version) != 2 || *d.SetGradW != 100 {
		t.Errorf("orphan after the failed write = %+v, want it put back", d)
	}
}

type failingDefaultRestore struct {
	CopyDefaultStore
}

func (failingDefaultRestore) Delete(context.Context, string, string) error {
	return errors.New("delete gone")
}
func (failingDefaultRestore) Update(context.Context, string, string, sep2.DefaultDERControl) error {
	return errors.New("update gone")
}

type failingCopyProgramUpdate struct {
	CopyProgramStore
}

func (failingCopyProgramUpdate) Update(context.Context, string, string, sep2.DERProgram) error {
	return errors.New("program gone")
}

// Both callers of "write the default, write the program, undo the default"
// report an undo that failed the same way: ErrHalfApplied, with the default
// counted as written.
func TestHalfAppliedCreate_OneContractFromBothCallers(t *testing.T) {
	ctx := context.Background()

	f := seedDefault(t, u16(4), u16(2))
	p, _ := f.programs.Get(ctx, "e1", "p1")
	p.Href = "/edev/e1/fsa/f/derp/p1"
	_ = f.programs.Update(ctx, "e1", "p1", p)
	empty := newCopies(t)
	ch, err := EditCreatingDefault(ctx, failingProgramUpdate{ProgramStore: f.programs, err: errors.New("disk")},
		failingDefaultRestore{CopyDefaultStore: empty.Defaults}, f.ref, "fresh", nil, sep2.DefaultDERControl{MRID: "M"})
	if !errors.Is(err, ErrHalfApplied) || !ch.Default || ch.Program {
		t.Errorf("EditCreatingDefault = %+v, %v, want ErrHalfApplied and Changes{Default: true}", ch, err)
	}

	c := newCopies(t)
	if _, err := c.Ensure(ctx, "1", "fsa-A", noDefaultTemplate()); err != nil {
		t.Fatal(err)
	}
	withDefault := testTemplate()
	withDefault.Version++
	c.Programs = failingCopyProgramUpdate{CopyProgramStore: c.Programs}
	c.Defaults = failingDefaultRestore{CopyDefaultStore: c.Defaults}
	ch, err = c.Sync(ctx, "1", "fsa-A", withDefault)
	if !errors.Is(err, ErrHalfApplied) || !ch.Default || ch.Program {
		t.Errorf("Sync = %+v, %v, want ErrHalfApplied and Changes{Default: true}", ch, err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Errorf("a half-applied sync matches ErrNotFound: %v", err)
	}
}
