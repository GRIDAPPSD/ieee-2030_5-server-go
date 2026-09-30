package assembly_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// A posted Response is stored only when its endDeviceLFDI is the sender's
// own device or one the sender manages (IEEE 2030.5-2018 6.11.2). The
// sender here is testLFDI (testAuthPolicy).
func TestPostResponseChecksEndDeviceLFDIAgainstSender(t *testing.T) {
	const (
		managedLFDI = "1111111111111111111111111111111111111111"
		foreignLFDI = "2222222222222222222222222222222222222222"
		subject     = "0123456789ABCDEF0123456789ABCDEF"
	)
	status := sep2.ResponseStatusEventReceived
	derControlResponse := func(lfdi string) []byte {
		t.Helper()
		b, err := xml.Marshal(&sep2.DERControlResponse{Response: sep2.Response{EndDeviceLFDI: lfdi, Status: &status, Subject: subject}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	baseResponse := func(lfdi string) []byte {
		t.Helper()
		b, err := xml.Marshal(&sep2.Response{EndDeviceLFDI: lfdi, Status: &status, Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cases := []struct {
		name       string
		body       []byte
		wantStatus int
		wantStored string // stored endDeviceLFDI when 201
	}{
		{"owner", derControlResponse(testLFDI), http.StatusCreated, testLFDI},
		{"owner in lower case", derControlResponse(strings.ToLower(testLFDI)), http.StatusCreated, testLFDI},
		{"managed device", derControlResponse(managedLFDI), http.StatusCreated, managedLFDI},
		{"foreign device", derControlResponse(foreignLFDI), http.StatusForbidden, ""},
		{"foreign device in lower case", derControlResponse(strings.ToLower(foreignLFDI)), http.StatusForbidden, ""},
		{"foreign device on a base Response", baseResponse(foreignLFDI), http.StatusForbidden, ""},
		{"DERControlResponse naming no device", derControlResponse(""), http.StatusBadRequest, ""},
		{"LFDI that is not 40 hex digits", derControlResponse("NOT-AN-LFDI"), http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stores := testStores()
			if err := stores.EndDeviceManagers.Assign(context.Background(), testLFDI, managedLFDI); err != nil {
				t.Fatal(err)
			}
			srv := derControlRouter(t, stores)
			href := coreresponse.ListHref(coreresponse.DefaultSetID)
			resp, err := http.Post(srv.URL+href, "application/sep+xml", bytes.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			body := string(readBody(t, resp))
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, tc.wantStatus, body)
			}
			stored, err := stores.Responses.List(context.Background(), coreresponse.DefaultSetID, store.ListOptions{Unbounded: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus != http.StatusCreated {
				if len(stored.Items) != 0 {
					t.Fatalf("a refused Response was stored: %+v", stored.Items)
				}
				return
			}
			if len(stored.Items) != 1 || stored.Items[0].EndDeviceLFDI != tc.wantStored || stored.Items[0].Subject != subject {
				t.Fatalf("stored %+v, want one Response for %s", stored.Items, tc.wantStored)
			}
		})
	}
}

// A request that carries no certificate identity cannot speak for any
// device: its Response naming one is refused and nothing is stored.
func TestPostResponseWithoutIdentityIsRefused(t *testing.T) {
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, testLFDI)
	policy := testAuthPolicy()
	policy.Identity = func(context.Context) (string, string, bool) { return "", "", false }
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	status := sep2.ResponseStatusEventReceived
	body, err := xml.Marshal(&sep2.DERControlResponse{Response: sep2.Response{EndDeviceLFDI: testLFDI, Status: &status, Subject: "0123456789ABCDEF0123456789ABCDEF"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+coreresponse.ListHref(coreresponse.DefaultSetID), "application/sep+xml", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	stored, err := stores.Responses.List(context.Background(), coreresponse.DefaultSetID, store.ListOptions{Unbounded: true})
	if err != nil || len(stored.Items) != 0 {
		t.Fatalf("stored %+v (%v), want nothing", stored.Items, err)
	}
}
