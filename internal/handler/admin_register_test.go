package handler_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// #159 admin-registration handler tests.

// --- /api/certs/info -------------------------------------------------------

func TestCertInfoRawBody(t *testing.T) {
	certPEM, cert := freshDeviceCert(t)

	req := httptest.NewRequest(http.MethodPost, "/api/certs/info", bytes.NewReader(certPEM))
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["lfdi"] != sepTLS.LFDI(cert) {
		t.Errorf("lfdi mismatch: got %q want %q", got["lfdi"], sepTLS.LFDI(cert))
	}
	if got["sfdi"] != sepTLS.SFDI(cert) {
		t.Errorf("sfdi mismatch: got %q want %q", got["sfdi"], sepTLS.SFDI(cert))
	}
	// IEEE 2030.5 §6.11.7 device certs have empty Subject; subject field is
	// still emitted (an empty string), so we just assert it's a string key.
	if _, ok := got["subject"]; !ok {
		t.Error("subject field missing from response")
	}
	if got["fingerprint"] == "" || len(got["fingerprint"]) != 64 {
		t.Errorf("fingerprint must be 64 hex chars, got %q", got["fingerprint"])
	}
	if !strings.Contains(got["hardwareModuleName"], "TEST-admin-register") {
		t.Errorf("expected hardwareModuleName carrying TEST-admin-register, got %q", got["hardwareModuleName"])
	}
}

func TestCertInfoMultipartField(t *testing.T) {
	certPEM, cert := freshDeviceCert(t)

	body := &bytes.Buffer{}
	boundary := "----TestBoundary"
	fmt.Fprintf(body, "--%s\r\n", boundary)
	fmt.Fprintf(body, "Content-Disposition: form-data; name=\"cert\"\r\n\r\n")
	body.Write(certPEM)
	fmt.Fprintf(body, "\r\n--%s--\r\n", boundary)

	req := httptest.NewRequest(http.MethodPost, "/api/certs/info", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["lfdi"] != sepTLS.LFDI(cert) {
		t.Errorf("multipart lfdi mismatch: got %q", got["lfdi"])
	}
}

func TestCertInfoEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/certs/info", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", w.Code)
	}
}

func TestCertInfoMultipartFileUpload(t *testing.T) {
	certPEM, cert := freshDeviceCert(t)

	body := &bytes.Buffer{}
	boundary := "----FileBoundary"
	fmt.Fprintf(body, "--%s\r\n", boundary)
	fmt.Fprintf(body, "Content-Disposition: form-data; name=\"cert\"; filename=\"device.pem\"\r\n")
	fmt.Fprintf(body, "Content-Type: application/x-pem-file\r\n\r\n")
	body.Write(certPEM)
	fmt.Fprintf(body, "\r\n--%s--\r\n", boundary)

	req := httptest.NewRequest(http.MethodPost, "/api/certs/info", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for multipart file upload, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["lfdi"] != sepTLS.LFDI(cert) {
		t.Errorf("multipart-file lfdi mismatch: got %q", got["lfdi"])
	}
}

func TestCertInfoMultipartMissingField(t *testing.T) {
	body := &bytes.Buffer{}
	boundary := "----MissingBoundary"
	fmt.Fprintf(body, "--%s\r\n", boundary)
	fmt.Fprintf(body, "Content-Disposition: form-data; name=\"other\"\r\n\r\n")
	body.WriteString("not a cert")
	fmt.Fprintf(body, "\r\n--%s--\r\n", boundary)

	req := httptest.NewRequest(http.MethodPost, "/api/certs/info", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when multipart cert field is missing, got %d", w.Code)
	}
}

func TestCertInfoInvalidPEM(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/certs/info",
		bytes.NewReader([]byte("-----BEGIN CERTIFICATE-----\nnot-base64\n-----END CERTIFICATE-----\n")))
	w := httptest.NewRecorder()
	handler.HandleCertInfo()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed PEM, got %d", w.Code)
	}
}

// --- /api/devices/by-lfdi/{lfdi} -------------------------------------------

func TestLookupByLFDIFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	enabled := true
	want := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/12345678"},
		},
		SFDI: "167261211390", LFDI: "AABBCCDDEEFF00112233445566778899AABBCCDD",
		Enabled: &enabled,
	}
	if err := s.Create(context.Background(), "12345678", want); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/devices/by-lfdi/AABBCCDDEEFF00112233445566778899AABBCCDD", nil)
	req.SetPathValue("lfdi", "AABBCCDDEEFF00112233445566778899AABBCCDD")
	w := httptest.NewRecorder()
	handler.HandleDeviceLookupByLFDI(s)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Found  bool `json:"found"`
		Device struct {
			LFDI    string `json:"lfdi"`
			Href    string `json:"href"`
			Enabled bool   `json:"enabled"`
		} `json:"device"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found {
		t.Fatal("found should be true")
	}
	if got.Device.LFDI != want.LFDI {
		t.Errorf("device.lfdi mismatch: %q vs %q", got.Device.LFDI, want.LFDI)
	}
	if !got.Device.Enabled {
		t.Error("enabled should be true")
	}
}

func TestLookupByLFDINotFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	req := httptest.NewRequest(http.MethodGet, "/api/devices/by-lfdi/00000000000000000000000000000000DEADBEEF", nil)
	req.SetPathValue("lfdi", "00000000000000000000000000000000DEADBEEF")
	w := httptest.NewRecorder()
	handler.HandleDeviceLookupByLFDI(s)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (found:false is not an error), got %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["found"] != false {
		t.Errorf("expected found:false, got %v", got["found"])
	}
}

func TestLookupByLFDIInvalid(t *testing.T) {
	s := memory.NewEndDeviceStore()
	cases := []string{"", "too-short", strings.Repeat("Z", 40)}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/by-lfdi/"+c, nil)
		req.SetPathValue("lfdi", c)
		w := httptest.NewRecorder()
		handler.HandleDeviceLookupByLFDI(s)(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("input %q: expected 400, got %d", c, w.Code)
		}
	}
}

// --- /api/devices (POST add) -----------------------------------------------

// stubRegs records the most recent Create call so tests can assert PIN +
// DateTimeRegistered without depending on memory.Store semantics.
type stubRegs struct {
	mu   sync.Mutex
	last struct {
		id  string
		reg sep2.Registration
	}
	createErr error
}

func (s *stubRegs) Create(_ context.Context, id string, reg sep2.Registration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.last.id = id
	s.last.reg = reg
	return nil
}

func freshAddDeviceCert(t *testing.T) (sfdi, lfdi string) {
	t.Helper()
	_, cert := freshDeviceCert(t)
	return sepTLS.SFDI(cert), sepTLS.LFDI(cert)
}

func TestAddEndDevicePersistsRegistration(t *testing.T) {
	devStore := memory.NewEndDeviceStore()
	regs := &stubRegs{}

	sfdi, lfdi := freshAddDeviceCert(t)
	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"description":"Test inverter","pin":123456,"enabled":true}`, sfdi, lfdi)

	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devStore, regs)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}

	id := sfdi[:8]

	// Registration captured with the right PIN.
	if regs.last.id != id {
		t.Errorf("registration id mismatch: got %q want %q", regs.last.id, id)
	}
	if regs.last.reg.PIN != 123456 {
		t.Errorf("registration PIN: got %d want 123456", regs.last.reg.PIN)
	}
	if regs.last.reg.DateTimeRegistered == 0 {
		t.Error("DateTimeRegistered must be set")
	}
	if regs.last.reg.Href != "/edev/"+id+"/rg" {
		t.Errorf("registration href: got %q", regs.last.reg.Href)
	}

	// EndDevice persisted with the LFDI we sent (uppercased).
	got, err := devStore.GetByLFDI(context.Background(), lfdi)
	if err != nil {
		t.Fatalf("GetByLFDI: %v", err)
	}
	if got.SFDI != sfdi {
		t.Errorf("end device SFDI mismatch: got %q", got.SFDI)
	}
	if got.Enabled == nil || !*got.Enabled {
		t.Error("end device must be enabled per request")
	}
}

func TestAddEndDeviceCollisionOnLFDI(t *testing.T) {
	devStore := memory.NewEndDeviceStore()
	regs := &stubRegs{}

	sfdi, lfdi := freshAddDeviceCert(t)
	// Pre-seed.
	enabled := true
	id := sfdi[:8]
	if err := devStore.Create(context.Background(), id, sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + id}},
		SFDI:                 sfdi, LFDI: lfdi, Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"description":"dup","pin":1,"enabled":true}`, sfdi, lfdi)
	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devStore, regs)(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 on LFDI collision, got %d", w.Code)
	}
}

func TestAddEndDeviceValidation(t *testing.T) {
	validSFDI, validLFDI := freshAddDeviceCert(t)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"bad json", `not json`, 400},
		{"unknown field", `{"sfdi":"` + validSFDI + `","lfdi":"` + validLFDI + `","badField":1}`, 400},
		{"bad sfdi length", `{"sfdi":"123","lfdi":"` + validLFDI + `","pin":1}`, 400},
		{"bad sfdi check digit", `{"sfdi":"167261211392","lfdi":"` + validLFDI + `","pin":1}`, 400},
		{"bad lfdi length", `{"sfdi":"` + validSFDI + `","lfdi":"deadbeef","pin":1}`, 400},
		{"bad lfdi hex", `{"sfdi":"` + validSFDI + `","lfdi":"` + strings.Repeat("Z", 40) + `","pin":1}`, 400},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.HandleAddEndDevice(memory.NewEndDeviceStore(), &stubRegs{})(w, req)
			if w.Code != c.code {
				t.Errorf("case %s: expected %d, got %d body=%s", c.name, c.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestAddEndDeviceRegistrationFailureRollsBack(t *testing.T) {
	devStore := memory.NewEndDeviceStore()
	regs := &stubRegs{createErr: fmt.Errorf("regs down")}

	sfdi, lfdi := freshAddDeviceCert(t)
	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"description":"x","pin":1,"enabled":true}`, sfdi, lfdi)
	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devStore, regs)(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when regs.Create fails, got %d", w.Code)
	}
	// EndDevice should have been rolled back.
	if _, err := devStore.GetByLFDI(context.Background(), lfdi); err == nil {
		t.Error("expected EndDevice to be rolled back on regs.Create failure")
	}
}

func TestAddEndDeviceRequiresRegsStore(t *testing.T) {
	devStore := memory.NewEndDeviceStore()
	sfdi, lfdi := freshAddDeviceCert(t)
	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"pin":1}`, sfdi, lfdi)
	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devStore, nil)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when regs writer is nil, got %d", w.Code)
	}
}

// --- helpers ---------------------------------------------------------------

func freshDeviceCert(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "admin-register Test CA", ValidYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	caCert, caKey, err := parseCAPair(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-admin-register",
	})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, cert
}

func parseCAPair(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, fmt.Errorf("no cert PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, fmt.Errorf("no key PEM")
	}
	raw, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, raw.(*ecdsa.PrivateKey), nil
}

// avoid "imported and not used" if a future refactor drops store.
var _ = store.ErrNotFound
