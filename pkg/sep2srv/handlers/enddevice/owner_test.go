package enddevice_test

import (
	"context"
	"encoding/xml"
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
	return sep2.EndDevice{LFDI: "SOMEONE-ELSE", SFDI: "123"}, nil
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
