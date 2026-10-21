package handler_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// #990: the assign route and the device path share fsaprogram.Assign and
// differ only in what an existing assignment means.

// failDeleteFSAs fails every Delete of a device-scoped FSA.
type failDeleteFSAs struct{ handler.DeviceFSAWriter }

func (failDeleteFSAs) Delete(context.Context, string, string) error { return errInjected }

func TestAssignRoute_ExistingAssignment_409_ChangesNothing(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "3", "fsa-A")
	ctx := context.Background()
	before, err := f.h.DeviceFSAs.Get(ctx, "3", "fsa-A")
	if err != nil {
		t.Fatal(err)
	}

	w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusConflict || errorBody(t, w.Body.Bytes()) != "device already assigned to this FSA" {
		t.Fatalf("second assign: %d %s, want 409 device already assigned", w.Code, w.Body.String())
	}
	if devs := f.h.AdminFSAs.Devices(ctx, "fsa-A"); !slices.Equal(devs, []string{"3"}) {
		t.Errorf("devices = %q, want [3]", devs)
	}
	after, err := f.h.DeviceFSAs.Get(ctx, "3", "fsa-A")
	if err != nil || after.Href != before.Href || after.MRID != before.MRID {
		t.Errorf("scoped FSA after the refusal = %+v, %v, want %+v", after, err, before)
	}
}

// A restart or a failed write can leave the assignment without its scoped
// FSA. The device path completes it and answers as for a fresh write.
func TestDevicePath_CompletesAHalfWrittenAssignment(t *testing.T) {
	x := newDeviceControlFixture(t)
	ctx := context.Background()
	first := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	if err := x.f.h.DeviceFSAs.Delete(ctx, "1", first.FSA); err != nil {
		t.Fatal(err)
	}
	if devs := x.f.h.AdminFSAs.Devices(ctx, first.FSA); !slices.Equal(devs, []string{"1"}) {
		t.Fatalf("devices after the delete = %q, want the assignment kept", devs)
	}

	second := decodeDeviceControl(t, x.post("1", scheduledBody(t, 3000)))
	if second.FSA != first.FSA {
		t.Errorf("second call used fsa %s, want %s", second.FSA, first.FSA)
	}
	scoped, err := x.f.h.DeviceFSAs.Get(ctx, "1", first.FSA)
	if err != nil || scoped.Href != "/edev/1/fsa/"+first.FSA || scoped.MRID != first.FSA {
		t.Errorf("scoped FSA after completion = %+v, %v", scoped, err)
	}
	if devs := x.f.h.AdminFSAs.Devices(ctx, first.FSA); !slices.Equal(devs, []string{"1"}) {
		t.Errorf("devices = %q, want [1] once", devs)
	}
}

// The scoped FSA is the same undo step on the route as on the device path.
func TestAssignRoute_UndoLogNamesTheScopedFSA(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.createTemplate(t)
	logs := f.logs()
	f.h.DeviceFSAs = failDeleteFSAs{f.h.DeviceFSAs}
	f.faulty().failCreateAt = 1

	w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("assign: %d %s, want 500", w.Code, w.Body.String())
	}
	assertUndoLogged(t, logs, "3", "", "scoped_fsa")
}
