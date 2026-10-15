package fsaprogram

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestEnsureDevice_RecordsADeviceProgramWithNoTemplate(t *testing.T) {
	c := newCopies(t)
	tmpl := testTemplate()
	if _, err := c.EnsureDevice(context.Background(), "7", "fsa-A", tmpl); err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	o, err := c.Origins.Get(context.Background(), "7", tmpl.ID)
	if err != nil || o.Kind != memory.ProgramOriginDevice || o.FSAID != "fsa-A" || o.TemplateID != "" {
		t.Errorf("origin = %+v, %v, want a device origin under fsa-A with no templateID", o, err)
	}
	p, err := c.Programs.Get(context.Background(), "7", tmpl.ID)
	if err != nil || p.Href != "/edev/7/fsa/fsa-A/derp/"+tmpl.ID || p.MRID != tmpl.MRID {
		t.Errorf("program = %+v, %v", p, err)
	}
}

func TestEnsureDevice_UndoRemovesEveryPart(t *testing.T) {
	c := newCopies(t)
	tmpl := testTemplate()
	undo, err := c.EnsureDevice(context.Background(), "7", "fsa-A", tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(context.Background()); err != nil {
		t.Fatalf("undo: %v", err)
	}
	assertNoCopy(t, c, "7", "fsa-A", tmpl.ID)
}

func TestEnsureDevice_RefusesAnIdOrMRIDTheDeviceHolds(t *testing.T) {
	ctx := context.Background()
	t.Run("same id from a template copy", func(t *testing.T) {
		c := newCopies(t)
		tmpl := testTemplate()
		if _, err := c.Ensure(ctx, "7", "fsa-A", tmpl); err != nil {
			t.Fatal(err)
		}
		if _, err := c.EnsureDevice(ctx, "7", "fsa-A", tmpl); !errors.Is(err, ErrCopyConflict) {
			t.Errorf("err = %v, want ErrCopyConflict", err)
		}
		o, _ := c.Origins.Get(ctx, "7", tmpl.ID)
		if o.Kind != memory.ProgramOriginTemplate {
			t.Errorf("origin kind = %q, want the template copy left as it was", o.Kind)
		}
	})
	t.Run("same mRID under another id", func(t *testing.T) {
		c := newCopies(t)
		first := testTemplate()
		if _, err := c.EnsureDevice(ctx, "7", "fsa-A", first); err != nil {
			t.Fatal(err)
		}
		second := testTemplate()
		second.ID = "derp-00000000000000a2"
		if _, err := c.EnsureDevice(ctx, "7", "fsa-A", second); !errors.Is(err, ErrCopyConflict) {
			t.Errorf("err = %v, want ErrCopyConflict", err)
		}
		assertNoCopy(t, c, "7", "fsa-A", second.ID)
	})
}

func TestDevicePrograms_ListsOnlyDeviceProgramsOfThatFSA(t *testing.T) {
	ctx := context.Background()
	c := newCopies(t)
	mk := func(id, mrid string) memory.ProgramTemplate {
		t := testTemplate()
		t.ID, t.MRID, t.DefaultMRID = id, mrid, mrid[:len(mrid)-1]+"F"
		return t
	}
	dev1, dev2 := mk("derp-b", "0000000000000000000000000000A00B"), mk("derp-a", "0000000000000000000000000000A00A")
	other, tmplCopy := mk("derp-c", "0000000000000000000000000000A00C"), mk("derp-d", "0000000000000000000000000000A00D")
	for _, w := range []struct {
		dev, fsa string
		t        memory.ProgramTemplate
		device   bool
	}{
		{"7", "fsa-A", dev1, true}, {"7", "fsa-A", dev2, true}, {"7", "fsa-B", other, true},
		{"7", "fsa-A", tmplCopy, false}, {"8", "fsa-A", mk("derp-e", "0000000000000000000000000000A00E"), true},
	} {
		var err error
		if w.device {
			_, err = c.EnsureDevice(ctx, w.dev, w.fsa, w.t)
		} else {
			_, err = c.Ensure(ctx, w.dev, w.fsa, w.t)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.DevicePrograms(ctx, "7", "fsa-A")
	if err != nil || !slices.Equal(got, []string{"derp-a", "derp-b"}) {
		t.Errorf("DevicePrograms(7, fsa-A) = %v, %v, want [derp-a derp-b]", got, err)
	}

	if err := c.Programs.Delete(ctx, "7", "derp-a"); err != nil {
		t.Fatal(err)
	}
	got, err = c.DevicePrograms(ctx, "7", "fsa-A")
	if err != nil || !slices.Equal(got, []string{"derp-b"}) {
		t.Errorf("after deleting derp-a's program = %v, %v, want [derp-b]: an origin with no program is not listed", got, err)
	}
	if _, err := c.Origins.Get(ctx, "7", "derp-a"); errors.Is(err, store.ErrNotFound) {
		t.Fatal("control: the stale origin should still be stored")
	}
}
