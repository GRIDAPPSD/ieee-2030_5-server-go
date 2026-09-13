package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// GET /edev filters to the caller's own EndDevice. These tests reuse the
// request-scoped identity harness from ownership_gate_test.go.

func decodeEndDeviceList(t *testing.T, raw []byte) sep2.EndDeviceList {
	t.Helper()
	var list sep2.EndDeviceList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("GET /edev body is not an EndDeviceList: %v; body=%s", err, raw)
	}
	return list
}

func TestEndDeviceList_ServesOnlyTheCallersDevice(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", callerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev: status %d, want 200; body=%s", status, raw)
	}
	list := decodeEndDeviceList(t, raw)
	if len(list.EndDevice) != 1 {
		t.Fatalf("EndDeviceList carries %d devices, want exactly 1; body=%s", len(list.EndDevice), raw)
	}
	if got := list.EndDevice[0].LFDI; got != callerLFDI {
		t.Errorf("listed device LFDI = %q, want the caller's %q", got, callerLFDI)
	}
	if list.All != 1 || list.Results != 1 {
		t.Errorf("EndDeviceList all=%d results=%d, want 1 and 1 (the filtered count, not the store's 2)", list.All, list.Results)
	}
	if !strings.Contains(string(raw), `all="1"`) || !strings.Contains(string(raw), `results="1"`) {
		t.Errorf("served bytes do not carry all=\"1\" results=\"1\"; body=%s", raw)
	}
	for _, other := range []string{victimLFDI, victimSFDI} {
		if strings.Contains(string(raw), other) {
			t.Errorf("another device's identity %q appears in the raw response; body=%s", other, raw)
		}
	}
}

func TestEndDeviceList_CallerWithNoRecordGetsAWellFormedEmptyList(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	const strangerLFDI = "00112233445566778899AABBCCDDEEFF00112233"
	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", strangerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev as an unregistered device: status %d, want 200; body=%s", status, raw)
	}
	list := decodeEndDeviceList(t, raw)
	if list.All != 0 || list.Results != 0 || len(list.EndDevice) != 0 {
		t.Errorf("empty list all=%d results=%d items=%d, want 0, 0, 0; body=%s", list.All, list.Results, len(list.EndDevice), raw)
	}
	if !strings.Contains(string(raw), `all="0"`) || !strings.Contains(string(raw), `results="0"`) {
		t.Errorf("served bytes do not carry all=\"0\" results=\"0\"; body=%s", raw)
	}
	for _, leaked := range []string{victimLFDI, callerLFDI} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("empty list leaks %q; body=%s", leaked, raw)
		}
	}
}

func TestEndDeviceList_NoUsableIdentityIsRefused(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	cases := map[string]func(r *http.Request){
		"no identity":    func(*http.Request) {},
		"empty LFDI":     func(r *http.Request) { r.Header.Set(gateEmptyIdentityHeader, "1") },
		"lowercase LFDI": func(r *http.Request) { r.Header.Set(gateIdentityHeader, strings.ToLower(callerLFDI)) },
	}
	for name, set := range cases {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/edev", nil)
		if err != nil {
			t.Fatal(err)
		}
		set(req)
		status, raw := sendGateRequest(t, req)
		for _, leaked := range []string{victimLFDI, callerLFDI, strings.ToLower(callerLFDI)} {
			if strings.Contains(string(raw), leaked) {
				t.Errorf("%s: response leaks %q; body=%s", name, leaked, raw)
			}
		}
		if name == "lowercase LFDI" {
			// A present identity that matches nothing is a stranger, not a refusal.
			if status != http.StatusOK {
				t.Errorf("%s: status %d, want 200 with an empty list; body=%s", name, status, raw)
			} else if l := decodeEndDeviceList(t, raw); l.All != 0 || len(l.EndDevice) != 0 {
				t.Errorf("%s: all=%d items=%d, want an empty list", name, l.All, len(l.EndDevice))
			}
			continue
		}
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403; body=%s", name, status, raw)
		}
	}
}

// listRefusingEndDevices fails List, so a passing GET /edev proves the handler
// used the indexed lookup rather than scanning and filtering the collection.
type listRefusingEndDevices struct {
	store.EndDeviceStore
}

var errListNotAllowed = errors.New("test: List must not be called to serve GET /edev")

func (listRefusingEndDevices) List(context.Context, store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return store.ListResult[sep2.EndDevice]{}, errListNotAllowed
}

func TestEndDeviceList_UsesTheIndexedLookup(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	stores.EndDevices = listRefusingEndDevices{stores.EndDevices}
	srv := gateServer(t, stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", callerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev over a store whose List fails: status %d, want 200; body=%s", status, raw)
	}
	list := decodeEndDeviceList(t, raw)
	if len(list.EndDevice) != 1 || list.EndDevice[0].LFDI != callerLFDI {
		t.Errorf("list = %+v, want the caller's device only", list.EndDevice)
	}
}

func TestEndDeviceList_PagingKeepsTheFilteredAll(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	for _, q := range []string{"?s=1", "?l=0"} {
		status, raw := gateRequest(t, srv, http.MethodGet, "/edev"+q, callerLFDI, "")
		if status != http.StatusOK {
			t.Fatalf("GET /edev%s: status %d, want 200; body=%s", q, status, raw)
		}
		list := decodeEndDeviceList(t, raw)
		if list.All != 1 || list.Results != 0 || len(list.EndDevice) != 0 {
			t.Errorf("GET /edev%s: all=%d results=%d items=%d, want 1, 0, 0; body=%s", q, list.All, list.Results, len(list.EndDevice), raw)
		}
	}
}

func TestEndDeviceList_OwnerListsItsSelfRegisteredDevice(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, testStores(), gateTestPolicy())

	if status, raw := gateRequest(t, srv, http.MethodPost, "/edev", callerLFDI, `<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`); status != http.StatusCreated {
		t.Fatalf("POST /edev as caller: status %d; body=%s", status, raw)
	}
	if status, raw := gateRequest(t, srv, http.MethodPost, "/edev", victimLFDI, `<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`); status != http.StatusCreated {
		t.Fatalf("POST /edev as victim: status %d; body=%s", status, raw)
	}

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", callerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev: status %d; body=%s", status, raw)
	}
	list := decodeEndDeviceList(t, raw)
	if list.All != 1 || len(list.EndDevice) != 1 || list.EndDevice[0].LFDI != callerLFDI {
		t.Fatalf("all=%d items=%+v, want only the caller's device", list.All, list.EndDevice)
	}
	dev := list.EndDevice[0]
	if dev.Href == "" || dev.RegistrationLink == nil || dev.RegistrationLink.Href != dev.Href+"/rg" {
		t.Errorf("listed device lost its derived links: href=%q registrationLink=%+v", dev.Href, dev.RegistrationLink)
	}
	if dev.LogEventListLink == nil || dev.LogEventListLink.Href != dev.Href+"/lel" {
		t.Errorf("listed device lost its LogEventListLink: %+v", dev.LogEventListLink)
	}
}
