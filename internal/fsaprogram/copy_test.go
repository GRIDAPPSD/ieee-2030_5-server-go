package fsaprogram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func testTemplate() memory.ProgramTemplate {
	limit := sep2.PerCent(6000)
	return memory.ProgramTemplate{
		ID:                 "derp-00000000000000a1",
		MRID:               "0000000000000000000000000000A001",
		Description:        "volt support",
		Primacy:            2,
		Version:            3,
		DefaultMRID:        "0000000000000000000000000000D001",
		DefaultControlBase: &sep2.DERControlBase{OpModMaxLimW: &limit},
	}
}

func newCopies(t *testing.T) Copies {
	t.Helper()
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	return Copies{
		Programs: memory.NewDERProgramStore(),
		Defaults: memory.NewScopedStore[sep2.DefaultDERControl](),
		Origins:  origins,
	}
}

// assertCopy checks every field a device reads from t's copy under fsa.
func assertCopy(t *testing.T, c Copies, dev, fsa string, tmpl memory.ProgramTemplate) {
	t.Helper()
	ctx := context.Background()
	href := "/edev/" + dev + "/fsa/" + fsa + "/derp/" + tmpl.ID
	p, err := c.Programs.Get(ctx, dev, tmpl.ID)
	if err != nil {
		t.Fatalf("program %s: %v", href, err)
	}
	if p.Href != href || p.MRID != tmpl.MRID || p.Description != tmpl.Description || p.Primacy != tmpl.Primacy {
		t.Errorf("program = href %q mRID %q description %q primacy %d, want %q %q %q %d", p.Href, p.MRID, p.Description, p.Primacy, href, tmpl.MRID, tmpl.Description, tmpl.Primacy)
	}
	if p.Version == nil || *p.Version != tmpl.Version {
		t.Errorf("program version = %v, want %d", p.Version, tmpl.Version)
	}
	if p.DERControlListLink == nil || p.DERControlListLink.Href != href+"/derc" {
		t.Errorf("DERControlListLink = %+v, want %s/derc", p.DERControlListLink, href)
	}
	if p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != href+"/dderc" {
		t.Errorf("DefaultDERControlLink = %+v, want %s/dderc", p.DefaultDERControlLink, href)
	}
	if p.DERCurveListLink != nil || p.ActiveDERControlListLink != nil {
		t.Errorf("curve or active list link set: %+v %+v", p.DERCurveListLink, p.ActiveDERControlListLink)
	}
	d, err := c.Defaults.Get(ctx, derhref.ScopeKey(dev, fsa, tmpl.ID), coresingleton.SingletonKey)
	if err != nil {
		t.Fatalf("default control: %v", err)
	}
	if d.Href != href+"/dderc" || d.MRID != tmpl.DefaultMRID {
		t.Errorf("default = href %q mRID %q, want %s/dderc %s", d.Href, d.MRID, href, tmpl.DefaultMRID)
	}
	if d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || *d.DERControlBase.OpModMaxLimW != *tmpl.DefaultControlBase.OpModMaxLimW {
		t.Errorf("default base = %+v, want opModMaxLimW %d", d.DERControlBase, *tmpl.DefaultControlBase.OpModMaxLimW)
	}
	o, err := c.Origins.Get(ctx, dev, tmpl.ID)
	if err != nil || o.Kind != memory.ProgramOriginTemplate || o.FSAID != fsa || o.TemplateID != tmpl.ID {
		t.Errorf("origin = %+v, %v, want template %s under %s", o, err, tmpl.ID, fsa)
	}
}

func assertNoCopy(t *testing.T, c Copies, dev, fsa, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.Programs.Get(ctx, dev, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program %s/%s err = %v, want ErrNotFound", dev, id, err)
	}
	if _, err := c.Defaults.Get(ctx, derhref.ScopeKey(dev, fsa, id), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default %s/%s/%s err = %v, want ErrNotFound", dev, fsa, id, err)
	}
	if _, err := c.Origins.Get(ctx, dev, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("origin %s/%s err = %v, want ErrNotFound", dev, id, err)
	}
}

func TestEnsure_WritesEveryPartOfTheCopy(t *testing.T) {
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(context.Background(), "7", "fsa-A", tmpl); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	assertCopy(t, c, "7", "fsa-A", tmpl)
}

func TestEnsure_UndoDeletesWhatItWrote(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := testTemplate()
	undo, err := c.Ensure(ctx, "7", "fsa-A", tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(ctx); err != nil {
		t.Fatalf("undo: %v", err)
	}
	assertNoCopy(t, c, "7", "fsa-A", tmpl.ID)
}

// A second Ensure finds the copy present, so its undo must not delete the
// first call's writes.
func TestEnsure_PresentCopyIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	undo, err := c.Ensure(ctx, "7", "fsa-A", tmpl)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if err := undo(ctx); err != nil {
		t.Fatal(err)
	}
	assertCopy(t, c, "7", "fsa-A", tmpl)
}

func TestEnsure_ConflictsWriteNothing(t *testing.T) {
	tmpl := testTemplate()
	fixture := func(id, mrid string) sep2.DERProgram {
		p := sep2.DERProgram{MRID: mrid}
		p.Href = "/edev/7/fsa/fx/derp/" + id
		return p
	}
	cases := map[string]func(t *testing.T, c Copies){
		"fixture program holds the id": func(t *testing.T, c Copies) {
			if err := c.Programs.Create(context.Background(), "7", tmpl.ID, fixture(tmpl.ID, "0000000000000000000000000000BEEF")); err != nil {
				t.Fatal(err)
			}
		},
		"another program holds the mRID in another case": func(t *testing.T, c Copies) {
			if err := c.Programs.Create(context.Background(), "7", "p9", fixture("p9", strings.ToLower(tmpl.MRID))); err != nil {
				t.Fatal(err)
			}
		},
		"a device program holds the id": func(t *testing.T, c Copies) {
			if err := c.Origins.Create(context.Background(), "7", tmpl.ID, memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"}); err != nil {
				t.Fatal(err)
			}
		},
		"a copy of another FSA's template holds the id": func(t *testing.T, c Copies) {
			if err := c.Origins.Create(context.Background(), "7", tmpl.ID, memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: "fsa-B", TemplateID: tmpl.ID}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCopies(t)
			seed(t, c)
			if _, err := c.Ensure(context.Background(), "7", "fsa-A", tmpl); !errors.Is(err, ErrCopyConflict) {
				t.Fatalf("Ensure err = %v, want ErrCopyConflict", err)
			}
			if _, err := c.Defaults.Get(context.Background(), "7/fsa-A/"+tmpl.ID, coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("default written despite the conflict: %v", err)
			}
		})
	}
}

func TestEnsure_RefusesTemplateWithoutDefaultMRID(t *testing.T) {
	c := newCopies(t)
	tmpl := testTemplate()
	tmpl.DefaultMRID = ""
	if _, err := c.Ensure(context.Background(), "7", "fsa-A", tmpl); err == nil {
		t.Fatal("Ensure accepted a template with no default control mRID")
	}
	assertNoCopy(t, c, "7", "fsa-A", tmpl.ID)
}

type failingProgramCreate struct {
	CopyProgramStore
	err error
}

func (f failingProgramCreate) Create(context.Context, string, string, sep2.DERProgram) error {
	return f.err
}

func TestEnsure_FailedProgramWriteUndoesOriginAndDefault(t *testing.T) {
	boom := errors.New("disk full")
	c := newCopies(t)
	c.Programs = failingProgramCreate{CopyProgramStore: c.Programs, err: boom}
	tmpl := testTemplate()
	if _, err := c.Ensure(context.Background(), "7", "fsa-A", tmpl); !errors.Is(err, boom) {
		t.Fatalf("Ensure err = %v, want %v", err, boom)
	}
	assertNoCopy(t, c, "7", "fsa-A", tmpl.ID)
}

func TestReconcileCopies_RestoresMissingParts(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "fsa-A", "fsa-A", "1", "2", "3")
	tmpl := testTemplate()
	if err := admin.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	c := newCopies(t)
	// Device 1: a copy whose program write never landed.
	if err := c.Origins.Create(ctx, "1", tmpl.ID, memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: "fsa-A", TemplateID: tmpl.ID}); err != nil {
		t.Fatal(err)
	}
	// Device 3: a fixture program holds the id, so its copy is skipped.
	fx := sep2.DERProgram{MRID: "0000000000000000000000000000BEEF"}
	fx.Href = "/edev/3/fsa/fx/derp/" + tmpl.ID
	if err := c.Programs.Create(ctx, "3", tmpl.ID, fx); err != nil {
		t.Fatal(err)
	}

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if err := ReconcileCopies(ctx, admin, c, logf); err != nil {
		t.Fatalf("ReconcileCopies: %v", err)
	}
	assertCopy(t, c, "1", "fsa-A", tmpl)
	assertCopy(t, c, "2", "fsa-A", tmpl)
	if got, _ := c.Programs.Get(ctx, "3", tmpl.ID); got.Href != fx.Href || got.MRID != fx.MRID {
		t.Errorf("fixture program on device 3 = %+v, want it untouched", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], `device "3"`) {
		t.Errorf("log = %q, want one line naming device 3", logged)
	}
}

type failingDefaultDelete struct {
	CopyDefaultStore
	err error
}

func (f failingDefaultDelete) Delete(context.Context, string, string) error { return f.err }

func TestRemove_DeletesEveryPartOfTheCopy(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Remove(ctx, "7", "fsa-A", tmpl)
	if err != nil || !removed {
		t.Fatalf("Remove = %v, %v, want true, nil", removed, err)
	}
	assertNoCopy(t, c, "7", "fsa-A", tmpl.ID)
}

func TestRemove_LeavesProgramsThatAreNotTheCopy(t *testing.T) {
	ctx := context.Background()
	tmpl := testTemplate()
	for name, origin := range map[string]*memory.ProgramOrigin{
		"fixture program":                nil,
		"device program":                 {Kind: memory.ProgramOriginDevice, FSAID: "fsa-A"},
		"copy of another FSA's template": {Kind: memory.ProgramOriginTemplate, FSAID: "fsa-B", TemplateID: tmpl.ID},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCopies(t)
			p := sep2.DERProgram{MRID: "0000000000000000000000000000BEEF"}
			p.Href = "/edev/7/fsa/fsa-A/derp/" + tmpl.ID
			if err := c.Programs.Create(ctx, "7", tmpl.ID, p); err != nil {
				t.Fatal(err)
			}
			if origin != nil {
				if err := c.Origins.Create(ctx, "7", tmpl.ID, *origin); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := c.Remove(ctx, "7", "fsa-A", tmpl)
			if err != nil || removed {
				t.Fatalf("Remove = %v, %v, want false, nil", removed, err)
			}
			if got, err := c.Programs.Get(ctx, "7", tmpl.ID); err != nil || got.MRID != p.MRID {
				t.Errorf("program = %+v, %v, want it untouched", got, err)
			}
		})
	}
}

// A removal that fails stops, so what stays is a prefix of the write order
// (origin, default, program) that Ensure recognizes and completes.
func TestRemove_StopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk full")
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	broken := c
	broken.Defaults = failingDefaultDelete{CopyDefaultStore: c.Defaults, err: boom}
	_, err := broken.Remove(ctx, "7", "fsa-A", tmpl)
	var part *PartError
	if !errors.Is(err, boom) || !errors.As(err, &part) || part.Part != "default" || part.DeviceID != "7" || part.TemplateID != tmpl.ID {
		t.Fatalf("Remove err = %v, want a PartError for the default control wrapping %v", err, boom)
	}
	if _, err := c.Programs.Get(ctx, "7", tmpl.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program err = %v, want it removed before the failure", err)
	}
	if _, err := c.Origins.Get(ctx, "7", tmpl.ID); err != nil {
		t.Errorf("origin err = %v, want it kept after the failure", err)
	}
	if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
		t.Fatalf("Ensure over the remainder: %v", err)
	}
	assertCopy(t, c, "7", "fsa-A", tmpl)
}

// Ensure's own undo stops at its first failure too, and both errors reach
// the caller.
func TestEnsure_FailedUndoKeepsAPrefixAndJoinsBothErrors(t *testing.T) {
	ctx := context.Background()
	writeErr, undoErr := errors.New("program write failed"), errors.New("default delete failed")
	c := newCopies(t)
	broken := c
	broken.Programs = failingProgramCreate{CopyProgramStore: c.Programs, err: writeErr}
	broken.Defaults = failingDefaultDelete{CopyDefaultStore: c.Defaults, err: undoErr}
	tmpl := testTemplate()
	_, err := broken.Ensure(ctx, "7", "fsa-A", tmpl)
	var part *PartError
	if !errors.Is(err, writeErr) || !errors.Is(err, undoErr) || !errors.As(err, &part) || part.Part != "default" {
		t.Fatalf("Ensure err = %v, want both errors and a PartError for the default", err)
	}
	if _, err := c.Origins.Get(ctx, "7", tmpl.ID); err != nil {
		t.Errorf("origin err = %v, want it kept", err)
	}
	if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
		t.Fatalf("Ensure over the remainder: %v", err)
	}
	assertCopy(t, c, "7", "fsa-A", tmpl)
}
