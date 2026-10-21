package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1043: a program's default control can be removed, and a template without
// one is copied, synced and rebuilt without a DefaultDERControl.

func TestEditClearingDefault_UnlinksThenDeletesAndRaisesTheProgramVersionOnce(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	p, _ := f.programs.Get(ctx, "e1", "p1")
	p.DefaultDERControlLink = &sep2.Link{Href: "/edev/e1/fsa/f/derp/p1/dderc"}
	if err := f.programs.Update(ctx, "e1", "p1", p); err != nil {
		t.Fatal(err)
	}

	ch, err := EditClearingDefault(ctx, f.programs, f.defaults, f.ref, "scope", setPrimacy(9))
	if err != nil || ch != (Changes{Program: true, Default: true}) {
		t.Fatalf("EditClearingDefault = %+v, %v", ch, err)
	}
	p, _ = f.programs.Get(ctx, "e1", "p1")
	if p.DefaultDERControlLink != nil || versionOf(p.Version) != 5 || p.Primacy != 9 {
		t.Errorf("program link %v version %d primacy %d, want no link, 5 and 9", p.DefaultDERControlLink, versionOf(p.Version), p.Primacy)
	}
	if _, err := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after clearing: %v, want not found", err)
	}

	ch, err = EditClearingDefault(ctx, f.programs, f.defaults, f.ref, "scope", setPrimacy(9))
	if err != nil || ch != (Changes{}) {
		t.Errorf("second clear = %+v, %v, want no change", ch, err)
	}
	if p, _ = f.programs.Get(ctx, "e1", "p1"); versionOf(p.Version) != 5 {
		t.Errorf("version after a second clear = %d, want 5", versionOf(p.Version))
	}
}

type failingRemover struct{ DefaultRemover }

func (failingRemover) Delete(context.Context, string, string) error { return errors.New("disk gone") }

func TestEditClearingDefault_FailedDeleteLeavesAnUnlinkedDefaultAndRetryFinishes(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	remover := f.defaults.(DefaultRemover)
	if _, err := EditClearingDefault(ctx, f.programs, failingRemover{remover}, f.ref, "scope", nil); err == nil {
		t.Fatal("a failed delete reported success")
	}
	if _, err := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey); err != nil {
		t.Fatalf("default after a failed delete: %v, want it kept", err)
	}
	if _, err := EditClearingDefault(ctx, f.programs, remover, f.ref, "scope", nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after the retry: %v, want not found", err)
	}
}

func noDefaultTemplate() memory.ProgramTemplate {
	tmpl := testTemplate()
	tmpl.DefaultControlBase = nil
	return tmpl
}

func TestEnsure_TemplateWithoutDefaultWritesNoDefaultAndNoLink(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := noDefaultTemplate()
	if _, err := c.Ensure(ctx, "1", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	p, err := c.Programs.Get(ctx, "1", tmpl.ID)
	if err != nil || p.DefaultDERControlLink != nil || p.DERControlListLink == nil || p.MRID != tmpl.MRID {
		t.Errorf("program = %+v, %v, want a program with a control list link and no default link", p, err)
	}
	if _, err := c.Defaults.Get(ctx, derhref.ScopeKey("1", "fsa-A", tmpl.ID), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default: %v, want not found", err)
	}
	if o, err := c.Origins.Get(ctx, "1", tmpl.ID); err != nil || o.TemplateID != tmpl.ID {
		t.Errorf("origin = %+v, %v", o, err)
	}
}

func TestSync_RemovesAndRestoresTheDefaultWithTheTemplate(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(ctx, "1", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	scope := derhref.ScopeKey("1", "fsa-A", tmpl.ID)

	cleared := tmpl.Copy()
	cleared.DefaultControlBase = nil
	cleared.Version++
	ch, err := c.Sync(ctx, "1", "fsa-A", cleared)
	if err != nil || ch != (Changes{Program: true, Default: true}) {
		t.Fatalf("Sync to a template with no default = %+v, %v", ch, err)
	}
	p, _ := c.Programs.Get(ctx, "1", tmpl.ID)
	if p.DefaultDERControlLink != nil || versionOf(p.Version) != int(cleared.Version) {
		t.Errorf("program link %v version %d, want no link and %d", p.DefaultDERControlLink, versionOf(p.Version), cleared.Version)
	}
	if _, err := c.Defaults.Get(ctx, scope, coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after the sync: %v, want not found", err)
	}
	if ch, err := c.Sync(ctx, "1", "fsa-A", cleared); err != nil || ch != (Changes{}) {
		t.Errorf("second Sync = %+v, %v, want no change", ch, err)
	}

	restored := cleared.Copy()
	limit := sep2.PerCent(3000)
	restored.DefaultControlBase = &sep2.DERControlBase{OpModMaxLimW: &limit}
	restored.Version++
	restored.DefaultVersion++
	ch, err = c.Sync(ctx, "1", "fsa-A", restored)
	if err != nil || ch != (Changes{Program: true, Default: true}) {
		t.Fatalf("Sync to a template with a default again = %+v, %v", ch, err)
	}
	assertCopy(t, c, "1", "fsa-A", restored)
	d, _ := c.Defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if versionOf(d.Version) != int(restored.DefaultVersion) || *d.DERControlBase.OpModMaxLimW != 3000 {
		t.Errorf("restored default version %d base %+v", versionOf(d.Version), d.DERControlBase)
	}
}

func TestReconcileCopies_FinishesARemovalThatLeftAnUnlinkedDefault(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(ctx, "1", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	admin := memory.NewAdminFSAStore()
	if err := admin.Create(ctx, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	cleared := tmpl.Copy()
	cleared.DefaultControlBase = nil
	cleared.Version++
	if err := admin.CreateTemplate(ctx, "fsa-A", cleared); err != nil {
		t.Fatal(err)
	}
	if err := admin.AssignDevice(ctx, "fsa-A", "1"); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileCopies(ctx, admin, c, t.Logf); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Defaults.Get(ctx, derhref.ScopeKey("1", "fsa-A", tmpl.ID), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after the rebuild: %v, want not found", err)
	}
	if p, _ := c.Programs.Get(ctx, "1", tmpl.ID); p.DefaultDERControlLink != nil {
		t.Errorf("program link after the rebuild = %v, want none", p.DefaultDERControlLink)
	}
}

// A partial clear (program written, default delete failed) reports the
// program as written so the caller can notify it.
func TestEditClearingDefault_FailedDeleteReportsTheProgramWrite(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	ch, err := EditClearingDefault(ctx, f.programs, failingRemover{f.defaults.(DefaultRemover)}, f.ref, "scope", nil)
	if err == nil || ch != (Changes{Program: true}) {
		t.Errorf("EditClearingDefault = %+v, %v, want Changes{Program: true} and an error", ch, err)
	}
}
