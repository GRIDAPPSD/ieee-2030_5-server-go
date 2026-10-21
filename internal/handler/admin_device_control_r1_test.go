package handler_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #970 fix round 1.

// deviceState is everything a refused device-control request must leave
// alone for dev: admin FSAs, the device's own FSAs, its programs with their
// primacy and version, and their default controls.
func deviceState(t *testing.T, x *deviceControlFixture, dev string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, id := range x.f.h.AdminFSAs.IDs(ctx) {
		fmt.Fprintf(&b, "admin %s devices=%v only=%q\n", id, x.f.h.AdminFSAs.Devices(ctx, id), x.f.h.AdminFSAs.DeviceOnlyDevice(id))
	}
	list, err := x.f.copies.Programs.List(ctx, dev, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		v := -1
		if p.Version != nil {
			v = int(*p.Version)
		}
		fmt.Fprintf(&b, "program %s primacy=%d version=%d\n", p.Href, p.Primacy, v)
	}
	scoped, err := x.f.h.DeviceFSAs.Get(ctx, dev, "0")
	fmt.Fprintf(&b, "scoped0 %v %v\n", scoped.Href, err)
	return b.String()
}

func TestDeviceControl_AFailedIssueLeavesNothingBehind(t *testing.T) {
	refused := &dercontrol.RefusalError{Code: dercontrol.RefusalEnded}
	cases := []struct {
		name   string
		issuer error
		cancel bool
		status int
	}{
		{"refusal", refused, false, http.StatusConflict},
		{"internal failure", errors.New("store exploded"), false, http.StatusInternalServerError},
		{"client gone", nil, true, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		for _, reuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reuse=%v", tc.name, reuse), func(t *testing.T) {
				x := newDeviceControlFixture(t)
				if reuse {
					decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
					x.dc.seedProgram(t, "1", "0", "late", "LATE-PROGRAM", 100, true)
				}
				before := deviceState(t, x, "1")
				x.f.notifier.take()
				controlsBefore, _ := x.dc.storedCounts(t)

				ctx := context.Background()
				if tc.cancel {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				} else {
					x.dc.h.Issuer = failingIssuer{err: tc.issuer}
				}
				req := httptest.NewRequest(http.MethodPost, "/api/devices/1/controls", strings.NewReader(scheduledBody(t, 3000))).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				x.mux.ServeHTTP(w, req)
				if w.Code != tc.status {
					t.Fatalf("status = %d %s, want %d", w.Code, w.Body.String(), tc.status)
				}

				if after := deviceState(t, x, "1"); after != before {
					t.Errorf("a failed request changed the stores:\n--- before\n%s--- after\n%s", before, after)
				}
				if got := x.f.notifier.take(); len(got) != 0 {
					t.Errorf("a failed request notified %+v", got)
				}
				if c, _ := x.dc.storedCounts(t); c != controlsBefore {
					t.Errorf("controls stored = %d, want %d", c, controlsBefore)
				}
			})
		}
	}
}

func TestDeviceControl_PrimacyUsesOnlyValuesTheSchemaAllows(t *testing.T) {
	cases := []struct {
		other uint8
		want  uint8
	}{
		{65, 2}, {64, 2}, {50, 2}, {3, 2}, {2, 1}, {1, 0},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint("other=", tc.other), func(t *testing.T) {
			x := newDeviceControlFixture(t)
			x.dc.seedProgram(t, "1", "0", "other", "OTHER-PROGRAM", tc.other, true)
			got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
			if got.Primacy != tc.want {
				t.Fatalf("primacy = %d, want %d", got.Primacy, tc.want)
			}
			// The program the path wrote can be edited at its own primacy.
			w := x.f.do(http.MethodPut, "/api/devices/1/der-programs/"+got.Program, fmt.Sprintf(`{"description":"edited","primacy":%d}`, got.Primacy))
			if w.Code != http.StatusOK {
				t.Errorf("edit at the path's own primacy = %d %s, want 200", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeviceControl_ReusedReservedPrimacyIsRepicked(t *testing.T) {
	x := newDeviceControlFixture(t)
	first := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	p, err := x.f.copies.Programs.Get(context.Background(), "1", first.Program)
	if err != nil {
		t.Fatal(err)
	}
	p.Primacy = 49 // as an earlier release wrote it
	if err := x.f.copies.Programs.Update(context.Background(), "1", first.Program, p); err != nil {
		t.Fatal(err)
	}
	second := decodeDeviceControl(t, x.post("1", scheduledBody(t, 3000)))
	if second.Primacy != 191 || !strings.Contains(second.PrimacyReason, "re-picked") {
		t.Errorf("primacy = %d (%s), want 191 re-picked", second.Primacy, second.PrimacyReason)
	}
}

func TestDeviceControl_OutranksTheLowestOfSeveralOtherPrograms(t *testing.T) {
	for _, order := range [][2]uint8{{100, 150}, {150, 100}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			x := newDeviceControlFixture(t)
			x.dc.seedProgram(t, "1", "0", "a", "OTHER-A", order[0], true)
			x.dc.seedProgram(t, "1", "7", "b", "OTHER-B", order[1], true)
			got := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
			if got.Primacy != 99 {
				t.Errorf("primacy = %d, want 99: below the lowest of 100 and 150", got.Primacy)
			}
		})
	}
}

func TestDeviceControl_Default_NoOutrankIsRefusedAndLogged(t *testing.T) {
	x := newDeviceControlFixture(t)
	var logs bytes.Buffer
	x.f.h.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	x.dc.seedProgram(t, "1", "0", "zero", "ZERO-PROGRAM", 0, true)
	w := x.post("1", defaultBody(4000))
	if code := decodeCode(t, w, http.StatusConflict); code != "primacy_cannot_outrank" {
		t.Errorf("code = %q", code)
	}
	x.nothingWritten(t, "1")
	if !strings.Contains(logs.String(), "primacy_cannot_outrank") {
		t.Errorf("no log line for the refusal: %q", logs.String())
	}
}

func TestDeviceControl_FailureLogsUseTheDeviceControlOp(t *testing.T) {
	x := newDeviceControlFixture(t)
	x.dc.h.Issuer = failingIssuer{err: errors.New("boom")}
	x.post("1", scheduledBody(t, 5000))
	logs := x.dc.logs.String()
	if !strings.Contains(logs, "der_control_device_control_failed") || strings.Contains(logs, "der_control_create_") {
		t.Errorf("log = %q, want der_control_device_control_failed and no der_control_create_*", logs)
	}
}

// failingScopedFSAs is a device FSA store whose Create fails.
type failingScopedFSAs struct {
	*memory.ScopedStore[sep2.FunctionSetAssignments]
}

func (failingScopedFSAs) Create(context.Context, string, string, sep2.FunctionSetAssignments) error {
	return errors.New("scoped store down")
}

func TestDeviceControl_FSAMaterializeFailureRollsTheAssignmentBack(t *testing.T) {
	x := newDeviceControlFixture(t)
	x.f.h.DeviceFSAs = failingScopedFSAs{memory.NewScopedStore[sep2.FunctionSetAssignments]()}
	before := len(x.f.h.AdminFSAs.IDs(context.Background()))
	if w := x.post("1", scheduledBody(t, 5000)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	if n := len(x.f.h.AdminFSAs.IDs(context.Background())); n != before {
		t.Errorf("%d admin FSAs, want %d: the created FSA and its assignment must be undone", n, before)
	}
	x.nothingWritten(t, "1")
}

// failSecondDefaultGet fails the second Get of a default control, which is
// the read after an edit.
type failSecondDefaultGet struct {
	fsaprogram.CopyDefaultStore
	gets int
}

func (f *failSecondDefaultGet) Get(ctx context.Context, p, id string) (sep2.DefaultDERControl, error) {
	f.gets++
	if f.gets == 2 {
		return sep2.DefaultDERControl{}, errors.New("default read failed")
	}
	return f.CopyDefaultStore.Get(ctx, p, id)
}

func TestDeviceControl_Default_ReadAfterEditFailureLogsItsCauseAndUndoesTheEdit(t *testing.T) {
	x := newDeviceControlFixture(t)
	var logs bytes.Buffer
	x.f.h.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	first := decodeDeviceControl(t, x.post("1", defaultBody(4000)))
	x.f.h.Copies.Defaults = &failSecondDefaultGet{CopyDefaultStore: x.f.h.Copies.Defaults}
	if w := x.post("1", defaultBody(2000)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "default read failed") || strings.Contains(logs.String(), "default control missing") {
		t.Errorf("log = %q, want the real cause", logs.String())
	}
	x.f.h.Copies.Defaults = x.f.copies.Defaults
	d, err := x.f.copies.Defaults.Get(context.Background(), fsaprogram.DefaultScope("1", first.FSA, first.Program), coresingleton.SingletonKey)
	if err != nil || uint16(*d.DERControlBase.OpModMaxLimW) != 4000 {
		t.Errorf("default after the failed call = %+v, %v, want the earlier 4000", d, err)
	}
}

// The restart case with the boot rebuild: the stores a data dir keeps are
// reloaded, Reconcile and ReconcileCopies run as at boot, and the control,
// the default and the origin must all be there.
func TestDeviceControl_RestartWithBootRebuildKeepsControlDefaultAndOrigin(t *testing.T) {
	dir := t.TempDir()
	build := func() *deviceControlFixture {
		t.Helper()
		programs, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dir, "programs.json"))
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](filepath.Join(dir, "defaults.json"), "DefaultDERControl")
		if err != nil {
			t.Fatal(err)
		}
		origins, err := memory.NewProgramOriginStore(filepath.Join(dir, "origins.json"))
		if err != nil {
			t.Fatal(err)
		}
		admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
		if err != nil {
			t.Fatal(err)
		}
		controls, err := memory.NewDERControlStoreWithPersistence(filepath.Join(dir, "controls.json"))
		if err != nil {
			t.Fatal(err)
		}
		d := newDCHarnessWith(t, ptrU32(dcPEN), programs)
		d.controls = controls
		issuer, err := dercontrol.NewIssuer(programs, controls, d.lifecycles, dercontrol.Config{PEN: ptrU32(dcPEN)})
		if err != nil {
			t.Fatal(err)
		}
		d.h.Issuer, d.h.Controls = issuer, controls
		d.h.Ledger = sources.NewLedger(d.devices, d.managers, d.grants, d.grantMarks, controls, d.lifecycles)
		f := newTemplateFixtureWith(t, programs, d.devices)
		f.h.AdminFSAs = admin
		f.h.Copies = &fsaprogram.Copies{Programs: programs, Defaults: defaults, Origins: origins}
		f.copies = *f.h.Copies
		return wireDeviceControl(d, f)
	}

	x := build()
	sent := decodeDeviceControl(t, x.post("1", scheduledBody(t, 5000)))
	decodeDeviceControl(t, x.post("1", defaultBody(4000)))

	y := build()
	ctx := context.Background()
	if err := fsaprogram.Reconcile(ctx, y.f.h.AdminFSAs, y.f.h.DeviceFSAs); err != nil {
		t.Fatal(err)
	}
	if err := fsaprogram.ReconcileCopies(ctx, y.f.h.AdminFSAs, y.f.copies, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.f.h.DeviceFSAs.Get(ctx, "1", sent.FSA); err != nil {
		t.Errorf("rebuild did not restore the device's FSA: %v", err)
	}
	o, err := y.f.copies.Origins.Get(ctx, "1", sent.Program)
	if err != nil || o.Kind != memory.ProgramOriginDevice || o.FSAID != sent.FSA {
		t.Errorf("origin after rebuild = %+v, %v", o, err)
	}
	d, err := y.f.copies.Defaults.Get(ctx, fsaprogram.DefaultScope("1", sent.FSA, sent.Program), coresingleton.SingletonKey)
	if err != nil || d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || uint16(*d.DERControlBase.OpModMaxLimW) != 4000 {
		t.Errorf("default after rebuild = %+v, %v, want maxLimW 4000", d, err)
	}
	page, err := y.dc.controls.List(ctx, "1/"+sent.FSA+"/"+sent.Program, store.ListOptions{Unbounded: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].DERControlBase == nil || page.Items[0].DERControlBase.OpModMaxLimW == nil ||
		uint16(*page.Items[0].DERControlBase.OpModMaxLimW) != 5000 {
		t.Errorf("controls after restart = %+v, %v, want the maxLimW 5000 control", page.Items, err)
	}
	if ids := programIDsInFSA(t, y.f, "1", sent.FSA); !slices.Equal(ids, []string{sent.Program}) {
		t.Errorf("programs after rebuild = %v", ids)
	}
}
