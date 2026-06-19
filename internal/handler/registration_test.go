package handler_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// IEEE-101: Registration GET handler at /edev/{id}/rg.
//
// Spec reference: IEEE 2030.5 §10.6.4 (Registration resource), §6.3.4 (LFDI),
// CSIP V1.2 BASIC-004 (device reads PIN via /edev/{id}/rg to validate against
// its configured PIN).

const (
	// 40-char hex LFDI fixtures (per §6.3.4: 20 leading bytes of SHA-256
	// fingerprint). Match the shape used in admin_register_test.go.
	deviceLFDI = "AABBCCDD00112233445566778899AABBCCDDEEFF"
	otherLFDI  = "11223344556677889900AABBCCDDEEFF00112233"
	deviceSFDI = "123456789012"
	deviceID   = "12345678" // SFDI[:8]
)

// seedRegistrationFixture creates an EndDevice + Registration matching the
// shape produced by IEEE-095's admin POST /api/devices flow.
func seedRegistrationFixture(t *testing.T, edevs *memory.EndDeviceStore, regs *memory.RegistrationStore) {
	t.Helper()
	ctx := context.Background()
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + deviceID},
		},
		SFDI: deviceSFDI,
		LFDI: deviceLFDI,
	}
	if err := edevs.Create(ctx, deviceID, dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	reg := sep2.Registration{
		Resource:           sep2.Resource{Href: "/edev/" + deviceID + "/rg"},
		DateTimeRegistered: 1700000000,
		PIN:                123456,
	}
	if err := regs.Create(ctx, deviceID, reg); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}
}

func TestHandleGetRegistration_OwnDeviceReturnsXML(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "sep") {
		t.Errorf("Content-Type = %q, want sep+xml flavor", ct)
	}

	var got sep2.Registration
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.PIN != 123456 {
		t.Errorf("PIN = %d, want 123456", got.PIN)
	}
	if got.DateTimeRegistered != 1700000000 {
		t.Errorf("DateTimeRegistered = %d, want 1700000000", got.DateTimeRegistered)
	}
	if got.Href != "/edev/"+deviceID+"/rg" {
		t.Errorf("Href = %q, want %q", got.Href, "/edev/"+deviceID+"/rg")
	}
}

func TestHandleGetRegistration_LFDIMismatchReturns403(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, "999999999999", otherLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestHandleGetRegistration_NoIdentityReturns403(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	// No addIdentity call — context lacks DeviceIdentity.
	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestHandleGetRegistration_UnknownEndDeviceReturns404(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	// Don't seed.

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodGet, "/edev/missing/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetRegistration_NoRegistrationForDeviceReturns404(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()

	// Seed EndDevice WITHOUT a matching Registration. Models a device that
	// was auto-registered via mTLS (auth.AutoRegistrationMiddleware) but
	// for which the admin never wrote a Registration via POST /api/devices.
	ctx := context.Background()
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + deviceID},
		},
		SFDI: deviceSFDI,
		LFDI: deviceLFDI,
	}
	if err := edevs.Create(ctx, deviceID, dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetRegistration_PostReturns405(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	// Route handler directly (no method-specific mux) so the handler's own
	// method gate is exercised. Mirrors the pattern in edev_test.go.
	h := handler.HandleGetRegistration(edevs, regs)

	req := httptest.NewRequest(http.MethodPost, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow header = %q, want to contain GET", allow)
	}
}

func TestHandleGetRegistration_EmptyIDReturns400(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()

	// Invoke the handler directly without going through a mux pattern —
	// PathValue("id") is empty. Defensive guard mirrors HandleEndDevice.
	h := handler.HandleGetRegistration(edevs, regs)

	req := httptest.NewRequest(http.MethodGet, "/edev//rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleGetRegistration_RegistrationMissingHrefFilledFromPath(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()

	ctx := context.Background()
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + deviceID},
		},
		SFDI: deviceSFDI,
		LFDI: deviceLFDI,
	}
	if err := edevs.Create(ctx, deviceID, dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	// Registration with an empty Href — handler should backfill it from
	// the path so the wire response is well-formed.
	reg := sep2.Registration{
		DateTimeRegistered: 1700000000,
		PIN:                42,
	}
	if err := regs.Create(ctx, deviceID, reg); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got sep2.Registration
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Href != "/edev/"+deviceID+"/rg" {
		t.Errorf("Href = %q, want backfilled path", got.Href)
	}
}

func TestHandleGetRegistration_HeadAllowed(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	// HEAD is the standard companion to GET on read-only resources.
	// Even when registered via "GET /edev/{id}/rg" the handler must let
	// the caller method-gate match HEAD too — matches HandleEndDevice.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))
	mux.HandleFunc("HEAD /edev/{id}/rg", handler.HandleGetRegistration(edevs, regs))

	req := httptest.NewRequest(http.MethodHead, "/edev/"+deviceID+"/rg", nil)
	req = addIdentity(req, deviceSFDI, deviceLFDI)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", w.Code)
	}
}
