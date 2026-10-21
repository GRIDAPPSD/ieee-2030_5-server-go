package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Fault injection for the FSA control paths (#972 fix round 1).

// faultControls fronts the control store with explicit methods, so the
// issuer cannot skip its compensating writes. createFail fails a Create
// under a parent after the write took effect; deleteFail fails a Delete.
type faultControls struct {
	inner      *memory.DERControlStore
	mu         sync.Mutex
	createFail map[string]bool
	deleteFail map[string]bool
	onFail     func()
}

func (f *faultControls) Get(ctx context.Context, p, id string) (sep2.DERControl, error) {
	return f.inner.Get(ctx, p, id)
}

func (f *faultControls) List(ctx context.Context, p string, o store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	return f.inner.List(ctx, p, o)
}

func (f *faultControls) Create(ctx context.Context, p, id string, c sep2.DERControl) error {
	f.mu.Lock()
	fail := f.createFail[p]
	f.mu.Unlock()
	if !fail {
		return f.inner.Create(ctx, p, id, c)
	}
	if err := f.inner.Create(ctx, p, id, c); err != nil {
		return err
	}
	if f.onFail != nil {
		f.onFail()
	}
	return errors.New("disk full")
}

func (f *faultControls) Delete(ctx context.Context, p, id string) error {
	f.mu.Lock()
	fail := f.deleteFail[p]
	f.mu.Unlock()
	if fail {
		return errors.New("disk full")
	}
	return f.inner.Delete(ctx, p, id)
}

// faultLifecycles fails the next updateFails Updates under one parent.
type faultLifecycles struct {
	inner       *dercontrol.LifecycleStore
	mu          sync.Mutex
	updateFails map[string]int
}

func (f *faultLifecycles) Get(ctx context.Context, p, id string) (dercontrol.LifecycleRecord, error) {
	return f.inner.Get(ctx, p, id)
}

func (f *faultLifecycles) Create(ctx context.Context, p, id string, r dercontrol.LifecycleRecord) error {
	return f.inner.Create(ctx, p, id, r)
}

func (f *faultLifecycles) Update(ctx context.Context, p, id string, r dercontrol.LifecycleRecord) error {
	f.mu.Lock()
	n := f.updateFails[p]
	if n > 0 {
		f.updateFails[p] = n - 1
	}
	f.mu.Unlock()
	if n > 0 {
		return errors.New("disk full")
	}
	return f.inner.Update(ctx, p, id, r)
}

func (f *faultLifecycles) Delete(ctx context.Context, p, id string) error {
	return f.inner.Delete(ctx, p, id)
}

type faultFixture struct {
	*fsaControlFixture
	ctl     *faultControls
	lc      *faultLifecycles
	dir     string
	fsaLogs *bytes.Buffer
}

// newFaultFixture is newFSAControlFixture over an issuer that writes through
// the fault wrappers and, with persist, a persistent admin FSA store.
func newFaultFixture(t *testing.T, persist bool) *faultFixture {
	t.Helper()
	d := newDCHarness(t, ptrU32(dcPEN))
	ctx := context.Background()
	if err := d.devices.Create(ctx, "2", sep2.EndDevice{LFDI: "0000000000000000000000000000000000000002", SFDI: "2"}); err != nil {
		t.Fatal(err)
	}
	f := newTemplateFixtureWith(t, d.programs, d.devices)
	x := &faultFixture{
		ctl:     &faultControls{inner: d.controls, createFail: map[string]bool{}, deleteFail: map[string]bool{}},
		lc:      &faultLifecycles{inner: d.lifecycles, updateFails: map[string]int{}},
		fsaLogs: &bytes.Buffer{},
	}
	if persist {
		x.dir = t.TempDir()
		admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(x.dir, "fsas.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := admin.Create(ctx, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A", Description: "roof"}); err != nil {
			t.Fatal(err)
		}
		f.h.AdminFSAs = admin
	}
	f.h.Logger = slog.New(slog.NewTextHandler(x.fsaLogs, nil))
	issuer, err := dercontrol.NewIssuer(d.programs, x.ctl, x.lc, dercontrol.Config{PEN: ptrU32(dcPEN)})
	if err != nil {
		t.Fatal(err)
	}
	d.h.Issuer = issuer
	f.h.Retirer = issuer
	h := &handler.AdminFSAControlHandler{FSAs: f.h, Controls: d.h, Issuer: issuer}
	f.h.Replicas = h
	d.h.FSAControls = f.h.AdminFSAs
	f.mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls", h.HandleCreate())
	f.mux.HandleFunc("GET /api/fsas/{id}/der-programs/{derpId}/controls", h.HandleList())
	f.mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls/{controlId}/cancel", h.HandleCancel())
	tmpl := f.createTemplate(t)
	x.fsaControlFixture = &fsaControlFixture{dc: d, f: f, h: h, tid: tmpl.ID}
	return x
}

func (x *faultFixture) scope(dev string) string { return dev + "/fsa-A/" + x.tid }

func (x *faultFixture) readOnly(t *testing.T, on bool) {
	t.Helper()
	mode := os.FileMode(0o700)
	if on {
		mode = 0o500
	}
	if err := os.Chmod(x.dir, mode); err != nil {
		t.Fatal(err)
	}
}

func (x *faultFixture) copies(t *testing.T, dev string) int {
	t.Helper()
	list, err := x.dc.controls.List(context.Background(), x.scope(dev), store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func (x *faultFixture) create(t *testing.T) handler.FSAControlCreated {
	t.Helper()
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	var got handler.FSAControlCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	return got
}

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("permission checks do not apply when running as root")
	}
}

// Item 2: a record that cannot be removed after a failed fan-out is named in
// the answer and the log, and no later assignment sends it.
func TestFSAControl_RecordKeptAfterFailedFanOutIsNamedAndNotSent(t *testing.T) {
	skipAsRoot(t)
	x := newFaultFixture(t, true)
	x.f.assign(t, dcDevice, "fsa-A")
	x.f.assign(t, dcDeviceB, "fsa-A")
	x.ctl.createFail[x.scope(dcDeviceB)] = true
	x.ctl.onFail = func() { x.readOnly(t, true) }
	t.Cleanup(func() { x.readOnly(t, false) })

	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	x.readOnly(t, false)
	x.ctl.createFail = map[string]bool{}
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "record") || !strings.Contains(w.Body.String(), "/cancel") {
		t.Fatalf("create with the record left behind = %d %s, want a 500 naming the record and its cancel route", w.Code, w.Body.String())
	}
	logs := x.dc.logs.String()
	if !strings.Contains(logs, "der_fsa_control_record_kept") {
		t.Errorf("no log line names the kept record: %s", logs)
	}
	recs := x.f.h.AdminFSAs.FSAControls(context.Background(), "fsa-A", x.tid)
	if len(recs) != 1 || !strings.Contains(w.Body.String(), recs[0].MRID) {
		t.Fatalf("records = %+v; want the one kept, named in the answer", recs)
	}
	if x.copies(t, dcDevice) != 0 || x.copies(t, dcDeviceB) != 0 {
		t.Fatal("a copy survived the failed fan-out")
	}

	x.f.assign(t, "2", "fsa-A")
	if n := x.copies(t, "2"); n != 0 {
		t.Errorf("device 2 assigned after the failure holds %d copies of a control reported as failed, want 0", n)
	}
}

// Item 3: an assignment whose replica could not be undone says so in the log.
func TestFSAControl_AssignReplicaLeftBehindIsLogged(t *testing.T) {
	x := newFaultFixture(t, false)
	got := x.create(t)
	x.ctl.createFail[x.scope(dcDevice)] = true
	x.ctl.deleteFail[x.scope(dcDevice)] = true

	w := x.f.do(http.MethodPost, "/api/devices/"+dcDevice+"/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("assign = %d %s, want 500", w.Code, w.Body.String())
	}
	logs := x.fsaLogs.String()
	if !strings.Contains(logs, "assign_replica_incomplete") || !strings.Contains(logs, got.MRID) || !strings.Contains(logs, x.scope(dcDevice)) {
		t.Errorf("log = %s; want an assign_replica_incomplete line naming mRID %s and scope %s", logs, got.MRID, x.scope(dcDevice))
	}
}

// Item 4: a cancel that fails at one copy leaves the record live, and a retry
// finishes it.
func TestFSAControl_PartialCancelRetries(t *testing.T) {
	x := newFaultFixture(t, false)
	x.f.assign(t, dcDevice, "fsa-A")
	x.f.assign(t, dcDeviceB, "fsa-A")
	got := x.create(t)
	x.lc.updateFails[x.scope(dcDeviceB)] = 1

	w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "retry") {
		t.Fatalf("cancel with one copy failing = %d %s, want a 500 asking for a retry", w.Code, w.Body.String())
	}
	rec, err := x.f.h.AdminFSAs.FSAControl(context.Background(), "fsa-A", x.tid, got.ID)
	if err != nil || rec.CancelledAt != nil {
		t.Fatalf("record after the partial cancel = %+v, %v; want it still live", rec, err)
	}
	ctx := context.Background()
	if lc, _ := x.dc.lifecycles.Get(ctx, x.scope(dcDevice), got.ID); lc.CancelledAt == nil {
		t.Error("device 0's copy, before the failure, is not cancelled")
	}
	if lc, _ := x.dc.lifecycles.Get(ctx, x.scope(dcDeviceB), got.ID); lc.CancelledAt != nil {
		t.Error("device 1's copy is cancelled though its write failed")
	}

	if w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("retry = %d %s, want 200", w.Code, w.Body.String())
	}
	if lc, _ := x.dc.lifecycles.Get(ctx, x.scope(dcDeviceB), got.ID); lc.CancelledAt == nil {
		t.Error("the retry did not cancel device 1's copy")
	}
	if rec, _ := x.f.h.AdminFSAs.FSAControl(ctx, "fsa-A", x.tid, got.ID); rec.CancelledAt == nil {
		t.Error("the retry did not record the cancellation")
	}
}

// Item 5: when the record update fails after every copy is cancelled, the
// answer asks for a retry and no later assignment sends the control as live.
func TestFSAControl_RecordCancelFailureIsRetriedAndNotSent(t *testing.T) {
	skipAsRoot(t)
	x := newFaultFixture(t, true)
	x.f.assign(t, dcDevice, "fsa-A")
	got := x.create(t)
	x.readOnly(t, true)
	t.Cleanup(func() { x.readOnly(t, false) })
	w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`)
	x.readOnly(t, false)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "retry") {
		t.Fatalf("cancel with the record write failing = %d %s, want a 500 asking for a retry", w.Code, w.Body.String())
	}
	x.f.assign(t, dcDeviceB, "fsa-A")
	if n := x.copies(t, dcDeviceB); n != 0 {
		t.Errorf("device 1 assigned after the failed record write holds %d copies of a cancelled control, want 0", n)
	}
	if w := x.f.do(http.MethodPost, x.path()+"/"+got.ID+"/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("retry = %d %s, want 200", w.Code, w.Body.String())
	}
	if rec, _ := x.f.h.AdminFSAs.FSAControl(context.Background(), "fsa-A", x.tid, got.ID); rec.CancelledAt == nil {
		t.Error("the retry did not record the cancellation")
	}
}

// Item 6: assign replication refused by a grant in the device's fleet.
func TestFSAControl_AssignRefusedByAFleetGrant(t *testing.T) {
	x := newFaultFixture(t, false)
	x.dc.seedFleet(t)
	start := futureStart(900)
	x.dc.seedGrant(t, grantSpec{start: start, duration: 3600, energyWh: 100, powerW: ptrI16(5000)})
	x.create(t)

	w := x.f.do(http.MethodPost, "/api/devices/"+dcDevice+"/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	assertConflict(t, w, "fleet_window_committed", "control overlaps a live flow reservation grant of its fleet", grantMRID)
	ctx := context.Background()
	if slices.Contains(x.f.h.AdminFSAs.Devices(ctx, "fsa-A"), dcDevice) {
		t.Error("the refused assignment is recorded")
	}
	if _, err := x.dc.programs.Get(ctx, dcDevice, x.tid); err == nil {
		t.Error("the refused assignment left the copy")
	}
	if !strings.Contains(x.fsaLogs.String(), "fleet_window_committed") {
		t.Errorf("the refusal is not logged: %s", x.fsaLogs.String())
	}
}

// Item 6: copies sharing an mRID with no record to name are refused by the
// device cancel route.
func TestFSAControl_DeviceCancelOfSharedMRIDWithoutRecordRefused(t *testing.T) {
	x := newFaultFixture(t, false)
	x.f.assign(t, dcDevice, "fsa-A")
	x.f.assign(t, dcDeviceB, "fsa-A")
	got := x.create(t)
	x.dc.h.FSAControls = nil
	w := x.dc.do(t, http.MethodPost, x.dc.cancelPath(got.MRID), `{}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"fsa_control"`) {
		t.Fatalf("device cancel of a shared mRID = %d %s, want 409 fsa_control", w.Code, w.Body.String())
	}
}

// Item 6: an assigned device with no copy of the template refuses the create.
func TestFSAControl_CopyMissingRefused(t *testing.T) {
	x := newFaultFixture(t, false)
	x.f.assign(t, dcDevice, "fsa-A")
	if err := x.dc.programs.Delete(context.Background(), dcDevice, x.tid); err != nil {
		t.Fatal(err)
	}
	if err := x.f.copies.Origins.Delete(context.Background(), dcDevice, x.tid); err != nil {
		t.Fatal(err)
	}
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "holds no copy") {
		t.Fatalf("create with a copy missing = %d %s, want 409", w.Code, w.Body.String())
	}
	if recs := x.f.h.AdminFSAs.FSAControls(context.Background(), "fsa-A", x.tid); len(recs) != 0 {
		t.Errorf("records after the refusal = %d, want 0", len(recs))
	}
}

// Item 5: the unassign refused for a live control is logged.
func TestFSAControl_UnassignRefusalLogged(t *testing.T) {
	x := newFaultFixture(t, false)
	x.f.assign(t, dcDevice, "fsa-A")
	x.create(t)
	w := x.f.do(http.MethodDelete, "/api/devices/"+dcDevice+"/fsa-assignment?fsaHref=/api/fsas/fsa-A", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("unassign = %d %s, want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(x.fsaLogs.String(), "live_controls") {
		t.Errorf("the refusal is not logged: %s", x.fsaLogs.String())
	}
}
