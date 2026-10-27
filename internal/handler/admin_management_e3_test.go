package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
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
