package metering_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const unregisteredReason = "managed-device-unregistered"

// recordFixture is a manager paired to mgmtDeviceLFDI, with an EndDevice
// store the test fills or leaves empty.
func recordFixture(t *testing.T) (store.EndDeviceManagementReader, *memory.EndDeviceStore) {
	t.Helper()
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(context.Background(), mgmtManagerLFDI, mgmtDeviceLFDI); err != nil {
		t.Fatalf("assign pair: %v", err)
	}
	return managers, memory.NewEndDeviceStore()
}

func seedEndDevice(t *testing.T, devices *memory.EndDeviceStore, lfdi string) {
	t.Helper()
	dev := sep2.EndDevice{LFDI: lfdi, SFDI: "123456789012"}
	dev.Href = "/edev/1"
	if err := devices.Create(context.Background(), "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
}

func postMirrorClaim(t *testing.T, managers store.EndDeviceManagementReader, mupStore *memory.Store[sep2.MirrorUsagePoint], caller, claimed string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, managers, identityProvider(caller), nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(mupWireBody("REC_MRID", "x", claimed))))
	return w
}

// TestRequireDeviceRecords_CreateNeedsTheManagedRecord pins the three create
// outcomes: the manager is refused with the unregistered reason and nothing
// is stored when the record is missing, admitted and stored under the managed
// device when it exists, and a device mirroring for itself needs no record.
func TestRequireDeviceRecords_CreateNeedsTheManagedRecord(t *testing.T) {
	t.Run("manager without a record is refused", func(t *testing.T) {
		buf := captureMirrorLog(t)
		pairs, devices := recordFixture(t)
		mupStore := memory.NewStore[sep2.MirrorUsagePoint]()

		w := postMirrorClaim(t, metering.RequireDeviceRecords(pairs, devices), mupStore, mgmtManagerLFDI, mgmtDeviceLFDI)

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
		}
		if got := strings.TrimSpace(w.Body.String()); got != "forbidden" {
			t.Errorf("body = %q, want the fixed %q", got, "forbidden")
		}
		if n, _ := mupStore.Count(context.Background()); n != 0 {
			t.Errorf("stored count = %d, want 0", n)
		}
		if !strings.Contains(buf.String(), "may not act for device="+`"`+mgmtDeviceLFDI+`"`) {
			t.Errorf("refusal log line missing, log = %q", buf.String())
		}
	})

	t.Run("manager with a record is admitted", func(t *testing.T) {
		pairs, devices := recordFixture(t)
		seedEndDevice(t, devices, mgmtDeviceLFDI)
		mupStore := memory.NewStore[sep2.MirrorUsagePoint]()

		w := postMirrorClaim(t, metering.RequireDeviceRecords(pairs, devices), mupStore, mgmtManagerLFDI, mgmtDeviceLFDI)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
		}
		stored, err := mupStore.Get(context.Background(), metering.MirrorStoreID(mgmtDeviceLFDI, "REC_MRID"))
		if err != nil {
			t.Fatalf("stored mirror: %v", err)
		}
		if stored.DeviceLFDI != mgmtDeviceLFDI {
			t.Errorf("stored DeviceLFDI = %q, want the managed device %q", stored.DeviceLFDI, mgmtDeviceLFDI)
		}
	})

	t.Run("device mirroring for itself needs no record", func(t *testing.T) {
		pairs, devices := recordFixture(t)
		mupStore := memory.NewStore[sep2.MirrorUsagePoint]()

		w := postMirrorClaim(t, metering.RequireDeviceRecords(pairs, devices), mupStore, mgmtDeviceLFDI, "")

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
		}
		if _, err := mupStore.Get(context.Background(), metering.MirrorStoreID(mgmtDeviceLFDI, "REC_MRID")); err != nil {
			t.Errorf("self mirror not stored: %v", err)
		}
	})
}

// TestRequireDeviceRecords_InstanceRoutesNeedTheManagedRecord covers the
// four /mup/{id} routes: each refuses the manager with 403 while the record
// is missing and admits it once the record exists.
func TestRequireDeviceRecords_InstanceRoutesNeedTheManagedRecord(t *testing.T) {
	routes := []struct {
		name   string
		method string
		path   string
		body   []byte
		want   int
	}{
		{"GET", http.MethodGet, "/mup/%s", nil, http.StatusOK},
		{"PUT", http.MethodPut, "/mup/%s", mupWireBody("REC_ROUTE", "y", mgmtDeviceLFDI), http.StatusNoContent},
		{"DELETE", http.MethodDelete, "/mup/%s", nil, http.StatusOK},
		{"POST reading", http.MethodPost, "/mup/%s", []byte(`<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>AB</mRID></MirrorMeterReading>`), http.StatusCreated},
	}
	for _, rt := range routes {
		for _, withRecord := range []bool{false, true} {
			name := rt.name + " record=" + map[bool]string{false: "absent", true: "present"}[withRecord]
			t.Run(name, func(t *testing.T) {
				pairs, devices := recordFixture(t)
				if withRecord {
					seedEndDevice(t, devices, mgmtDeviceLFDI)
				}
				mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
				mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
				id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "REC_ROUTE", "before")
				mux := mirrorInstanceMuxWithManagers(mupStore, mmrStore, metering.RequireDeviceRecords(pairs, devices), mgmtManagerLFDI)

				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(rt.method, strings.Replace(rt.path, "%s", id, 1), bytes.NewReader(rt.body)))

				want := rt.want
				if !withRecord {
					want = http.StatusForbidden
				}
				if w.Code != want {
					t.Fatalf("status = %d, want %d; body = %s", w.Code, want, w.Body.String())
				}
				_, err := mupStore.Get(context.Background(), id)
				if !withRecord && err != nil {
					t.Errorf("a refused %s changed the stored mirror: %v", rt.name, err)
				}
			})
		}
	}
}

// failingDevices answers every GetByLFDI with err, or with a record carrying
// stored when err is nil.
type failingDevices struct {
	store.EndDeviceReader
	err    error
	stored string
}

func (f failingDevices) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{LFDI: f.stored}, f.err
}

// TestRequireDeviceRecords_IndeterminateRecordLookup pins the fail-closed
// cases: a device-store outage is a 500 (never a refusal read as success),
// a record whose stored LFDI differs from the one asked for grants nothing,
// and an absent device reader refuses every delegation.
func TestRequireDeviceRecords_IndeterminateRecordLookup(t *testing.T) {
	pairs, _ := recordFixture(t)
	cases := []struct {
		name    string
		devices store.EndDeviceReader
		want    int
	}{
		{"device store outage", failingDevices{err: errors.New("device store unavailable")}, http.StatusInternalServerError},
		{"index returned another device", failingDevices{stored: mgmtOutsiderLFDI}, http.StatusForbidden},
		{"no device reader wired", nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			w := postMirrorClaim(t, metering.RequireDeviceRecords(pairs, tc.devices), mupStore, mgmtManagerLFDI, mgmtDeviceLFDI)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}
			if n, _ := mupStore.Count(context.Background()); n != 0 {
				t.Errorf("stored count = %d, want 0", n)
			}
		})
	}
}

// TestRequireDeviceRecords_AbsentManagersIsReturnedUnchanged keeps the
// no-delegation mode: wrapping an absent reader must not make it present.
func TestRequireDeviceRecords_AbsentManagersIsReturnedUnchanged(t *testing.T) {
	if got := metering.RequireDeviceRecords(nil, memory.NewEndDeviceStore()); !store.IsAbsent(got) {
		t.Errorf("RequireDeviceRecords(nil, ...) = %T, want an absent reader", got)
	}
}
