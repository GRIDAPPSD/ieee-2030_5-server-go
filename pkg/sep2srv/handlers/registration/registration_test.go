// Tests for the Registration handler.
// Ported from the reference server's internal/handler/registration_test.go,
// adapted to the injected IdentityFunc seam: instead of auth.GetIdentity the
// tests wire a closure that returns a fixed identity.
package registration_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/registration"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	// 40-char hex LFDI fixtures (per spec section 6.3.4: 20 leading bytes of
	// SHA-256 fingerprint).
	deviceLFDI = "AABBCCDD00112233445566778899AABBCCDDEEFF"
	otherLFDI  = "11223344556677889900AABBCCDDEEFF00112233"
	deviceSFDI = "123456789012"
	deviceID   = "12345678" // SFDI[:8]
)

// identityOK returns an IdentityFunc that always supplies the given LFDI/SFDI.
func identityOK(lfdi, sfdi string) registration.IdentityFunc {
	return func(_ context.Context) (string, string, bool) { return lfdi, sfdi, true }
}

// identityNone returns an IdentityFunc that always returns ok=false (no identity).
func identityNone() registration.IdentityFunc {
	return func(_ context.Context) (string, string, bool) { return "", "", false }
}

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

// TestHandleGetRegistration_OwnDeviceReturnsXML: happy path, field-value
// assertions on PIN, DateTimeRegistered, and Href (data-invariants Rule 1).
func TestHandleGetRegistration_OwnDeviceReturnsXML(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI)))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
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
		t.Errorf("Href = %q, want canonical path", got.Href)
	}
}

// TestHandleGetRegistration_NoIdentityReturns403: CRITICAL security gate.
// When no device identity is present the handler must return 403, never 200.
func TestHandleGetRegistration_NoIdentityReturns403(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityNone()))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("no-identity status = %d, want 403", w.Code)
	}
}

// TestHandleGetRegistration_LFDIMismatchReturns403: CRITICAL security gate.
// A device must not be able to read another device's Registration (PIN).
func TestHandleGetRegistration_LFDIMismatchReturns403(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	mux := http.NewServeMux()
	// Caller's LFDI does not match the stored EndDevice's LFDI.
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityOK(otherLFDI, "999999999999")))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("LFDI-mismatch status = %d, want 403", w.Code)
	}
}

// TestHandleGetRegistration_UnknownEndDeviceReturns404: the EndDevice must
// exist before a Registration can be returned.
func TestHandleGetRegistration_UnknownEndDeviceReturns404(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI)))

	req := httptest.NewRequest(http.MethodGet, "/edev/missing/rg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleGetRegistration_NoRegistrationForDeviceReturns404: a device
// registered via auto-registration may have an EndDevice record but no
// admin-authored Registration yet.
func TestHandleGetRegistration_NoRegistrationForDeviceReturns404(t *testing.T) {
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
	// No Registration seeded.

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI)))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleGetRegistration_PostReturns405: only GET/HEAD are allowed.
func TestHandleGetRegistration_PostReturns405(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	seedRegistrationFixture(t, edevs, regs)

	// Invoke the handler directly (no method-specific mux) so the handler's
	// own method gate fires.
	h := registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI))
	req := httptest.NewRequest(http.MethodPost, "/edev/"+deviceID+"/rg", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want to contain GET", allow)
	}
}

// TestHandleGetRegistration_EmptyIDReturns400: defensive guard on empty {id}.
func TestHandleGetRegistration_EmptyIDReturns400(t *testing.T) {
	edevs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()

	// Invoke without a mux so PathValue("id") is "".
	h := registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI))
	req := httptest.NewRequest(http.MethodGet, "/edev//rg", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleGetRegistration_RegistrationMissingHrefFilledFromPath: the handler
// back-fills the Href from the path when the stored record lacks one.
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
	reg := sep2.Registration{DateTimeRegistered: 1700000000, PIN: 42}
	if err := regs.Create(ctx, deviceID, reg); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/rg", registration.HandleGetRegistration(edevs, regs, identityOK(deviceLFDI, deviceSFDI)))

	req := httptest.NewRequest(http.MethodGet, "/edev/"+deviceID+"/rg", nil)
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
