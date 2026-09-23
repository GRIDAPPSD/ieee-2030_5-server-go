package handler_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// TestHandleCreateServerCertInvalidJSONDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for the admin plane's JSON envelope: a fixed body,
// decoder detail (which can quote attacker-supplied content, e.g. an
// oversized numeric literal echoed verbatim in a json.UnmarshalTypeError)
// left to the operator-facing log.
func TestHandleCreateServerCertInvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "13370360913370360913370360"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"hosts":["localhost"],"validYears":` + marker + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// TestHandleCreateDeviceCertInvalidJSONDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for HandleCreateDeviceCert's JSON decode site: an
// oversized numeric literal is echoed verbatim in a json.UnmarshalTypeError,
// which is attacker-supplied content the client-visible body must not carry.
func TestHandleCreateDeviceCertInvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "13370360913370360913370360913370360913370360"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"deviceType":` + marker + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// TestHandleCreateDeviceCertInvalidHWTypeDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for HandleCreateDeviceCert's hwType OID parse site,
// which certs.ParseOID's error quotes verbatim.
func TestHandleCreateDeviceCertInvalidHWTypeDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"hwSerialNum":"HW1","hwType":"` + marker + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"invalid hwType"}` {
		t.Errorf("body = %q, want the fixed message envelope", got)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

func TestHandleGetCA(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.CertPEM == "" {
		t.Error("certPEM should not be empty")
	}
	// Verify it doesn't contain a private key
	if bytes.Contains([]byte(resp.CertPEM), []byte("PRIVATE KEY")) {
		t.Error("CA response must NOT contain private key")
	}
}

// TestHandleGetCACombinedPEMServesCertOnly pins #644: a combined PEM file
// (certificate followed by its private key, a layout some tooling produces)
// must not echo the key to the caller of the route an operator uses to fetch
// the anchor for a device. The control (bytes.Contains for "PRIVATE KEY")
// reproduces RED against the pre-fix handler, which serves s.caCertPEM
// verbatim; it goes GREEN once the handler filters to CERTIFICATE blocks.
func TestHandleGetCACombinedPEMServesCertOnly(t *testing.T) {
	certPEM, keyPEM, caCert := newCAPEM(t)
	combined := append(append([]byte{}, certPEM...), keyPEM...)

	svc := handler.NewAdminCertService(caCert, nil, combined)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if bytes.Contains([]byte(resp.CertPEM), []byte("PRIVATE KEY")) {
		t.Fatalf("certPEM leaked a private key block: %q", resp.CertPEM)
	}
	block, rest := pem.Decode([]byte(resp.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certPEM did not decode to a CERTIFICATE block: %q", resp.CertPEM)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("certPEM carried trailing PEM content after the certificate: %q", rest)
	}
	if !bytes.Equal(block.Bytes, caCert.Raw) {
		t.Error("certPEM decoded to a different certificate than the CA's")
	}
}

// TestHandleGetCAServesEveryCertificateBlock covers the three file shapes
// #644 names: a combined cert-and-key file, a multi-certificate file (a
// chain), and a certificate with trailing non-PEM text. Every case must
// yield a response holding every CERTIFICATE block and nothing else.
func TestHandleGetCAServesEveryCertificateBlock(t *testing.T) {
	cert1PEM, keyPEM, cert1 := newCAPEM(t)
	cert2PEM, _, cert2 := newCAPEM(t)

	tests := []struct {
		name      string
		file      []byte
		wantCerts [][]byte // raw DER bytes expected, in order
	}{
		{
			name:      "certificate and key",
			file:      append(append([]byte{}, cert1PEM...), keyPEM...),
			wantCerts: [][]byte{cert1.Raw},
		},
		{
			name:      "certificate chain",
			file:      append(append([]byte{}, cert1PEM...), cert2PEM...),
			wantCerts: [][]byte{cert1.Raw, cert2.Raw},
		},
		{
			name:      "certificate with trailing text",
			file:      append(append([]byte{}, cert1PEM...), []byte("# comment appended by some tooling\n")...),
			wantCerts: [][]byte{cert1.Raw},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := handler.NewAdminCertService(cert1, nil, tt.file)
			h := svc.HandleGetCA()

			req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			var resp struct {
				CertPEM string `json:"certPEM"`
			}
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if bytes.Contains([]byte(resp.CertPEM), []byte("PRIVATE KEY")) {
				t.Fatalf("certPEM leaked a private key block: %q", resp.CertPEM)
			}

			rest := []byte(resp.CertPEM)
			var got [][]byte
			for {
				var block *pem.Block
				block, rest = pem.Decode(rest)
				if block == nil {
					break
				}
				if block.Type != "CERTIFICATE" {
					t.Fatalf("non-certificate block %q reached the response", block.Type)
				}
				got = append(got, block.Bytes)
			}
			if len(bytes.TrimSpace(rest)) != 0 {
				t.Errorf("trailing non-PEM content reached the response: %q", rest)
			}
			if len(got) != len(tt.wantCerts) {
				t.Fatalf("got %d certificate blocks, want %d", len(got), len(tt.wantCerts))
			}
			for i, want := range tt.wantCerts {
				if !bytes.Equal(got[i], want) {
					t.Errorf("block %d: certificate DER did not match", i)
				}
			}
		})
	}
}

// TestHandleGetCACertOnlyFileIsByteForByteUnchanged pins the invariant that a
// certificate-only file passes through unmodified: the route's fix for #644
// must filter, never re-encode, or a source file wrapped at a different line
// length than Go's pem.Encode would come back changed.
func TestHandleGetCACertOnlyFileIsByteForByteUnchanged(t *testing.T) {
	certPEM, _, caCert := newCAPEM(t)

	svc := handler.NewAdminCertService(caCert, nil, certPEM)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CertPEM != string(certPEM) {
		t.Errorf("certificate-only response changed:\n got: %q\nwant: %q", resp.CertPEM, string(certPEM))
	}
}

// TestHandleGetCADropsBlockSkippedBeforeAndBetweenCertificates pins #644's
// escaped bug: the filter previously copied the span since the *previous*
// block ended, not the matched block's own bytes, so a block pem.Decode
// skipped (a mismatched END label, no END line, or malformed base64) was
// folded into whichever certificate followed it. The request runs over a
// real listening socket, not httptest.NewRecorder, because the property
// under test is what a caller on the wire receives.
func TestHandleGetCADropsBlockSkippedBeforeAndBetweenCertificates(t *testing.T) {
	cert1PEM, keyPEM, cert1 := newCAPEM(t)
	cert2PEM, _, _ := newCAPEM(t)

	// A key block whose END label does not match its BEGIN label: pem.Decode
	// skips past it entirely rather than returning it as a block.
	mismatchedEnd := bytes.Replace(append([]byte{}, keyPEM...), []byte("-----END PRIVATE KEY-----"), []byte("-----END WRONG LABEL-----"), 1)

	tests := []struct {
		name string
		file []byte
	}{
		{
			name: "skipped block precedes the only certificate",
			file: append(append([]byte{}, mismatchedEnd...), cert1PEM...),
		},
		{
			name: "skipped block sits between two certificates",
			file: append(append(append([]byte{}, cert1PEM...), mismatchedEnd...), cert2PEM...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := handler.NewAdminCertService(cert1, nil, tt.file)
			mux := http.NewServeMux()
			mux.HandleFunc("/api/certs/ca", svc.HandleGetCA())
			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/certs/ca")
			if err != nil {
				t.Fatalf("GET %s: %v", srv.URL, err)
			}
			defer func() { _ = resp.Body.Close() }()
			var out struct {
				CertPEM string `json:"certPEM"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if bytes.Contains([]byte(out.CertPEM), []byte("PRIVATE KEY")) {
				t.Fatalf("certPEM leaked the skipped block's key material: %q", out.CertPEM)
			}
			if bytes.Contains([]byte(out.CertPEM), []byte("WRONG LABEL")) {
				t.Fatalf("certPEM leaked the skipped block's mismatched trailer: %q", out.CertPEM)
			}
		})
	}
}

// TestHandleGetCANoCertificateBlockIsRefused pins item 2 of the #644 fix
// round: a stored file that decodes to no certificate block at all (here, a
// key-only file) is not a 200 success. A scripted caller writes this
// response straight to a trust anchor file, so an empty "success" is a
// silent failure; the route refuses instead.
func TestHandleGetCANoCertificateBlockIsRefused(t *testing.T) {
	_, keyPEM, caCert := newCAPEM(t)

	svc := handler.NewAdminCertService(caCert, nil, keyPEM)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("status = 200, want a non-2xx refusal for a file with no certificate block")
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Errorf("refusal body leaked key material: %q", w.Body.String())
	}
}

// TestHandleGetCAAcceptsLegacyX509CertificateLabel pins item 3: LoadCA does
// not check block.Type before parsing, so a CA file carrying the legacy
// OpenSSL "X509 CERTIFICATE" label loads and signs correctly. The download
// route must not silently empty such a file just because its label predates
// the modern "CERTIFICATE" convention.
func TestHandleGetCAAcceptsLegacyX509CertificateLabel(t *testing.T) {
	certPEM, _, caCert := newCAPEM(t)
	legacy := bytes.Replace(append([]byte{}, certPEM...), []byte("CERTIFICATE"), []byte("X509 CERTIFICATE"), 2)

	svc := handler.NewAdminCertService(caCert, nil, legacy)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var out struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.CertPEM == "" {
		t.Fatal("certPEM is empty for a legacy-labeled CA that loads and signs correctly")
	}
	block, _ := pem.Decode([]byte(out.CertPEM))
	if block == nil || !bytes.Equal(block.Bytes, caCert.Raw) {
		t.Errorf("certPEM did not decode to the CA certificate: %q", out.CertPEM)
	}
}

// newCAPEM generates a CA certificate and key pair and returns the encoded
// PEM bytes alongside the parsed certificate, for constructing
// AdminCertService fixtures directly (the private key type is irrelevant to
// HandleGetCA).
func newCAPEM(t *testing.T) (certPEM, keyPEM []byte, cert *x509.Certificate) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, cert
}

func TestHandleCreateServerCert(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"hosts":["localhost","127.0.0.1"],"commonName":"Test","validYears":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
		KeyPEM  string `json:"keyPEM"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.CertPEM == "" || resp.KeyPEM == "" {
		t.Error("response should contain certPEM and keyPEM")
	}
}

func TestHandleCreateServerCertMissingHosts(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"commonName":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCreateDeviceCert(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"deviceType":1,"hwSerialNum":"INV-001","hwType":"1.3.6.1.4.1.40732.99"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
		KeyPEM  string `json:"keyPEM"`
		SFDI    string `json:"sfdi"`
		LFDI    string `json:"lfdi"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if resp.CertPEM == "" || resp.KeyPEM == "" {
		t.Error("should contain certPEM and keyPEM")
	}
	if len(resp.SFDI) != 12 {
		t.Errorf("SFDI length = %d, want 12", len(resp.SFDI))
	}
	if !sepTLS.ValidateSFDI(resp.SFDI) {
		t.Errorf("SFDI %q has invalid checksum", resp.SFDI)
	}
	if len(resp.LFDI) != 40 {
		t.Errorf("LFDI length = %d, want 40", len(resp.LFDI))
	}
}

func TestHandleCreateDeviceCertInvalidJSON(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString("{bad"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleCertDeviceTypesReturnsAllThree asserts the field values the
// endpoint returns, not just a 200: value, name and label for each of the
// three types certs.AllDeviceTypes defines.
func TestHandleCertDeviceTypesReturnsAllThree(t *testing.T) {
	h := handler.HandleCertDeviceTypes()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/device-types", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		DeviceTypes []struct {
			Value int    `json:"value"`
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"deviceTypes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := map[int]struct{ name, label string }{
		1: {"generic", "Generic"},
		2: {"mobile", "Mobile"},
		3: {"post_manufacture", "Post-Manufacture"},
	}
	if len(resp.DeviceTypes) != len(want) {
		t.Fatalf("got %d device types, want %d: %+v", len(resp.DeviceTypes), len(want), resp.DeviceTypes)
	}
	seen := make(map[int]bool, len(resp.DeviceTypes))
	for _, dt := range resp.DeviceTypes {
		w, ok := want[dt.Value]
		if !ok {
			t.Errorf("unexpected value %d in response", dt.Value)
			continue
		}
		if dt.Name != w.name || dt.Label != w.label {
			t.Errorf("value %d: got name=%q label=%q, want name=%q label=%q", dt.Value, dt.Name, dt.Label, w.name, w.label)
		}
		seen[dt.Value] = true
	}
	for v := range want {
		if !seen[v] {
			t.Errorf("value %d missing from response", v)
		}
	}
}

// TestHandleCertDeviceTypesValuesRoundTripThroughMint proves each value the
// device-types endpoint returns is accepted by HandleCreateDeviceCert and
// produces a certificate carrying that same type's OID, not a silently
// clamped Generic (admin_certs.go's decode path clamps an out-of-range
// deviceType to 1).
func TestHandleCertDeviceTypesValuesRoundTripThroughMint(t *testing.T) {
	listH := handler.HandleCertDeviceTypes()
	listReq := httptest.NewRequest(http.MethodGet, "/api/certs/device-types", nil)
	listW := httptest.NewRecorder()
	listH.ServeHTTP(listW, listReq)

	var listResp struct {
		DeviceTypes []struct {
			Value int `json:"value"`
		} `json:"deviceTypes"`
	}
	if err := json.NewDecoder(listW.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode device-types response: %v", err)
	}
	if len(listResp.DeviceTypes) == 0 {
		t.Fatal("device-types response listed no values to round-trip")
	}

	svc := newTestCertService(t)
	mintH := svc.HandleCreateDeviceCert()

	for _, dt := range listResp.DeviceTypes {
		body := fmt.Sprintf(`{"deviceType":%d,"hwSerialNum":"RT-%d","hwType":"1.3.6.1.4.1.40732.99"}`, dt.Value, dt.Value)
		req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		mintH.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("deviceType %d: status = %d, want 201, body: %s", dt.Value, w.Code, w.Body.String())
		}

		var mintResp struct {
			CertPEM string `json:"certPEM"`
		}
		if err := json.NewDecoder(w.Body).Decode(&mintResp); err != nil {
			t.Fatalf("deviceType %d: decode mint response: %v", dt.Value, err)
		}

		block, _ := pem.Decode([]byte(mintResp.CertPEM))
		if block == nil {
			t.Fatalf("deviceType %d: certPEM did not decode", dt.Value)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("deviceType %d: parse cert: %v", dt.Value, err)
		}
		wantOID := certs.DeviceType(dt.Value).OID()
		if !certs.HasPolicyOID(cert, wantOID) {
			t.Errorf("deviceType %d: minted cert missing policy OID %v", dt.Value, wantOID)
		}
	}
}

func newTestCertService(t *testing.T) *handler.AdminCertService {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(block.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	raw, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)

	return handler.NewAdminCertService(caCert, raw.(*ecdsa.PrivateKey), caCertPEM)
}
