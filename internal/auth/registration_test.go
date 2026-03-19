package auth_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestAutoRegistrationCreatesDevice(t *testing.T) {
	store := memory.NewEndDeviceStore()

	// Wrap identity + auto-registration middleware
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	handler := auth.IdentityMiddleware(
		auth.AutoRegistrationMiddleware(store, auth.RegistrationModeAuto)(inner),
	)

	cert := generateDeviceCert(t)
	req := httptest.NewRequest(http.MethodGet, "/edev", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	// Verify device was created in store
	count, _ := store.Count(context.Background())
	if count != 1 {
		t.Errorf("device count = %d, want 1 (auto-registered)", count)
	}
}

func TestAutoRegistrationSkipsExisting(t *testing.T) {
	store := memory.NewEndDeviceStore()

	// Pre-register a device
	dev := sep2.EndDevice{SFDI: "123456789012", LFDI: "AABB"}
	dev.Href = "/edev/12345678"
	store.Create(context.Background(), "12345678", dev)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.GetDeviceID(r.Context())
		if !ok {
			t.Error("device ID should be in context")
		}
		if id != "12345678" {
			t.Errorf("device ID = %q, want 12345678", id)
		}
		w.WriteHeader(200)
	})

	handler := auth.AutoRegistrationMiddleware(store, auth.RegistrationModeAuto)(inner)

	// Simulate identity already in context
	identity := auth.DeviceIdentity{SFDI: "123456789012", LFDI: "AABB"}
	ctx := context.WithValue(context.Background(), auth.IdentityContextKey(), identity)
	req := httptest.NewRequest(http.MethodGet, "/edev", nil).WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	// Count should still be 1
	count, _ := store.Count(context.Background())
	if count != 1 {
		t.Errorf("count = %d, want 1 (no duplicate)", count)
	}
}

func TestManualModeDoesNotAutoRegister(t *testing.T) {
	store := memory.NewEndDeviceStore()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	handler := auth.AutoRegistrationMiddleware(store, auth.RegistrationModeManual)(inner)

	identity := auth.DeviceIdentity{SFDI: "999999999999", LFDI: "CCCC"}
	ctx := context.WithValue(context.Background(), auth.IdentityContextKey(), identity)
	req := httptest.NewRequest(http.MethodGet, "/edev", nil).WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	count, _ := store.Count(context.Background())
	if count != 0 {
		t.Errorf("manual mode should not auto-register, count = %d", count)
	}
}
