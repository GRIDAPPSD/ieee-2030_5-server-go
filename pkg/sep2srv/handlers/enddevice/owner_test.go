package enddevice_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestOwnedBy(t *testing.T) {
	t.Parallel()
	const lfdi = "0BA1C3D4E5F60718293A4B5C6D7E8F9012345678"
	cases := []struct {
		name           string
		stored, caller string
		want           bool
	}{
		{"exact match", lfdi, lfdi, true},
		{"both empty", "", "", false},
		{"stored empty", "", lfdi, false},
		{"caller empty", lfdi, "", false},
		{"case differs", strings.ToLower(lfdi), lfdi, false},
		{"trailing space", lfdi + " ", lfdi, false},
		{"different device", "F0E1D2C3B4A5968778695A4B3C2D1E0F87654321", lfdi, false},
	}
	for _, tc := range cases {
		if got := coreedev.OwnedBy(tc.stored, tc.caller); got != tc.want {
			t.Errorf("%s: OwnedBy(%q, %q) = %v, want %v", tc.name, tc.stored, tc.caller, got, tc.want)
		}
	}
}

// staleIndexStore answers GetByLFDI with a record whose stored LFDI is not the
// one asked for, the shape of an index that drifted from its records.
type staleIndexStore struct {
	store.EndDeviceStore
}

func (staleIndexStore) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	dev := sep2.EndDevice{LFDI: "SOMEONE-ELSE", SFDI: "123"}
	dev.Href = "/edev/77"
	return dev, nil
}

func serveList(t *testing.T, h http.HandlerFunc) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/edev", nil))
	return rec.Code, rec.Body.String()
}

func TestHandleEndDeviceListForCaller_Edges(t *testing.T) {
	t.Parallel()
	const caller = "CALLER"
	identity := func(context.Context) (string, string, bool) { return caller, "", true }

	t.Run("nil identity func is refused", func(t *testing.T) {
		t.Parallel()
		status, _ := serveList(t, coreedev.HandleEndDeviceListForCaller(memory.NewEndDeviceStore(), nil, nil, 900))
		if status != http.StatusForbidden {
			t.Errorf("status %d, want 403", status)
		}
	})

	t.Run("absent store answers 500 without panicking", func(t *testing.T) {
		t.Parallel()
		var typedNil *memory.EndDeviceStore
		for name, s := range map[string]store.EndDeviceStore{"nil": nil, "typed nil": typedNil} {
			status, body := serveList(t, coreedev.HandleEndDeviceListForCaller(s, nil, identity, 900))
			if status != http.StatusInternalServerError || strings.Contains(body, "EndDeviceList") {
				t.Errorf("%s store: status %d body %q, want a 500 and no list", name, status, body)
			}
		}
	})

	t.Run("index returning another device lists nothing", func(t *testing.T) {
		t.Parallel()
		status, body := serveList(t, coreedev.HandleEndDeviceListForCaller(staleIndexStore{memory.NewEndDeviceStore()}, nil, identity, 900))
		if status != http.StatusOK {
			t.Fatalf("status %d, want 200; body=%s", status, body)
		}
		var list sep2.EndDeviceList
		if err := xml.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if list.All != 0 || list.Results != 0 || len(list.EndDevice) != 0 || strings.Contains(body, "SOMEONE-ELSE") {
			t.Errorf("all=%d results=%d items=%d body=%s, want an empty list with no foreign LFDI", list.All, list.Results, len(list.EndDevice), body)
		}
	})

	t.Run("non-GET is 405", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		coreedev.HandleEndDeviceListForCaller(memory.NewEndDeviceStore(), nil, identity, 900)(rec, httptest.NewRequest(http.MethodPut, "/edev", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status %d, want 405", rec.Code)
		}
	})
}

// managedByStub reports fixed managed LFDIs, or err.
type managedByStub struct {
	store.EndDeviceManagementStore
	managed []string
	err     error
}

func (m managedByStub) ManagedBy(context.Context, string) ([]string, error) {
	return m.managed, m.err
}

func TestHandleEndDeviceListForCaller_ManagedEdges(t *testing.T) {
	t.Parallel()
	identity := func(context.Context) (string, string, bool) { return "CALLER", "", true }

	t.Run("management lookup failure is 500", func(t *testing.T) {
		t.Parallel()
		h := coreedev.HandleEndDeviceListForCaller(memory.NewEndDeviceStore(), managedByStub{err: errors.New("management backend down")}, identity, 900)
		status, body := serveList(t, h)
		if status != http.StatusInternalServerError || strings.Contains(body, "EndDeviceList") || strings.Contains(body, "backend down") {
			t.Errorf("status %d body %q, want a 500 carrying neither a list nor the cause", status, body)
		}
	})

	t.Run("managed LFDI resolving to another record is not listed", func(t *testing.T) {
		t.Parallel()
		h := coreedev.HandleEndDeviceListForCaller(staleIndexStore{memory.NewEndDeviceStore()}, managedByStub{managed: []string{"MANAGED"}}, identity, 900)
		status, body := serveList(t, h)
		if status != http.StatusOK {
			t.Fatalf("status %d, want 200; body=%s", status, body)
		}
		var list sep2.EndDeviceList
		if err := xml.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if list.All != 0 || len(list.EndDevice) != 0 || strings.Contains(body, "SOMEONE-ELSE") {
			t.Errorf("all=%d items=%d body=%s, want an empty list with no foreign LFDI", list.All, len(list.EndDevice), body)
		}
	})
}

// TestHandleEndDeviceListForCaller_IndexDriftLogNamesTheStoreKey cannot run in
// parallel: it swaps the process-wide log output.
func TestHandleEndDeviceListForCaller_IndexDriftLogNamesTheStoreKey(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	identity := func(context.Context) (string, string, bool) { return "CALLER", "", true }
	status, body := serveList(t, coreedev.HandleEndDeviceListForCaller(staleIndexStore{memory.NewEndDeviceStore()}, nil, identity, 900))
	if status != http.StatusOK || strings.Contains(body, "SOMEONE-ELSE") {
		t.Fatalf("status %d body %q, want 200 with the drifted record not listed", status, body)
	}
	if !strings.Contains(buf.String(), `"77"`) {
		t.Errorf("index drift was logged without the drifted record's store key; log=%q", buf.String())
	}
}

// TestHandleEndDeviceListForCaller_BackfillsFlowReservationLinks covers the
// same pre-existing-device gap as the POST /edev backfill test, through the
// list a client actually discovers its devices with: a record seeded without
// either flow reservation link must still carry both when it is listed.
func TestHandleEndDeviceListForCaller_BackfillsFlowReservationLinks(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	pre := sep2.EndDevice{LFDI: "CALLER"}
	pre.Href = "/edev/1"
	if err := s.Create(context.Background(), "1", pre); err != nil {
		t.Fatalf("seed pre-existing EndDevice: %v", err)
	}

	identity := func(context.Context) (string, string, bool) { return "CALLER", "", true }
	status, body := serveList(t, coreedev.HandleEndDeviceListForCaller(s, nil, identity, 900))
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", status, body)
	}
	var list sep2.EndDeviceList
	if err := xml.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if len(list.EndDevice) != 1 {
		t.Fatalf("listed %d devices, want 1; body=%s", len(list.EndDevice), body)
	}
	got := list.EndDevice[0]
	if got.FlowReservationRequestListLink == nil || got.FlowReservationRequestListLink.Href != "/edev/1/frq" {
		t.Errorf("listed FlowReservationRequestListLink = %+v, want href /edev/1/frq", got.FlowReservationRequestListLink)
	}
	if got.FlowReservationResponseListLink == nil || got.FlowReservationResponseListLink.Href != "/edev/1/frp" {
		t.Errorf("listed FlowReservationResponseListLink = %+v, want href /edev/1/frp", got.FlowReservationResponseListLink)
	}
}
