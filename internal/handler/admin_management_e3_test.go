package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1128 E3: a managed-side rekey is refused while the old LFDI still has a
// record, and pairing a device that has connected directly warns.

func rekeyManaged(t *testing.T, h interface {
	HandleRekeyManagementPair() http.HandlerFunc
}, from, to string) (int, string) {
	t.Helper()
	w := doJSON(t, h.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"managed","from":"`+from+`","to":"`+to+`"}`)
	return w.Code, w.Body.String()
}

func TestRekeyManaged_RefusedWhileOldRecordExists(t *testing.T) {
	t.Parallel()
	h, devs := healthHandler(t, activity.New())
	seedRecord(t, devs, "2", mgHandlerChildA)

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", code, body)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error == "" {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if want := "old LFDI " + mgHandlerChildA; len(e.Error) < len(want) || e.Error[:len(want)] != want {
		t.Errorf("reason = %q, want it to name the old record %q", e.Error, want)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); m != mgHandlerManagerA {
		t.Errorf("ManagerOf(old) = %q, want %q unmoved", m, mgHandlerManagerA)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerManagerB); m != "" {
		t.Errorf("ManagerOf(new) = %q, want no pair", m)
	}
}

func TestRekeyManaged_AllowedWithNoOldRecord(t *testing.T) {
	t.Parallel()
	h, _ := healthHandler(t, activity.New())

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, body)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerManagerB); m != mgHandlerManagerA {
		t.Errorf("ManagerOf(new) = %q, want %q", m, mgHandlerManagerA)
	}
}

func TestCreateManagementPair_WarnsForDirectlyConnectedDevice(t *testing.T) {
	t.Parallel()
	seen := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return seen })
	h, _ := healthHandler(t, rec)
	rec.Record(mgHandlerManagerB)

	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerManagerB+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var got struct {
		ManagedLFDI string       `json:"managedLFDI"`
		Managed     healthMember `json:"managed"`
		Warning     string       `json:"warning"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ManagedLFDI != mgHandlerManagerB {
		t.Errorf("managedLFDI = %q", got.ManagedLFDI)
	}
	if got.Managed.LastSeen == nil || *got.Managed.LastSeen != "2026-10-26T08:00:00Z" {
		t.Errorf("managed lastSeen = %v, want 2026-10-26T08:00:00Z", got.Managed.LastSeen)
	}
	if got.Warning == "" {
		t.Error("warning empty, want one for a device with a lastSeen")
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerManagerB); m != mgHandlerManagerA {
		t.Errorf("pair not created: ManagerOf = %q", m)
	}
}

func TestCreateManagementPair_NoWarningForQuietDevice(t *testing.T) {
	t.Parallel()
	h, _ := healthHandler(t, activity.New())

	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+mgHandlerManagerA+`","managedLFDI":"`+mgHandlerManagerB+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["warning"]; ok {
		t.Errorf("warning present for a never-seen device: %s", raw["warning"])
	}
}

// #1128 fix round: the old LFDI owns more than its record.

func mirrorHandler(t *testing.T, rec *activity.Recorder) (*handler.AdminManagementHandler, *memory.Store[sep2.MirrorUsagePoint]) {
	t.Helper()
	h, _ := healthHandler(t, rec)
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	h.MirrorUsagePoints = mups
	return h, mups
}

func addMirror(t *testing.T, mups *memory.Store[sep2.MirrorUsagePoint], lfdi string) {
	t.Helper()
	mup := sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/1"}, DeviceLFDI: lfdi}
	if err := mups.Create(context.Background(), "1", mup); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
}

func TestRekeyManaged_RefusedWhileOldMirrorSurvivesRecordDelete(t *testing.T) {
	t.Parallel()
	h, mups := mirrorHandler(t, activity.New())
	// The reviewer's sequence: the record is gone, the mirror is not.
	addMirror(t, mups, mgHandlerChildA)

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", code, body)
	}
	if !strings.Contains(body, "MirrorUsagePoint") || strings.Contains(body, "EndDevice record") {
		t.Errorf("reason = %s, want it to name the mirror and not a record", body)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); m != mgHandlerManagerA {
		t.Errorf("ManagerOf(old) = %q, want %q unmoved", m, mgHandlerManagerA)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerManagerB); m != "" {
		t.Errorf("ManagerOf(new) = %q, want no pair", m)
	}
}

func TestRekeyManaged_OtherDevicesMirrorDoesNotBlock(t *testing.T) {
	t.Parallel()
	h, mups := mirrorHandler(t, activity.New())
	addMirror(t, mups, mgHandlerChildB)

	if code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB); code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, body)
	}
}

func TestRekeyManaged_UnpairedFromAnswers404BeforeRecordConflict(t *testing.T) {
	t.Parallel()
	h, devs := healthHandler(t, activity.New())
	seedRecord(t, devs, "9", mgHandlerManagerB)

	code, body := rekeyManaged(t, h, mgHandlerManagerB, mgHandlerChildB)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unpaired from with a record; body = %s", code, body)
	}
}

func TestRekeyManaged_SameLFDIAnswers400(t *testing.T) {
	t.Parallel()
	h, devs := healthHandler(t, activity.New())
	seedRecord(t, devs, "2", mgHandlerChildA)

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerChildA)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", code, body)
	}
}

func TestRekeyManaged_NoEndDeviceReaderFailsClosed(t *testing.T) {
	t.Parallel()
	h := &handler.AdminManagementHandler{Managers: memory.NewEndDeviceManagementStore()}
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", code, body)
	}
	if m, _ := h.Managers.ManagerOf(t.Context(), mgHandlerChildA); m != mgHandlerManagerA {
		t.Errorf("ManagerOf(old) = %q, want %q unmoved", m, mgHandlerManagerA)
	}
}

func TestRekeyManaged_WarnsWhenTargetConnectedDirectly(t *testing.T) {
	t.Parallel()
	seen := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return seen })
	h, _ := healthHandler(t, rec)
	rec.Record(mgHandlerManagerB)

	code, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", code, body)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(got["warning"], "2026-10-26T08:00:00Z") {
		t.Errorf("warning = %q, want it to carry the lastSeen", got["warning"])
	}
}

func TestRekeyManaged_NoWarningForQuietTarget(t *testing.T) {
	t.Parallel()
	h, _ := healthHandler(t, activity.New())

	_, body := rekeyManaged(t, h, mgHandlerChildA, mgHandlerManagerB)
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["warning"]; ok {
		t.Errorf("warning present for a never-seen target: %q", got["warning"])
	}
}
