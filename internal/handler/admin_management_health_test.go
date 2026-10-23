package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1087: a pair is keyed on the LFDI, which changes when a certificate
// rotates. The listing reports, per LFDI, whether an EndDevice record exists
// and when the LFDI last made a request, so a rotation is visible to the
// operator instead of silent.

type healthMember struct {
	LFDI      string  `json:"lfdi"`
	HasRecord *bool   `json:"hasRecord"`
	LastSeen  *string `json:"lastSeen"`
}

type healthByManager struct {
	ManagerLFDI  string         `json:"managerLFDI"`
	ManagedLFDIs []string       `json:"managedLFDIs"`
	Manager      healthMember   `json:"manager"`
	Managed      []healthMember `json:"managed"`
}

type healthByManaged struct {
	ManagerLFDI string       `json:"managerLFDI"`
	ManagedLFDI string       `json:"managedLFDI"`
	Manager     healthMember `json:"manager"`
	Managed     healthMember `json:"managed"`
}

func seedRecord(t *testing.T, devs store.EndDeviceStore, id, lfdi string) {
	t.Helper()
	dev := sep2.EndDevice{LFDI: lfdi}
	dev.Href = "/edev/" + id
	if err := devs.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("seed EndDevice %q: %v", id, err)
	}
}

func healthHandler(t *testing.T, rec *activity.Recorder) (*handler.AdminManagementHandler, *memory.EndDeviceStore) {
	t.Helper()
	devs := memory.NewEndDeviceStore()
	h := &handler.AdminManagementHandler{
		Managers:   memory.NewEndDeviceManagementStore(),
		EndDevices: devs,
		Activity:   rec,
	}
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildB)
	return h, devs
}

func wantBool(t *testing.T, label string, got *bool, want bool) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s hasRecord = %v, want %v", label, got, want)
	}
}

func TestListManagementPairs_ByManagerReportsRecordAndLastSeen(t *testing.T) {
	t.Parallel()
	seen := time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return seen })
	h, devs := healthHandler(t, rec)
	// The manager and the first child have records; the second child's
	// certificate has rotated, so its pair names an LFDI with no record.
	seedRecord(t, devs, "1", mgHandlerManagerA)
	seedRecord(t, devs, "2", mgHandlerChildA)
	rec.Record(mgHandlerManagerA)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?manager="+mgHandlerManagerA, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var got healthByManager
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.ManagedLFDIs) != 2 {
		t.Errorf("managedLFDIs = %v, want the existing 2-entry list kept", got.ManagedLFDIs)
	}
	if got.Manager.LFDI != mgHandlerManagerA {
		t.Errorf("manager lfdi = %q, want %q", got.Manager.LFDI, mgHandlerManagerA)
	}
	wantBool(t, "manager", got.Manager.HasRecord, true)
	if got.Manager.LastSeen == nil || *got.Manager.LastSeen != "2026-10-23T09:30:00Z" {
		t.Errorf("manager lastSeen = %v, want 2026-10-23T09:30:00Z", got.Manager.LastSeen)
	}
	if len(got.Managed) != 2 {
		t.Fatalf("managed = %+v, want 2 entries", got.Managed)
	}
	byLFDI := map[string]healthMember{}
	for _, m := range got.Managed {
		byLFDI[m.LFDI] = m
	}
	wantBool(t, "child A", byLFDI[mgHandlerChildA].HasRecord, true)
	wantBool(t, "child B", byLFDI[mgHandlerChildB].HasRecord, false)
	if byLFDI[mgHandlerChildA].LastSeen != nil || byLFDI[mgHandlerChildB].LastSeen != nil {
		t.Errorf("never-seen children lastSeen = %v, %v, want null for both", byLFDI[mgHandlerChildA].LastSeen, byLFDI[mgHandlerChildB].LastSeen)
	}
}

func TestListManagementPairs_ByManagedReportsRecordAndLastSeen(t *testing.T) {
	t.Parallel()
	seen := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return seen })
	h, devs := healthHandler(t, rec)
	seedRecord(t, devs, "2", mgHandlerChildA)
	rec.Record(mgHandlerChildA)

	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var got healthByManaged
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ManagerLFDI != mgHandlerManagerA || got.ManagedLFDI != mgHandlerChildA {
		t.Errorf("pair = %q -> %q, want %q -> %q", got.ManagerLFDI, got.ManagedLFDI, mgHandlerManagerA, mgHandlerChildA)
	}
	wantBool(t, "manager", got.Manager.HasRecord, false)
	if got.Manager.LastSeen != nil {
		t.Errorf("manager lastSeen = %v, want null", got.Manager.LastSeen)
	}
	wantBool(t, "managed", got.Managed.HasRecord, true)
	if got.Managed.LastSeen == nil || *got.Managed.LastSeen != "2026-10-23T10:00:00Z" {
		t.Errorf("managed lastSeen = %v, want 2026-10-23T10:00:00Z", got.Managed.LastSeen)
	}
}

// TestListManagementPairs_NoEndDeviceReaderOmitsHasRecord pins that an
// unwired reader reads as "unknown", never as "no record".
func TestListManagementPairs_NoEndDeviceReaderOmitsHasRecord(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?managed="+mgHandlerChildA, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "hasRecord") {
		t.Errorf("body = %s, want no hasRecord key without an EndDevice reader", w.Body.String())
	}
}

type failingReader struct{ store.EndDeviceReader }

func (failingReader) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errors.New("backend down")
}

// TestListManagementPairs_LookupFailureIs500 pins that a failing record
// lookup is an error, not a silent "hasRecord: false" that would read as a
// rotation.
func TestListManagementPairs_LookupFailureIs500(t *testing.T) {
	t.Parallel()
	h := newManagementHandler(t)
	h.EndDevices = failingReader{}
	mustAssignH(t, h, mgHandlerManagerA, mgHandlerChildA)
	for _, q := range []string{"managed=" + mgHandlerChildA, "manager=" + mgHandlerManagerA} {
		w := doJSON(t, h.HandleListManagementPairs(), http.MethodGet, "/api/management-pairs?"+q, "")
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500; body = %s", q, w.Code, w.Body.String())
		}
	}
}
