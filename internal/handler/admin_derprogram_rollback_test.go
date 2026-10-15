package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #920 fix round 1: unassign removes copies, and every rollback either
// finishes or leaves only state the boot rebuild completes, and says so.

var errInjected = errors.New("injected store failure")

// faultyPrograms fails chosen writes to the DERProgram store.
type faultyPrograms struct {
	fsaprogram.CopyProgramStore
	mu           sync.Mutex
	creates      int
	failCreateAt int             // 1-based Create call that fails; 0 never
	failDelete   map[string]bool // device ids whose Delete fails
	onFail       func()          // runs when the Create fails
	gate         chan struct{}   // when set, Create on gateDevice waits for it
	gateDevice   string
	entered      chan struct{}
}

func (s *faultyPrograms) Create(ctx context.Context, parentID, id string, p sep2.DERProgram) error {
	s.mu.Lock()
	s.creates++
	n := s.creates
	gate, entered := s.gate, s.entered
	s.mu.Unlock()
	if gate != nil && parentID == s.gateDevice {
		close(entered)
		<-gate
	}
	if n == s.failCreateAt {
		if s.onFail != nil {
			s.onFail()
		}
		return errInjected
	}
	return s.CopyProgramStore.Create(ctx, parentID, id, p)
}

func (s *faultyPrograms) Delete(ctx context.Context, parentID, id string) error {
	if s.failDelete[parentID] {
		return errInjected
	}
	return s.CopyProgramStore.Delete(ctx, parentID, id)
}

func (f *templateFixture) faulty() *faultyPrograms {
	fp := &faultyPrograms{CopyProgramStore: f.copies.Programs, failDelete: map[string]bool{}}
	f.h.Copies.Programs = fp
	return fp
}

func (f *templateFixture) logs() *bytes.Buffer {
	var buf bytes.Buffer
	f.h.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	return &buf
}

func (f *templateFixture) createTemplate(t *testing.T) templateView {
	t.Helper()
	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("create template: %d %s", w.Code, w.Body.String())
	}
	var v templateView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func errorBody(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return e.Error
}

// assertEveryProgramHasItsOrigin fails on a program under one of the
// fixture's devices that is not a whole template copy: the orphan an
// out-of-order undo leaves.
func assertEveryProgramHasItsOrigin(t *testing.T, f *templateFixture) {
	t.Helper()
	ctx := context.Background()
	for _, dev := range []string{"1", "2", "3"} {
		list, err := f.copies.Programs.List(ctx, dev, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list.Items {
			id := p.Href[strings.LastIndex(p.Href, "/")+1:]
			if _, err := f.copies.Origins.Get(ctx, dev, id); err != nil {
				t.Errorf("device %s program %s has no origin: %v", dev, p.Href, err)
			}
		}
	}
}

func (f *templateFixture) reconcile(t *testing.T) {
	t.Helper()
	if err := fsaprogram.ReconcileCopies(context.Background(), f.h.AdminFSAs, f.copies, t.Logf); err != nil {
		t.Fatalf("ReconcileCopies: %v", err)
	}
}

func TestUnassignDeviceFSA_RemovesTheTemplateCopies(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	a, b := f.createTemplate(t), f.createTemplate(t)
	ctx := context.Background()
	fixture := sep2.DERProgram{MRID: "0000000000000000000000000000BEEF"}
	fixture.Href = "/edev/1/fsa/fx/derp/fixture-0"
	if err := f.copies.Programs.Create(ctx, "1", "fixture-0", fixture); err != nil {
		t.Fatal(err)
	}
	f.notifier.take()

	if w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unassign: %d %s", w.Code, w.Body.String())
	}
	for _, v := range []templateView{a, b} {
		if _, err := f.copies.Programs.Get(ctx, "1", v.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("program %s err = %v, want ErrNotFound", v.ID, err)
		}
		if _, err := f.copies.Defaults.Get(ctx, "1/fsa-A/"+v.ID, coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("default %s err = %v, want ErrNotFound", v.ID, err)
		}
		if _, err := f.copies.Origins.Get(ctx, "1", v.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("origin %s err = %v, want ErrNotFound", v.ID, err)
		}
		assertDeviceCopy(t, f.copies, "2", "fsa-A", v)
	}
	if got, err := f.copies.Programs.Get(ctx, "1", "fixture-0"); err != nil || got.MRID != fixture.MRID {
		t.Errorf("fixture program = %+v, %v, want it untouched", got, err)
	}
	if _, err := f.h.DeviceFSAs.Get(ctx, "1", "fsa-A"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("device FSA err = %v, want ErrNotFound", err)
	}
	if devs := f.h.AdminFSAs.Devices(ctx, "fsa-A"); !slices.Equal(devs, []string{"2"}) {
		t.Errorf("devices = %q, want [2]", devs)
	}
	first, second := a.ID, b.ID
	if second < first {
		first, second = second, first
	}
	want := []recordedNotification{
		{"/edev/1/fsa/fsa-A/derp/" + first, sep2.NotificationStatusCanceledDeleted},
		{"/edev/1/fsa/fsa-A/derp/" + second, sep2.NotificationStatusCanceledDeleted},
		{"/edev/1/fsa/fsa-A/derp", sep2.NotificationStatusDefault},
		{"/edev/1/fsa", sep2.NotificationStatusDefault},
	}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}

// An unassign that arrives while a create is copying to the device waits for
// it, then removes the copy: no copy outlives the assignment.
func TestUnassignDeviceFSA_WaitsForACreateInFlight(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	fp := f.faulty()
	fp.gate, fp.gateDevice, fp.entered = make(chan struct{}), "1", make(chan struct{})

	created := make(chan int, 1)
	go func() {
		created <- f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody).Code
	}()
	<-fp.entered
	unassigned := make(chan int, 1)
	go func() {
		unassigned <- f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", "").Code
	}()
	early := 0
	select {
	case early = <-unassigned:
		t.Errorf("unassign finished (%d) while the create held the copy half written", early)
	case <-time.After(200 * time.Millisecond):
	}
	close(fp.gate)
	if code := <-created; code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if early == 0 {
		if code := <-unassigned; code != http.StatusNoContent {
			t.Fatalf("unassign: %d", code)
		}
	}
	assertNoDeviceCopy(t, f.copies, "1")
	tmpls := f.h.AdminFSAs.Templates(context.Background(), "fsa-A")
	if len(tmpls) != 1 {
		t.Fatalf("templates = %d, want 1", len(tmpls))
	}
	if _, err := f.copies.Origins.Get(context.Background(), "1", tmpls[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("origin err = %v, want ErrNotFound", err)
	}
}

// A removal that fails keeps the device assigned, says so, and the boot
// rebuild restores the copy.
func TestUnassignDeviceFSA_FailedRemovalKeepsTheAssignment(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	v := f.createTemplate(t)
	logs := f.logs()
	f.faulty().failDelete["1"] = true

	w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unassign: %d %s, want 500", w.Code, w.Body.String())
	}
	if msg := errorBody(t, w.Body.Bytes()); msg != "unassign failed; device 1 stays assigned to fsa fsa-A, and its template copies are restored at the next restart" {
		t.Errorf("message = %q", msg)
	}
	if devs := f.h.AdminFSAs.Devices(context.Background(), "fsa-A"); !slices.Equal(devs, []string{"1"}) {
		t.Errorf("devices = %q, want [1]", devs)
	}
	assertUndoLogged(t, logs, "1", v.ID, fsaprogram.PartProgram)
	f.reconcile(t)
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
}

func assertUndoLogged(t *testing.T, logs *bytes.Buffer, device, template, part string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["event"] != "der_program_template_undo_failed" {
			continue
		}
		if rec["device"] == device && rec["template"] == template && rec["part"] == part {
			return
		}
	}
	t.Errorf("no undo log line for device %q template %q part %q in:\n%s", device, template, part, logs.String())
}

// Device 2's copy fails, then device 1's copy cannot be undone. The undo
// stops there: the template stays, the reply says so, no program is left
// without its origin, and the boot rebuild completes device 2.
func TestCreateProgramTemplate_FailedCopyUndoKeepsTheTemplate(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	logs := f.logs()
	fp := f.faulty()
	fp.failCreateAt = 2
	fp.failDelete["1"] = true

	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create: %d %s, want 500", w.Code, w.Body.String())
	}
	tmpls := f.h.AdminFSAs.Templates(context.Background(), "fsa-A")
	if len(tmpls) != 1 {
		t.Fatalf("templates = %d, want the template kept", len(tmpls))
	}
	id := tmpls[0].ID
	want := fmt.Sprintf("program template write failed; template %s was kept on fsa fsa-A and its device copies are completed at the next restart", id)
	if msg := errorBody(t, w.Body.Bytes()); msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
	assertUndoLogged(t, logs, "1", id, fsaprogram.PartProgram)
	assertEveryProgramHasItsOrigin(t, f)

	f.reconcile(t)
	v := templateView{ID: id, MRID: tmpls[0].MRID, Description: tmpls[0].Description, Primacy: tmpls[0].Primacy, Version: tmpls[0].Version}
	v.DefaultControl.MRID = tmpls[0].DefaultMRID
	limit := uint16(*tmpls[0].DefaultControlBase.OpModMaxLimW)
	v.DefaultControl.DERControlBase.OpModMaxLimW = &limit
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
	assertDeviceCopy(t, f.copies, "2", "fsa-A", v)
}

// Every copy is undone but the template delete fails: the reply names the
// kept template, and the boot rebuild copies it to every assigned device.
func TestCreateProgramTemplate_FailedTemplateDeleteIsReported(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	f := newTemplateFixture(t)
	dir := t.TempDir()
	admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	f.h.AdminFSAs = admin
	if err := admin.Create(context.Background(), "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	logs := f.logs()
	fp := f.faulty()
	fp.failCreateAt = 2
	fp.onFail = func() {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Error(err)
		}
	}

	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create: %d %s, want 500", w.Code, w.Body.String())
	}
	tmpls := admin.Templates(context.Background(), "fsa-A")
	if len(tmpls) != 1 {
		t.Fatalf("templates = %d, want the template whose delete failed", len(tmpls))
	}
	if msg := errorBody(t, w.Body.Bytes()); !strings.Contains(msg, "template "+tmpls[0].ID+" was kept on fsa fsa-A") {
		t.Errorf("message = %q, want it to name the kept template", msg)
	}
	assertUndoLogged(t, logs, "", tmpls[0].ID, "template")
	assertNoDeviceCopy(t, f.copies, "1")
	assertNoDeviceCopy(t, f.copies, "2")

	f.reconcile(t)
	for _, dev := range []string{"1", "2"} {
		if _, err := f.copies.Programs.Get(context.Background(), dev, tmpls[0].ID); err != nil {
			t.Errorf("device %s after the rebuild: %v", dev, err)
		}
	}
}

// A copy write that fails partway through an assign undoes the earlier copy,
// the device FSA and the assignment, and notifies nothing.
func TestAssignDeviceFSA_FailedCopyUndoesTheWholeAssign(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	a, b := f.createTemplate(t), f.createTemplate(t)
	f.notifier.take()
	f.faulty().failCreateAt = 2

	w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusInternalServerError || errorBody(t, w.Body.Bytes()) != "program write failed" {
		t.Fatalf("assign: %d %s, want 500 with the fixed message", w.Code, w.Body.String())
	}
	ctx := context.Background()
	if devs := f.h.AdminFSAs.Devices(ctx, "fsa-A"); len(devs) != 0 {
		t.Errorf("devices = %q, want none", devs)
	}
	if _, err := f.h.DeviceFSAs.Get(ctx, "3", "fsa-A"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("device FSA err = %v, want ErrNotFound", err)
	}
	assertNoDeviceCopy(t, f.copies, "3")
	for _, v := range []templateView{a, b} {
		if _, err := f.copies.Origins.Get(ctx, "3", v.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("origin %s err = %v, want ErrNotFound", v.ID, err)
		}
		if _, err := f.copies.Defaults.Get(ctx, "3/fsa-A/"+v.ID, coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("default %s err = %v, want ErrNotFound", v.ID, err)
		}
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications = %+v, want none", got)
	}
}

// The earlier copy cannot be undone: the device stays assigned, the reply
// says so, and the boot rebuild completes both copies.
func TestAssignDeviceFSA_FailedUndoKeepsTheAssignment(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	a, b := f.createTemplate(t), f.createTemplate(t)
	logs := f.logs()
	fp := f.faulty()
	fp.failCreateAt = 2
	fp.failDelete["3"] = true

	w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("assign: %d %s, want 500", w.Code, w.Body.String())
	}
	if msg := errorBody(t, w.Body.Bytes()); msg != "assign failed; device 3 stays assigned to fsa fsa-A, and its template copies are completed at the next restart" {
		t.Errorf("message = %q", msg)
	}
	first := a.ID
	if b.ID < first {
		first = b.ID
	}
	assertUndoLogged(t, logs, "3", first, fsaprogram.PartProgram)
	if devs := f.h.AdminFSAs.Devices(context.Background(), "fsa-A"); !slices.Equal(devs, []string{"3"}) {
		t.Errorf("devices = %q, want [3]", devs)
	}
	assertEveryProgramHasItsOrigin(t, f)
	f.reconcile(t)
	assertDeviceCopy(t, f.copies, "3", "fsa-A", a)
	assertDeviceCopy(t, f.copies, "3", "fsa-A", b)
}
