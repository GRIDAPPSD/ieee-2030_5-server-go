package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #922: one operator edit of a program and its default raises each version
// once; template copies take the template's versions; delete removes every
// part of a program.

func setGrad(v uint16) func(*sep2.DefaultDERControl) error {
	return func(d *sep2.DefaultDERControl) error { d.SetGradW = u16(v); return nil }
}

func TestEditWithDefault_RaisesEachVersionOnce(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		program            func(*sep2.DERProgram) error
		dflt               func(*sep2.DefaultDERControl) error
		changed            bool
		wantProg, wantDflt int
		wantPrimacy        uint8
		wantGrad           uint16
	}{
		{"both", setPrimacy(9), setGrad(200), true, 5, 3, 9, 200},
		{"program only", setPrimacy(9), setGrad(100), true, 5, 2, 9, 100},
		{"default only", setPrimacy(1), setGrad(200), true, 5, 3, 1, 200},
		{"no default given", setPrimacy(9), nil, true, 5, 2, 9, 100},
		{"neither", setPrimacy(1), setGrad(100), false, 4, 2, 1, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedDefault(t, u16(4), u16(2))
			changed, err := EditWithDefault(ctx, f.programs, f.defaults, f.ref, "scope", tc.program, tc.dflt)
			if err != nil || changed != tc.changed {
				t.Fatalf("EditWithDefault = %v, %v; want changed %v", changed, err, tc.changed)
			}
			p, _ := f.programs.Get(ctx, "e1", "p1")
			d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
			if versionOf(p.Version) != tc.wantProg || p.Primacy != tc.wantPrimacy {
				t.Errorf("program version %d primacy %d, want %d and %d", versionOf(p.Version), p.Primacy, tc.wantProg, tc.wantPrimacy)
			}
			if versionOf(d.Version) != tc.wantDflt || *d.SetGradW != tc.wantGrad {
				t.Errorf("default version %d setGradW %d, want %d and %d", versionOf(d.Version), *d.SetGradW, tc.wantDflt, tc.wantGrad)
			}
		})
	}
}

func TestEditWithDefault_FailedProgramWritePutsTheDefaultBack(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	boom := errors.New("program disk gone")
	programs := failingProgramUpdate{ProgramStore: f.programs, err: boom}
	changed, err := EditWithDefault(ctx, programs, f.defaults, f.ref, "scope", setPrimacy(9), setGrad(200))
	if !errors.Is(err, boom) || changed {
		t.Fatalf("EditWithDefault = %v, %v; want %v", changed, err, boom)
	}
	d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
	if versionOf(d.Version) != 2 || *d.SetGradW != 100 {
		t.Errorf("default = version %d setGradW %d, want it put back at 2 and 100", versionOf(d.Version), *d.SetGradW)
	}
}

type failingProgramUpdate struct {
	ProgramStore
	err error
}

func (f failingProgramUpdate) Update(context.Context, string, string, sep2.DERProgram) error {
	return f.err
}

func TestCopyDefault_CarriesTheTemplateDefaultVersion(t *testing.T) {
	tmpl := testTemplate()
	if d := CopyDefault("7", "fsa-A", tmpl); d.Version != nil {
		t.Errorf("default version = %d, want none for a default never edited", *d.Version)
	}
	tmpl.DefaultVersion = 4
	if d := CopyDefault("7", "fsa-A", tmpl); versionOf(d.Version) != 4 {
		t.Errorf("default version = %d, want 4", versionOf(d.Version))
	}
}

func TestSync_WritesTheTemplateIntoTheCopyAndUndoes(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	before := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", before); err != nil {
		t.Fatal(err)
	}
	after := before.Copy()
	after.Description, after.Primacy, after.Version, after.DefaultVersion = "peak shave", 70, 4, 1
	limit := sep2.PerCent(4000)
	after.DefaultControlBase = sep2.DERControlBase{OpModMaxLimW: &limit}

	undo, err := c.Sync(ctx, "7", "fsa-A", after)
	if err != nil {
		t.Fatal(err)
	}
	assertCopy(t, c, "7", "fsa-A", after)
	d, _ := c.Defaults.Get(ctx, DefaultScope("7", "fsa-A", after.ID), coresingleton.SingletonKey)
	if versionOf(d.Version) != 1 {
		t.Errorf("default version = %d, want 1", versionOf(d.Version))
	}

	if err := undo(ctx); err != nil {
		t.Fatal(err)
	}
	assertCopy(t, c, "7", "fsa-A", before)
	d, _ = c.Defaults.Get(ctx, DefaultScope("7", "fsa-A", after.ID), coresingleton.SingletonKey)
	if d.Version != nil {
		t.Errorf("default version after undo = %d, want none", *d.Version)
	}
}

func TestSync_FailedProgramWritePutsTheDefaultBack(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	before := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", before); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("program disk gone")
	c.Programs = failingCopyUpdate{CopyProgramStore: c.Programs, err: boom}
	after := before.Copy()
	after.Version, after.DefaultVersion = 4, 1
	limit := sep2.PerCent(4000)
	after.DefaultControlBase = sep2.DERControlBase{OpModMaxLimW: &limit}
	if _, err := c.Sync(ctx, "7", "fsa-A", after); !errors.Is(err, boom) {
		t.Fatalf("Sync err = %v, want %v", err, boom)
	}
	assertCopy(t, c, "7", "fsa-A", before)
}

type failingCopyUpdate struct {
	CopyProgramStore
	err error
}

func (f failingCopyUpdate) Update(context.Context, string, string, sep2.DERProgram) error {
	return f.err
}

func TestRemoveProgram_DeletesEveryPart(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	dev := testTemplate()
	if _, err := c.EnsureDevice(ctx, "7", "fsa-A", dev); err != nil {
		t.Fatal(err)
	}
	fixture := sep2.DERProgram{MRID: "0000000000000000000000000000BEEF"}
	fixture.Href = "/edev/7/fsa/0/derp/0"
	if err := c.Programs.Create(ctx, "7", "0", fixture); err != nil {
		t.Fatal(err)
	}
	if err := c.Defaults.Create(ctx, DefaultScope("7", "0", "0"), coresingleton.SingletonKey, sep2.DefaultDERControl{MRID: "D0"}); err != nil {
		t.Fatal(err)
	}

	if err := c.RemoveProgram(ctx, "7", "fsa-A", dev.ID); err != nil {
		t.Fatal(err)
	}
	assertNoCopy(t, c, "7", "fsa-A", dev.ID)
	if err := c.RemoveProgram(ctx, "7", "0", "0"); err != nil {
		t.Fatal(err)
	}
	assertNoCopy(t, c, "7", "0", "0")
}

func TestRemoveProgram_StopsAtTheFirstFailureLeavingTheOrigin(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	dev := testTemplate()
	if _, err := c.EnsureDevice(ctx, "7", "fsa-A", dev); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("default disk gone")
	c.Defaults = failingDefaultDelete{CopyDefaultStore: c.Defaults, err: boom}
	err := c.RemoveProgram(ctx, "7", "fsa-A", dev.ID)
	var part *PartError
	if !errors.As(err, &part) || part.Part != PartDefault || !errors.Is(err, boom) {
		t.Fatalf("RemoveProgram err = %v, want a *PartError on the default", err)
	}
	if _, err := c.Programs.Get(ctx, "7", dev.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program after the failure: %v, want it deleted", err)
	}
	if o, err := c.Origins.Get(ctx, "7", dev.ID); err != nil || o.Kind != memory.ProgramOriginDevice {
		t.Errorf("origin after the failure = %+v, %v; want it kept for the boot cleanup", o, err)
	}
}
