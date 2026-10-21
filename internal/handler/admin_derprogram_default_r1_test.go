package handler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1043 round 1: a program whose stored default is not linked is repaired by
// a set, a partial clear is notified, and a cleared default is an operator
// state for the device control route.

// unlinkDefault leaves the program with no DefaultDERControlLink while its
// default stays stored, the state a failed clear delete or a failed create
// rollback leaves.
func unlinkDefault(t *testing.T, f *templateFixture, dev, id string) {
	t.Helper()
	p, err := f.copies.Programs.Get(context.Background(), dev, id)
	if err != nil {
		t.Fatal(err)
	}
	p.DefaultDERControlLink = nil
	if err := f.copies.Programs.Update(context.Background(), dev, id, p); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDeviceProgram_SetRepairsAnUnlinkedStoredDefault(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	before := defaultOf(t, f, "1", "fsa-A", created.ID)
	unlinkDefault(t, f, "1", created.ID)
	f.notifier.take()

	w := f.do(http.MethodPut, devicePath("1", created.ID), setBody)
	if w.Code != http.StatusOK {
		t.Fatalf("set over an unlinked default: %d %s", w.Code, w.Body.String())
	}
	requireDefaultServed(t, f, "1", "fsa-A", created.ID)
	d := defaultOf(t, f, "1", "fsa-A", created.ID)
	p, _ := f.copies.Programs.Get(context.Background(), "1", created.ID)
	if *d.DERControlBase.OpModMaxLimW != 4000 || d.MRID != before.MRID || p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != d.Href {
		t.Errorf("default %+v program link %+v, want 4000, mRID %s kept and the link restored", d, p.DefaultDERControlLink, before.MRID)
	}
	if got, want := f.notifier.take(), edited("/edev/1/fsa/fsa-A/derp/"+created.ID, true); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}

// failingDefaultDelete fails every Delete of a default control.
type failingDefaultDelete struct{ fsaprogram.CopyDefaultStore }

func (failingDefaultDelete) Delete(context.Context, string, string) error { return errInjected }

func TestUpdateDeviceProgram_PartialClearIsNotifiedAndRetryConverges(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	healthy := f.h.Copies.Defaults
	f.h.Copies.Defaults = failingDefaultDelete{healthy}
	f.notifier.take()

	if w := f.do(http.MethodPut, devicePath("1", created.ID), clearBody); w.Code != http.StatusInternalServerError {
		t.Fatalf("clear with a failing delete: %d %s, want 500", w.Code, w.Body.String())
	}
	if p, _ := f.copies.Programs.Get(context.Background(), "1", created.ID); p.DefaultDERControlLink != nil {
		t.Fatalf("program still links the default: %+v", p.DefaultDERControlLink)
	}
	want := edited("/edev/1/fsa/fsa-A/derp/"+created.ID, false)
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications after the partial clear = %+v, want %+v", got, want)
	}

	f.h.Copies.Defaults = healthy
	if w := f.do(http.MethodPut, devicePath("1", created.ID), clearBody); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	requireNoDefault(t, f, "1", "fsa-A", created.ID)
	if _, err := healthy.Get(context.Background(), derhref.ScopeKey("1", "fsa-A", created.ID), coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default after the retry: %v, want not found", err)
	}
}

// The state a failed clear delete leaves (program unlinked, default stored)
// is repaired by a set, not answered 200 with the wire still unlinked.
func TestUpdateDeviceProgram_SetAfterAFailedClearDeleteServesTheDefault(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.createDeviceProgram(t, "1", "fsa-A", voltSupportBody)
	healthy := f.h.Copies.Defaults
	f.h.Copies.Defaults = failingDefaultDelete{healthy}
	if w := f.do(http.MethodPut, devicePath("1", created.ID), clearBody); w.Code != http.StatusInternalServerError {
		t.Fatalf("clear: %d %s, want 500", w.Code, w.Body.String())
	}
	f.h.Copies.Defaults = healthy

	if w := f.do(http.MethodPut, devicePath("1", created.ID), setBody); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	requireDefaultServed(t, f, "1", "fsa-A", created.ID)
}

// A kind default send over an unlinked stored default links it again.
func TestDeviceControl_Default_RelinksAnUnlinkedStoredDefault(t *testing.T) {
	x := newDeviceControlFixture(t)
	first := decodeDeviceControl(t, x.post("1", defaultBody(4000)))
	unlinkDefault(t, x.f, "1", first.Program)

	decodeDeviceControl(t, x.post("1", defaultBody(2500)))
	requireDefaultServed(t, x.f, "1", first.FSA, first.Program)
	if d := defaultOf(t, x.f, "1", first.FSA, first.Program); *d.DERControlBase.OpModMaxLimW != 2500 {
		t.Errorf("default = %+v, want 2500", d)
	}
}

// failingUpdates fails every program Update, and every default Update after
// the first, so a write lands and its undo does not.
type failingProgramUpdate struct{ fsaprogram.CopyProgramStore }

func (failingProgramUpdate) Update(context.Context, string, string, sep2.DERProgram) error {
	return errInjected
}

type failingSecondDefaultUpdate struct {
	fsaprogram.CopyDefaultStore
	calls int
}

func (s *failingSecondDefaultUpdate) Update(ctx context.Context, p, id string, d sep2.DefaultDERControl) error {
	s.calls++
	if s.calls > 1 {
		return errInjected
	}
	return s.CopyDefaultStore.Update(ctx, p, id, d)
}

func TestDeviceControl_Default_PartialWriteIsNotified(t *testing.T) {
	x := newDeviceControlFixture(t)
	first := decodeDeviceControl(t, x.post("1", defaultBody(4000)))
	x.f.h.Copies.Programs = failingProgramUpdate{x.f.copies.Programs}
	x.f.h.Copies.Defaults = &failingSecondDefaultUpdate{CopyDefaultStore: x.f.copies.Defaults}
	x.f.notifier.take()

	if w := x.post("1", defaultBody(2500)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	base := fmt.Sprintf("/edev/1/fsa/%s/derp/%s", first.FSA, first.Program)
	want := []recordedNotification{{base + "/dderc", sep2.NotificationStatusDefault}, {base[:len(base)-len(first.Program)-1], sep2.NotificationStatusDefault}}
	if got := x.f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}
