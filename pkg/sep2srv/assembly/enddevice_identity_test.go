package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// PUT /edev/{id} applies its body to the record but never the record's
// identity: the stored LFDI and SFDI are what the gate, the list and the LFDI
// index resolve by. These tests read the store and drive the other devices'
// requests through the assembled router.

func putBody(identity string) string {
	return `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><changedTime>1</changedTime><enabled>false</enabled>` + identity + `</EndDevice>`
}

func TestEndDeviceUpdate_BodyIdentityCannotRedirectAnotherDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())

	if l := listAs(t, srv, "", victimLFDI); len(l.EndDevice) != 1 || l.EndDevice[0].Href != "/edev/"+victimID {
		t.Fatalf("control: victim list %+v, want only /edev/%s", l.EndDevice, victimID)
	}
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+callerID+"/der", managerLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("control: manager GET /edev/%s/der: status %d, want 403; body=%q", callerID, status, raw)
	}

	forged := putBody(`<lFDI>` + victimLFDI + `</lFDI><sFDI>` + victimSFDI + `</sFDI>`)
	if status, raw := gateRequest(t, srv, http.MethodPut, "/edev/"+callerID, callerLFDI, forged); status != http.StatusNoContent {
		t.Fatalf("owner PUT carrying another device's identity: status %d, want 204; body=%q", status, raw)
	}

	stored, err := fleet.stores.EndDevices.Get(ctx, callerID)
	if err != nil {
		t.Fatalf("read the PUT record: %v", err)
	}
	if stored.LFDI != callerLFDI || stored.SFDI != callerSFDI {
		t.Errorf("stored identity after the PUT: LFDI=%q SFDI=%q, want the pre-PUT %q and %q", stored.LFDI, stored.SFDI, callerLFDI, callerSFDI)
	}
	if stored.Enabled == nil || *stored.Enabled {
		t.Errorf("the PUT's non-identity field was not applied: enabled=%v, want false", stored.Enabled)
	}
	for name, lookup := range map[string]func() (sep2.EndDevice, error){
		"GetByLFDI(victim)": func() (sep2.EndDevice, error) { return fleet.stores.EndDevices.GetByLFDI(ctx, victimLFDI) },
		"GetBySFDI(victim)": func() (sep2.EndDevice, error) { return fleet.stores.EndDevices.GetBySFDI(ctx, victimSFDI) },
		"GetByLFDI(owner)":  func() (sep2.EndDevice, error) { return fleet.stores.EndDevices.GetByLFDI(ctx, callerLFDI) },
	} {
		want := "/edev/" + victimID
		if name == "GetByLFDI(owner)" {
			want = "/edev/" + callerID
		}
		if dev, err := lookup(); err != nil || dev.Href != want {
			t.Errorf("%s = href %q, err %v; want %s", name, dev.Href, err, want)
		}
	}

	if l := listAs(t, srv, "", victimLFDI); len(l.EndDevice) != 1 || l.EndDevice[0].Href != "/edev/"+victimID || l.EndDevice[0].LFDI != victimLFDI {
		t.Errorf("victim list after the PUT: %+v, want only its own /edev/%s", l.EndDevice, victimID)
	}
	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", victimLFDI, `<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`)
	var registered sep2.EndDevice
	if status != http.StatusOK || xml.Unmarshal(raw, &registered) != nil || registered.Href != "/edev/"+victimID {
		t.Errorf("victim POST /edev after the PUT: status %d href %q, want 200 and its own /edev/%s; body=%s", status, registered.Href, victimID, raw)
	}

	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+callerID+"/der", managerLFDI, ""); status != http.StatusForbidden {
		t.Errorf("manager GET /edev/%s/der after the PUT: status %d, want 403; body=%q", callerID, status, raw)
	}
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID+"/der", managerLFDI, ""); status != http.StatusOK {
		t.Errorf("manager GET /edev/%s/der after the PUT: status %d, want 200; body=%q", victimID, status, raw)
	}

	status, raw = gateRequest(t, srv, http.MethodGet, "/edev/"+callerID, callerLFDI, "")
	var own sep2.EndDevice
	if status != http.StatusOK || xml.Unmarshal(raw, &own) != nil || own.LFDI != callerLFDI {
		t.Errorf("owner GET /edev/%s after its PUT: status %d LFDI %q, want 200 and its own LFDI; body=%s", callerID, status, own.LFDI, raw)
	}
	if status, raw := gateRequest(t, srv, http.MethodPut, "/edev/"+callerID, callerLFDI, putBody("")); status != http.StatusNoContent {
		t.Errorf("owner PUT /edev/%s after its PUT: status %d, want 204; body=%q", callerID, status, raw)
	}
}

func TestEndDeviceUpdate_OwnerPUTWithoutIdentityKeepsIdentityAndAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, body := range map[string]string{
		"identity absent": putBody(""),
		"identity empty":  putBody(`<lFDI></lFDI><sFDI></sFDI>`),
	} {
		fleet := newManagementFleet(t)
		srv := gateServer(t, fleet.stores, gateTestPolicy())

		if status, raw := gateRequest(t, srv, http.MethodPut, "/edev/"+victimID, victimLFDI, body); status != http.StatusNoContent {
			t.Errorf("%s: owner PUT: status %d, want 204; body=%q", name, status, raw)
			continue
		}
		stored, err := fleet.stores.EndDevices.Get(ctx, victimID)
		if err != nil || stored.LFDI != victimLFDI || stored.SFDI != victimSFDI {
			t.Errorf("%s: stored identity LFDI=%q SFDI=%q err=%v, want the pre-PUT %q and %q", name, stored.LFDI, stored.SFDI, err, victimLFDI, victimSFDI)
		}
		for _, tc := range []struct {
			method, asLFDI, body string
			want                 int
		}{
			{http.MethodGet, victimLFDI, "", http.StatusOK},
			{http.MethodPut, victimLFDI, putBody(""), http.StatusNoContent},
			{http.MethodGet, managerLFDI, "", http.StatusOK},
		} {
			if status, raw := gateRequest(t, srv, tc.method, "/edev/"+victimID, tc.asLFDI, tc.body); status != tc.want {
				t.Errorf("%s: %s /edev/%s as %s: status %d, want %d; body=%q", name, tc.method, victimID, tc.asLFDI, status, tc.want, raw)
			}
		}
		if l := listAs(t, srv, "", victimLFDI); len(l.EndDevice) != 1 || l.EndDevice[0].Href != "/edev/"+victimID {
			t.Errorf("%s: owner list %+v, want only /edev/%s", name, l.EndDevice, victimID)
		}
	}
}
