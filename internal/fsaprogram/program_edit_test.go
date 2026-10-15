package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
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
		changes            Changes
		wantProg, wantDflt int
		wantPrimacy        uint8
		wantGrad           uint16
	}{
		{"both", setPrimacy(9), setGrad(200), Changes{Program: true, Default: true}, 5, 3, 9, 200},
		{"program only", setPrimacy(9), setGrad(100), Changes{Program: true}, 5, 2, 9, 100},
		{"default only", setPrimacy(1), setGrad(200), Changes{Program: true, Default: true}, 5, 3, 1, 200},
		{"no default given", setPrimacy(9), nil, Changes{Program: true}, 5, 2, 9, 100},
		{"neither", setPrimacy(1), setGrad(100), Changes{}, 4, 2, 1, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedDefault(t, u16(4), u16(2))
			ch, err := EditWithDefault(ctx, f.programs, f.defaults, f.ref, "scope", tc.program, tc.dflt)
			if err != nil || ch != tc.changes {
				t.Fatalf("EditWithDefault = %+v, %v; want %+v", ch, err, tc.changes)
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
	ch, err := EditWithDefault(ctx, programs, f.defaults, f.ref, "scope", setPrimacy(9), setGrad(200))
	if !errors.Is(err, boom) || ch != (Changes{}) {
		t.Fatalf("EditWithDefault = %+v, %v; want %v", ch, err, boom)
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

// countingPrograms counts program writes, and fails them when failWrite is
// set.
type countingPrograms struct {
	CopyProgramStore
	updates   int
	failWrite error
}

func (p *countingPrograms) Update(ctx context.Context, parent, id string, r sep2.DERProgram) error {
	p.updates++
	if p.failWrite != nil {
		return p.failWrite
	}
	return p.CopyProgramStore.Update(ctx, parent, id, r)
}

type countingDefaults struct {
	CopyDefaultStore
	updates  int
	failFrom int // 1-based Update from which every Update fails; 0 never
}

func (d *countingDefaults) Update(ctx context.Context, parent, id string, r sep2.DefaultDERControl) error {
	d.updates++
	if d.failFrom != 0 && d.updates >= d.failFrom {
		return errors.New("default disk gone")
	}
	return d.CopyDefaultStore.Update(ctx, parent, id, r)
}

func editedTemplate(before memory.ProgramTemplate) memory.ProgramTemplate {
	after := before.Copy()
	after.Description, after.Primacy, after.Version, after.DefaultVersion = "peak shave", 70, before.Version+1, before.DefaultVersion+1
	limit := sep2.PerCent(4000)
	after.DefaultControlBase = sep2.DERControlBase{OpModMaxLimW: &limit}
	return after
}

func TestSync_WritesOnlyAStaleCopy(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	before := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", before); err != nil {
		t.Fatal(err)
	}
	progs := &countingPrograms{CopyProgramStore: c.Programs}
	dflts := &countingDefaults{CopyDefaultStore: c.Defaults}
	c.Programs, c.Defaults = progs, dflts

	if ch, err := c.Sync(ctx, "7", "fsa-A", before); err != nil || ch != (Changes{}) || progs.updates+dflts.updates != 0 {
		t.Fatalf("Sync of a current copy = %+v, %v, %d writes; want nothing written", ch, err, progs.updates+dflts.updates)
	}
	after := editedTemplate(before)
	if ch, err := c.Sync(ctx, "7", "fsa-A", after); err != nil || ch != (Changes{Program: true, Default: true}) {
		t.Fatalf("Sync = %+v, %v", ch, err)
	}
	assertCopy(t, c, "7", "fsa-A", after)
	d, _ := c.Defaults.Get(ctx, DefaultScope("7", "fsa-A", after.ID), coresingleton.SingletonKey)
	if versionOf(d.Version) != 1 {
		t.Errorf("default version = %d, want 1", versionOf(d.Version))
	}
}

// A copy whose program write and default restore both fail is left with the
// new default and the old program: each part at one revision of the
// template. The next Sync finishes it.
func TestSync_PartialFailureLeavesRevisionsThatTheNextSyncFinishes(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	before := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", before); err != nil {
		t.Fatal(err)
	}
	healthyP, healthyD := c.Programs, c.Defaults
	boom := errors.New("program disk gone")
	c.Programs = &countingPrograms{CopyProgramStore: healthyP, failWrite: boom}
	c.Defaults = &countingDefaults{CopyDefaultStore: healthyD, failFrom: 2}
	after := editedTemplate(before)
	ch, err := c.Sync(ctx, "7", "fsa-A", after)
	if !errors.Is(err, boom) || ch != (Changes{Default: true}) {
		t.Fatalf("Sync = %+v, %v; want the default written and %v", ch, err, boom)
	}
	p, _ := healthyP.Get(ctx, "7", after.ID)
	d, _ := healthyD.Get(ctx, DefaultScope("7", "fsa-A", after.ID), coresingleton.SingletonKey)
	if versionOf(p.Version) != int(before.Version) || p.Description != before.Description ||
		versionOf(d.Version) != 1 || *d.DERControlBase.OpModMaxLimW != 4000 {
		t.Fatalf("after the failure: program v%d %q, default v%d %v", versionOf(p.Version), p.Description, versionOf(d.Version), *d.DERControlBase.OpModMaxLimW)
	}

	c.Programs, c.Defaults = healthyP, healthyD
	if ch, err := c.Sync(ctx, "7", "fsa-A", after); err != nil || ch != (Changes{Program: true}) {
		t.Fatalf("second Sync = %+v, %v; want the program written", ch, err)
	}
	assertCopy(t, c, "7", "fsa-A", after)
}

// The boot rebuild rewrites a copy left at an older revision, and writes
// nothing to a current one.
func TestReconcileCopies_SyncsAStaleCopy(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "fsa-A", "fsa-A", "1", "2")
	before := testTemplate()
	if err := admin.CreateTemplate(ctx, "fsa-A", before); err != nil {
		t.Fatal(err)
	}
	c := newCopies(t)
	for _, dev := range []string{"1", "2"} {
		if _, err := c.Ensure(ctx, dev, "fsa-A", before); err != nil {
			t.Fatal(err)
		}
	}
	after := editedTemplate(before)
	if err := admin.UpdateTemplate(ctx, "fsa-A", after); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(ctx, "1", "fsa-A", after); err != nil {
		t.Fatal(err)
	}

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if err := ReconcileCopies(ctx, admin, c, logf); err != nil {
		t.Fatal(err)
	}
	assertCopy(t, c, "1", "fsa-A", after)
	assertCopy(t, c, "2", "fsa-A", after)
	if len(logged) != 1 || !strings.Contains(logged[0], `device "2"`) {
		t.Errorf("log = %q, want one sync line naming device 2", logged)
	}

	progs := &countingPrograms{CopyProgramStore: c.Programs}
	dflts := &countingDefaults{CopyDefaultStore: c.Defaults}
	c.Programs, c.Defaults = progs, dflts
	if err := ReconcileCopies(ctx, admin, c, logf); err != nil || progs.updates+dflts.updates != 0 {
		t.Errorf("second rebuild = %v with %d writes, want none", err, progs.updates+dflts.updates)
	}
}

type failingProgramDelete struct {
	CopyProgramStore
	err error
}

func (f failingProgramDelete) Delete(context.Context, string, string) error { return f.err }

func TestRemoveProgram_DeletesEveryPart(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	dev := testTemplate()
	if _, err := c.EnsureDevice(ctx, "7", "fsa-A", dev); err != nil {
		t.Fatal(err)
	}
	seedFixture(t, c, "7", "0", "0")

	if err := c.RemoveProgram(ctx, "7", "fsa-A", dev.ID); err != nil {
		t.Fatal(err)
	}
	assertNoCopy(t, c, "7", "fsa-A", dev.ID)
	if err := c.RemoveProgram(ctx, "7", "0", "0"); err != nil {
		t.Fatal(err)
	}
	assertNoCopy(t, c, "7", "0", "0")
}

func seedFixture(t *testing.T, c Copies, dev, fsa, id string) {
	t.Helper()
	ctx := context.Background()
	fixture := sep2.DERProgram{MRID: "0000000000000000000000000000BEEF"}
	fixture.Href = "/edev/" + dev + "/fsa/" + fsa + "/derp/" + id
	if err := c.Programs.Create(ctx, dev, id, fixture); err != nil {
		t.Fatal(err)
	}
	if err := c.Defaults.Create(ctx, DefaultScope(dev, fsa, id), coresingleton.SingletonKey, sep2.DefaultDERControl{MRID: "D0"}); err != nil {
		t.Fatal(err)
	}
}

// Every partial removal keeps the program, so a retry finds it, or leaves
// only a device program's origin, which the boot cleanup removes. No default
// outlives its program.
func TestRemoveProgram_EveryPartialIsRetriedOrCleanedUp(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk gone")
	for _, tc := range []struct {
		name       string
		fixture    bool
		breakStore func(c *Copies)
		part       string
	}{
		{"fixture, default fails", true, func(c *Copies) { c.Defaults = failingDefaultDelete{CopyDefaultStore: c.Defaults, err: boom} }, PartDefault},
		{"fixture, program fails", true, func(c *Copies) { c.Programs = failingProgramDelete{CopyProgramStore: c.Programs, err: boom} }, PartProgram},
		{"device, default fails", false, func(c *Copies) { c.Defaults = failingDefaultDelete{CopyDefaultStore: c.Defaults, err: boom} }, PartDefault},
		{"device, program fails", false, func(c *Copies) { c.Programs = failingProgramDelete{CopyProgramStore: c.Programs, err: boom} }, PartProgram},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthy := newCopies(t)
			fsa, id := "0", "0"
			if tc.fixture {
				seedFixture(t, healthy, "7", fsa, id)
			} else {
				p := testTemplate()
				fsa, id = "fsa-A", p.ID
				if _, err := healthy.EnsureDevice(ctx, "7", fsa, p); err != nil {
					t.Fatal(err)
				}
			}
			broken := healthy
			tc.breakStore(&broken)
			err := broken.RemoveProgram(ctx, "7", fsa, id)
			var part *PartError
			if !errors.As(err, &part) || part.Part != tc.part || !errors.Is(err, boom) {
				t.Fatalf("RemoveProgram err = %v, want a *PartError on the %s", err, tc.part)
			}
			if _, err := healthy.Programs.Get(ctx, "7", id); err != nil {
				t.Fatalf("program after the failure: %v, want it kept for a retry", err)
			}
			if err := healthy.RemoveProgram(ctx, "7", fsa, id); err != nil {
				t.Fatalf("retry: %v", err)
			}
			assertNoCopy(t, healthy, "7", fsa, id)
		})
	}
}

// A failed origin delete leaves only the origin, which the boot cleanup
// removes once the program is gone.
func TestRemoveProgram_ALeftOriginIsCleanedAtBoot(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	p := testTemplate()
	if _, err := c.EnsureDevice(ctx, "7", "fsa-A", p); err != nil {
		t.Fatal(err)
	}
	broken := c
	broken.Origins = failingOriginDelete{OriginStore: c.Origins}
	var part *PartError
	if err := broken.RemoveProgram(ctx, "7", "fsa-A", p.ID); !errors.As(err, &part) || part.Part != PartOrigin {
		t.Fatalf("RemoveProgram err = %v, want a *PartError on the origin", err)
	}
	if err := ReconcileCopies(ctx, memory.NewAdminFSAStore(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	assertNoCopy(t, c, "7", "fsa-A", p.ID)
}

type failingOriginDelete struct{ OriginStore }

func (failingOriginDelete) Delete(context.Context, string, string) error {
	return errors.New("origin disk gone")
}
