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
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #991: the audit event names the DER control routes write are part of the
// operator's log contract, so each is pinned by a captured log line, read as
// the JSON a log consumer parses.

// auditLines returns the JSON log lines in buf whose event is event.
func auditLines(t *testing.T, buf *bytes.Buffer, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["event"] == event {
			out = append(out, rec)
		}
	}
	return out
}

// oneAudit fails unless buf holds exactly one line for event, and returns it.
func oneAudit(t *testing.T, buf *bytes.Buffer, event string) map[string]any {
	t.Helper()
	lines := auditLines(t, buf, event)
	if len(lines) != 1 {
		t.Fatalf("%d lines with event %q, want 1, in:\n%s", len(lines), event, buf.String())
	}
	return lines[0]
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

// wantFields fails on a field of rec that differs from want.
func wantFields(t *testing.T, rec map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("field %q = %v (%T), want %v in %v", k, rec[k], rec[k], v, rec)
		}
	}
}

func TestAuditEvents_DeviceRoute_RefusalsKeepTheirEventNames(t *testing.T) {
	x := newDeviceControlFixture(t)
	var ctl, fsa bytes.Buffer
	x.dc.h.Logger, x.f.h.Logger = jsonLogger(&ctl), jsonLogger(&fsa)

	if w := x.post("1", `{"type":"maxLimW","maxLimW":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no kind = %d %s, want 400", w.Code, w.Body.String())
	}
	rec := oneAudit(t, &ctl, "der_control_device_control_refused")
	wantFields(t, rec, map[string]any{"level": "WARN", "msg": "admin: DER control request refused", "code": "kind_required", "status": float64(400)})
	if len(auditLines(t, &ctl, "der_control_device_control_failed")) != 0 {
		t.Error("a 400 refusal logged a _failed event")
	}

	x.dc.seedProgram(t, "1", "0", "zero", "ZERO-PROGRAM", 0, true)
	if w := x.post("1", defaultBody(4000)); w.Code != http.StatusConflict {
		t.Fatalf("no outrank = %d %s, want 409", w.Code, w.Body.String())
	}
	rec = oneAudit(t, &fsa, "der_device_control_refused")
	wantFields(t, rec, map[string]any{
		"level": "WARN", "msg": "admin: DER control request refused", "code": "primacy_cannot_outrank", "status": float64(409),
		"device": "1", "program_href": "/edev/1/fsa/0/derp/zero",
	})
	for _, k := range []string{"remote_addr", "admission"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("refusal line lacks %q: %v", k, rec)
		}
	}
}

func TestAuditEvents_DeviceRoute_UnrepresentableDeviceIDIsRefusedUnderItsEvent(t *testing.T) {
	x := newDeviceControlFixture(t)
	var fsa bytes.Buffer
	x.f.h.Logger = jsonLogger(&fsa)
	const odd = "dev one"
	if err := x.dc.devices.Create(context.Background(), odd, sep2.EndDevice{LFDI: "0000000000000000000000000000000000000009", SFDI: "9"}); err != nil {
		t.Fatal(err)
	}
	w := x.f.do(http.MethodPost, "/api/devices/dev%20one/controls", scheduledBody(t, 5000))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "device id cannot form a program href") {
		t.Fatalf("odd device id = %d %s, want the 409 refusal", w.Code, w.Body.String())
	}
	rec := oneAudit(t, &fsa, "der_device_control_refused")
	wantFields(t, rec, map[string]any{"level": "WARN", "code": "device_href_invalid", "status": float64(409)})
}

func TestAuditEvents_DeviceRoute_FailureAndIncompleteKeepTheirEventNames(t *testing.T) {
	x := newDeviceControlFixture(t)
	var ctl, fsa bytes.Buffer
	x.dc.h.Logger, x.f.h.Logger = jsonLogger(&ctl), jsonLogger(&fsa)
	x.dc.h.Issuer = failingIssuer{err: errors.New("boom")}
	// The issue fails after the program is written; its undo cannot delete.
	x.f.faulty().failDelete["1"] = true

	if w := x.post("1", scheduledBody(t, 5000)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	rec := oneAudit(t, &ctl, "der_control_device_control_failed")
	wantFields(t, rec, map[string]any{"level": "ERROR", "msg": "admin: DER control request failed", "status": float64(500)})
	rec = oneAudit(t, &fsa, "device_control_incomplete")
	wantFields(t, rec, map[string]any{"level": "ERROR", "msg": "admin: device control left partly applied", "path": "/api/devices/1/controls"})
	if len(auditLines(t, &fsa, "der_program_template_undo_failed")) == 0 {
		t.Error("the failed undo was not logged under der_program_template_undo_failed")
	}
}

func TestAuditEvents_TemplateCreate_IncompleteKeepsItsEventName(t *testing.T) {
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	logs := f.logs()
	fp := f.faulty()
	fp.failCreateAt = 2
	fp.failDelete["1"] = true

	if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody); w.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", w.Code, w.Body.String())
	}
	tmpls := f.h.AdminFSAs.Templates(context.Background(), "fsa-A")
	if len(tmpls) != 1 {
		t.Fatalf("templates = %d, want the template kept", len(tmpls))
	}
	rec := oneAudit(t, logs, "der_program_template_incomplete")
	wantFields(t, rec, map[string]any{
		"level": "ERROR", "msg": "admin: DER program template create left partly applied", "fsa": "fsa-A", "template": tmpls[0].ID,
	})
}

func TestAuditEvents_FSAControl_RefusedAndFailedKeepTheirEventNames(t *testing.T) {
	x := newFSAControlFixture(t)
	var ctl bytes.Buffer
	x.dc.h.Logger = jsonLogger(&ctl)

	if w := x.f.do(http.MethodPost, x.path(), `{"durationSeconds":600}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no type = %d %s, want 400", w.Code, w.Body.String())
	}
	rec := oneAudit(t, &ctl, "der_control_fsa_control_refused")
	wantFields(t, rec, map[string]any{"level": "WARN", "status": float64(400)})

	x.f.assign(t, dcDevice, "fsa-A")
	w := x.f.do(http.MethodPost, x.path(), fsaControlBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ctl.Reset()
	if w := x.f.do(http.MethodPost, x.path()+"/nope/cancel", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown = %d %s, want 404", w.Code, w.Body.String())
	}
	if len(auditLines(t, &ctl, "der_control_fsa_cancel_refused")) != 1 {
		t.Errorf("no der_control_fsa_cancel_refused line in:\n%s", ctl.String())
	}
}

// readOnlyOnCreate is a device FSA store whose Create makes dir read-only and
// then fails, so the undo of the admin writes before it cannot persist.
type readOnlyOnCreate struct {
	failingScopedFSAs
	dir string
}

func (s readOnlyOnCreate) Create(context.Context, string, string, sep2.FunctionSetAssignments) error {
	if err := os.Chmod(s.dir, 0o500); err != nil {
		return err
	}
	return errors.New("scoped store down")
}

func TestAuditEvents_DeviceOnlyFSA_IncompleteKeepsItsEventName(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	x := newDeviceControlFixture(t)
	dir := t.TempDir()
	admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	x.f.h.AdminFSAs = admin
	x.f.h.DeviceFSAs = readOnlyOnCreate{failingScopedFSAs{memory.NewScopedStore[sep2.FunctionSetAssignments]()}, dir}
	var fsa bytes.Buffer
	x.f.h.Logger = jsonLogger(&fsa)

	if w := x.post("1", scheduledBody(t, 5000)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	rec := oneAudit(t, &fsa, "device_only_fsa_incomplete")
	wantFields(t, rec, map[string]any{"level": "ERROR", "msg": "admin: device-only FSA left partly applied", "device": "1"})
}
