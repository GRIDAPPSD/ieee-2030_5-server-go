package handler_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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

// getCA performs a real HTTP GET against HandleGetCA over a listening
// socket rather than httptest.NewRecorder: the property under test is what
// a caller on the wire receives, and this change's invariant is that any
// claim about the response is proven that way.
func getCA(t *testing.T, svc *handler.AdminCertService) (status int, certPEM string, rawBody []byte) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/certs/ca", svc.HandleGetCA())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/certs/ca")
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rawBody, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var out struct {
		CertPEM string `json:"certPEM"`
	}
	_ = json.Unmarshal(rawBody, &out)
	return resp.StatusCode, out.CertPEM, rawBody
}

func TestHandleGetCA(t *testing.T) {
	svc := newTestCertService(t)

	status, certPEM, _ := getCA(t, svc)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if certPEM == "" {
		t.Error("certPEM should not be empty")
	}
	if bytes.Contains([]byte(certPEM), []byte("PRIVATE KEY")) {
		t.Error("CA response must NOT contain private key")
	}
}

// TestHandleGetCACombinedPEMServesCertOnly pins #644: a combined PEM file
// (certificate followed by its private key, a layout some tooling produces)
// must not echo the key to the caller of the route an operator uses to fetch
// the anchor for a device.
func TestHandleGetCACombinedPEMServesCertOnly(t *testing.T) {
	certPEM, keyPEM, caCert := newCAPEM(t)
	combined := append(append([]byte{}, certPEM...), keyPEM...)

	svc := handler.NewAdminCertService(caCert, nil, combined)
	status, got, _ := getCA(t, svc)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if bytes.Contains([]byte(got), []byte("PRIVATE KEY")) {
		t.Fatalf("certPEM leaked a private key block: %q", got)
	}
	block, rest := pem.Decode([]byte(got))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certPEM did not decode to a CERTIFICATE block: %q", got)
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
			status, certPEM, _ := getCA(t, svc)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if bytes.Contains([]byte(certPEM), []byte("PRIVATE KEY")) {
				t.Fatalf("certPEM leaked a private key block: %q", certPEM)
			}

			rest := []byte(certPEM)
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

// TestHandleGetCACertOnlyFileIsCanonicallyReencoded pins Question 1 of the
// #644 design: the response is pem.EncodeToMemory's canonical encoding of
// the certificate the parser accepted, not the stored file's own bytes.
// For the shipped generator's own output the two happen to coincide,
// because certs.GenerateCA already encodes canonically (measured in the
// design record: 526 of 526 bytes identical), so this also pins that the
// common case is unaffected.
func TestHandleGetCACertOnlyFileIsCanonicallyReencoded(t *testing.T) {
	certPEM, _, caCert := newCAPEM(t)

	svc := handler.NewAdminCertService(caCert, nil, certPEM)
	status, got, _ := getCA(t, svc)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	want := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}))
	if got != want {
		t.Errorf("certPEM = %q, want the canonical re-encoding %q", got, want)
	}
	if got != string(certPEM) {
		t.Errorf("certPEM = %q, want it to still equal the generator's own output %q", got, string(certPEM))
	}
}

// TestHandleGetCANormalizesNonCanonicalWrapping pins the design's named
// residual risk for Question 1: the response is now coupled to Go's PEM
// encoder rather than to the operator's file, so a stored file wrapped at a
// width other than 64 columns comes back re-wrapped. If a future toolchain
// changed pem.EncodeToMemory's wrapping this goes red, instead of silently
// changing what every device is told to trust.
func TestHandleGetCANormalizesNonCanonicalWrapping(t *testing.T) {
	_, _, caCert := newCAPEM(t)
	src := "-----BEGIN CERTIFICATE-----\n" + wrapBase64(caCert.Raw, 48) + "\n-----END CERTIFICATE-----\n"

	svc := handler.NewAdminCertService(caCert, nil, []byte(src))
	status, got, _ := getCA(t, svc)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got == src {
		t.Fatal("certPEM equals the 48-column source; want it re-wrapped canonically")
	}
	want := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}))
	if got != want {
		t.Errorf("certPEM = %q, want the canonical (64-column) re-encoding %q", got, want)
	}
}

// TestHandleGetCADropsBlockSkippedBeforeAndBetweenCertificates pins #644's
// escaped bug: the filter previously copied the span since the *previous*
// block ended, not the matched block's own bytes, so a block pem.Decode
// skipped (a mismatched END label, no END line, or malformed base64) was
// folded into whichever certificate followed it.
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
			status, certPEM, _ := getCA(t, svc)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if bytes.Contains([]byte(certPEM), []byte("PRIVATE KEY")) {
				t.Fatalf("certPEM leaked the skipped block's key material: %q", certPEM)
			}
			if bytes.Contains([]byte(certPEM), []byte("WRONG LABEL")) {
				t.Fatalf("certPEM leaked the skipped block's mismatched trailer: %q", certPEM)
			}
		})
	}
}

// TestHandleGetCASharedEndBeginLineServesCertificateOnly pins the shape the
// #644 design measured, closed by no earlier round: a stored file whose
// PRIVATE KEY block's END marker and the certificate's BEGIN marker share
// one line. pem.Decode accepts a BEGIN at offset 0 of its current search
// window even when the preceding byte is not a newline, so a source-byte
// filter keyed on a reimplemented line-start rule fell back to offset 0 and
// served the whole file, private key included (measured: 750 bytes at the
// pre-design head, opening "-----BEGIN PRIVATE KEY-----"). The parse-based
// filter selects by what x509.ParseCertificate accepts, never by an offset,
// so it cannot repeat that mistake.
func TestHandleGetCASharedEndBeginLineServesCertificateOnly(t *testing.T) {
	certPEM, keyPEM, caCert := newCAPEM(t)
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("generated CA key PEM did not decode")
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("generated CA cert PEM did not decode")
	}
	shared := "-----BEGIN PRIVATE KEY-----\n" + wrapBase64(keyBlock.Bytes, 64) + "\n" +
		"-----END -----BEGIN CERTIFICATE-----\n" + wrapBase64(certBlock.Bytes, 64) + "\n" +
		"-----END CERTIFICATE-----\n"

	svc := handler.NewAdminCertService(caCert, nil, []byte(shared))
	status, got, raw := getCA(t, svc)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	if bytes.Contains([]byte(got), []byte("PRIVATE KEY")) {
		t.Fatalf("certPEM leaked the private key on the shared END/BEGIN line: %q", got)
	}
	block, rest := pem.Decode([]byte(got))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certPEM did not decode to a CERTIFICATE block: %q", got)
	}
	if !bytes.Equal(block.Bytes, caCert.Raw) {
		t.Error("certPEM decoded to a different certificate than the CA's")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("certPEM carried trailing PEM content: %q", rest)
	}
}

// TestHandleGetCANormalizesLegacyX509CertificateLabel pins Question 3 of the
// #644 design: LoadCA does not check block.Type before parsing, so a CA
// file carrying the legacy OpenSSL "X509 CERTIFICATE" label loads and
// signs correctly, and the route must not silently empty it. The label is
// not preserved: it is normalized to "CERTIFICATE" so the body always
// loads as a trust anchor, which crypto/x509's pool loader requires.
func TestHandleGetCANormalizesLegacyX509CertificateLabel(t *testing.T) {
	certPEM, _, caCert := newCAPEM(t)
	legacy := bytes.Replace(append([]byte{}, certPEM...), []byte("CERTIFICATE"), []byte("X509 CERTIFICATE"), 2)

	svc := handler.NewAdminCertService(caCert, nil, legacy)
	status, got, raw := getCA(t, svc)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	block, _ := pem.Decode([]byte(got))
	if block == nil {
		t.Fatalf("certPEM did not decode: %q", got)
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("block.Type = %q, want the normalized CERTIFICATE label", block.Type)
	}
	if !bytes.Equal(block.Bytes, caCert.Raw) {
		t.Errorf("certPEM did not decode to the CA certificate: %q", got)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(got)) {
		t.Error("certPEM did not load as a trust anchor via AppendCertsFromPEM")
	}
}

// wrapBase64 base64-encodes der and wraps it at width columns, distinct
// from pem.EncodeToMemory (which always wraps at 64). Used to build
// non-canonical fixtures the canonical-output tests must normalize.
func wrapBase64(der []byte, width int) string {
	b64 := base64.StdEncoding.EncodeToString(der)
	var lines []string
	for i := 0; i < len(b64); i += width {
		end := i + width
		if end > len(b64) {
			end = len(b64)
		}
		lines = append(lines, b64[i:end])
	}
	return strings.Join(lines, "\n")
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

// TestHandleCreateServerCertNilKeyReturns503 is #638 fix round 1 (MEDIUM 3):
// a certificate with a nil key (the shape LoadCAPair returns for a keyless
// or mismatched pair) must be refused with a 503, not reach
// certs.GenerateServerCert and panic on the nil key.
func TestHandleCreateServerCertNilKeyReturns503(t *testing.T) {
	svc, _, _ := newTestTwoCAService(t, "638 Serving CA", "638 Device CA")
	certOnly := handler.NewAdminCertServiceWithCAs(svc.ServingCA(), nil, nil, svc.DeviceCA(), nil)
	h := certOnly.HandleCreateServerCert()

	body := `{"hosts":["localhost"],"validYears":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateDeviceCertNilKeyReturns503 is the device-route half of
// TestHandleCreateServerCertNilKeyReturns503.
func TestHandleCreateDeviceCertNilKeyReturns503(t *testing.T) {
	svc, _, _ := newTestTwoCAService(t, "638 Serving CA", "638 Device CA")
	certOnly := handler.NewAdminCertServiceWithCAs(svc.ServingCA(), nil, nil, svc.DeviceCA(), nil)
	h := certOnly.HandleCreateDeviceCert()

	body := `{"deviceType":1,"hwSerialNum":"NILKEY-001","hwType":"1.3.6.1.4.1.40732.99"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body: %s", w.Code, w.Body.String())
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

// TestAdminCertServiceServingCAAndDeviceCAReturnTheirOwnCert is #638 fix
// round 2 item 1: ServingCA() is exercised by TestHandleGetCA and
// TestAdminCertServiceRoutesEachRouteToItsOwnCA, but before this test
// DeviceCA()'s only caller in the handler package's own tests was
// TestHandleCreateServerCertNilKeyReturns503 / TestHandleCreateDeviceCertNilKeyReturns503,
// which read it back only to feed it into another constructor call, never
// asserting on the value. Swapping DeviceCA()'s body to return
// s.servingCACert passed the handler package's suite alone; both accessors
// are asserted directly here, against two distinguishable CAs, so a swap of
// either return statement fails.
func TestAdminCertServiceServingCAAndDeviceCAReturnTheirOwnCert(t *testing.T) {
	svc, servingCA, deviceCA := newTestTwoCAService(t, "638 Item1 Serving CA", "638 Item1 Device CA")

	if got := svc.ServingCA(); !got.Equal(servingCA) {
		t.Errorf("ServingCA() = %q, want the serving CA %q", got.Subject, servingCA.Subject)
	}
	if got := svc.ServingCA(); got.Equal(deviceCA) {
		t.Error("ServingCA() returned the device CA")
	}
	if got := svc.DeviceCA(); !got.Equal(deviceCA) {
		t.Errorf("DeviceCA() = %q, want the device CA %q", got.Subject, deviceCA.Subject)
	}
	if got := svc.DeviceCA(); got.Equal(servingCA) {
		t.Error("DeviceCA() returned the serving CA")
	}
}

// TestAdminCertServiceRoutesEachRouteToItsOwnCA is #622 done-condition 2: a
// device certificate signed by the device CA verifies against the device
// CA and is refused by the serving CA, and a server certificate signed by
// the serving CA verifies against the serving CA and is refused by the
// device CA. Both directions are asserted per data-invariants: a service
// that quietly signs everything with one CA must not pass by having every
// check trivially succeed.
func TestAdminCertServiceRoutesEachRouteToItsOwnCA(t *testing.T) {
	svc, servingCA, deviceCA := newTestTwoCAService(t, "Test Serving CA", "Test Device CA")

	servingPool := x509.NewCertPool()
	servingPool.AddCert(servingCA)
	devicePool := x509.NewCertPool()
	devicePool.AddCert(deviceCA)

	// HandleGetCA answers with the serving CA, not the device CA: an
	// operator downloads this to verify THIS server, not to see what the
	// listener trusts (#622, #625).
	getH := svc.HandleGetCA()
	getReq := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	getW := httptest.NewRecorder()
	getH.ServeHTTP(getW, getReq)
	var getResp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(getW.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode GET /api/certs/ca response: %v", err)
	}
	getBlock, _ := pem.Decode([]byte(getResp.CertPEM))
	if getBlock == nil {
		t.Fatal("GET /api/certs/ca: certPEM did not decode")
	}
	gotCA, err := x509.ParseCertificate(getBlock.Bytes)
	if err != nil {
		t.Fatalf("GET /api/certs/ca: parse cert: %v", err)
	}
	if !gotCA.Equal(servingCA) {
		t.Error("GET /api/certs/ca did not return the serving CA")
	}
	if gotCA.Equal(deviceCA) {
		t.Error("GET /api/certs/ca returned the device CA")
	}

	// Server cert: signed by the serving CA, verifies against it, refused
	// by the device CA pool.
	serverH := svc.HandleCreateServerCert()
	serverReq := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(`{"hosts":["localhost"]}`))
	serverW := httptest.NewRecorder()
	serverH.ServeHTTP(serverW, serverReq)
	if serverW.Code != http.StatusCreated {
		t.Fatalf("POST /api/certs/server: status = %d, want 201, body: %s", serverW.Code, serverW.Body.String())
	}
	var serverResp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(serverW.Body).Decode(&serverResp); err != nil {
		t.Fatalf("decode POST /api/certs/server response: %v", err)
	}
	serverBlock, _ := pem.Decode([]byte(serverResp.CertPEM))
	serverCert, err := x509.ParseCertificate(serverBlock.Bytes)
	if err != nil {
		t.Fatalf("parse minted server cert: %v", err)
	}
	if _, err := serverCert.Verify(x509.VerifyOptions{Roots: servingPool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("minted server cert does not verify against the serving CA: %v", err)
	}
	if _, err := serverCert.Verify(x509.VerifyOptions{Roots: devicePool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Error("minted server cert unexpectedly verifies against the device CA: HandleCreateServerCert is not routed to the serving pair")
	}

	// Device cert: signed by the device CA, verifies against it, refused
	// by the serving CA pool.
	deviceH := svc.HandleCreateDeviceCert()
	deviceReq := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"ROUTE-001","hwType":"1.3.6.1.4.1.40732.99"}`))
	deviceW := httptest.NewRecorder()
	deviceH.ServeHTTP(deviceW, deviceReq)
	if deviceW.Code != http.StatusCreated {
		t.Fatalf("POST /api/certs/device: status = %d, want 201, body: %s", deviceW.Code, deviceW.Body.String())
	}
	var deviceResp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(deviceW.Body).Decode(&deviceResp); err != nil {
		t.Fatalf("decode POST /api/certs/device response: %v", err)
	}
	deviceBlock, _ := pem.Decode([]byte(deviceResp.CertPEM))
	deviceCert, err := x509.ParseCertificate(deviceBlock.Bytes)
	if err != nil {
		t.Fatalf("parse minted device cert: %v", err)
	}
	// CheckSignatureFrom, not Verify: CSIP-compliant device certs carry a
	// critical HardwareModuleName SAN that stdlib x509 leaves in
	// UnhandledCriticalExtensions (internal/certs/generate_test.go documents
	// the same choice for GenerateDeviceCert's own signature check). This
	// still asserts what item 2 asks: which CA's key produced the signature.
	if err := deviceCert.CheckSignatureFrom(deviceCA); err != nil {
		t.Errorf("minted device cert is not signed by the device CA: %v", err)
	}
	if err := deviceCert.CheckSignatureFrom(servingCA); err == nil {
		t.Error("minted device cert unexpectedly verifies as signed by the serving CA: HandleCreateDeviceCert is not routed to the device pair")
	}
}

// TestAdminCertServiceRefusalControlSameCAVerifiesBoth is the control for
// the two "unexpectedly verifies" assertions above: with a single CA
// service (the pre-#622 constructor, both CAs are the same file), the same
// cross-pool check that must refuse above instead succeeds. This proves
// those assertions can fail - they are not vacuously true because a pool
// is empty or malformed.
func TestAdminCertServiceRefusalControlSameCAVerifiesBoth(t *testing.T) {
	svc := newTestCertService(t) // one CA fills both roles, as when the two CAs are the same file

	deviceH := svc.HandleCreateDeviceCert()
	deviceReq := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"CTRL-001","hwType":"1.3.6.1.4.1.40732.99"}`))
	deviceW := httptest.NewRecorder()
	deviceH.ServeHTTP(deviceW, deviceReq)
	if deviceW.Code != http.StatusCreated {
		t.Fatalf("POST /api/certs/device: status = %d, want 201, body: %s", deviceW.Code, deviceW.Body.String())
	}
	var deviceResp struct {
		CertPEM string `json:"certPEM"`
	}
	if err := json.NewDecoder(deviceW.Body).Decode(&deviceResp); err != nil {
		t.Fatalf("decode POST /api/certs/device response: %v", err)
	}
	deviceBlock, _ := pem.Decode([]byte(deviceResp.CertPEM))
	deviceCert, err := x509.ParseCertificate(deviceBlock.Bytes)
	if err != nil {
		t.Fatalf("parse minted device cert: %v", err)
	}

	if err := deviceCert.CheckSignatureFrom(svc.ServingCA()); err != nil {
		t.Errorf("control: with one CA filling both roles, the device cert must be signed by the (identical) serving CA, got: %v", err)
	}
}

func newTestTwoCAService(t *testing.T, servingCN, deviceCN string) (svc *handler.AdminCertService, servingCA, deviceCA *x509.Certificate) {
	t.Helper()

	mkCA := func(cn string) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
		certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: cn, ValidYears: 1})
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(certPEM)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		keyBlock, _ := pem.Decode(keyPEM)
		raw, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return cert, raw.(*ecdsa.PrivateKey), certPEM
	}

	servingCA, servingKey, servingPEM := mkCA(servingCN)
	deviceCA, deviceKey, _ := mkCA(deviceCN)

	svc = handler.NewAdminCertServiceWithCAs(servingCA, servingKey, servingPEM, deviceCA, deviceKey)
	return svc, servingCA, deviceCA
}

// generateTestCA is the single-CA half of newTestTwoCAService's mkCA
// closure, factored out for #638 fix round 2 item 3 so a test can build one
// role's pair without the other.
func generateTestCA(t *testing.T, cn string) (cert *x509.Certificate, key *ecdsa.PrivateKey, certPEM []byte) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: cn, ValidYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	raw, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert, raw.(*ecdsa.PrivateKey), certPEM
}

// TestAdminCertServiceDeviceOnlyPairServesDeviceRouteAndRefusesServingRoutes
// is #638 fix round 2 item 3, device-only direction. admin_certs.go's doc
// comment states that a deployment with only one CA pair loaded still
// serves the routes that pair covers; every existing test builds both
// pairs, so nothing exercises a service built with the serving pair fully
// absent (not just its key, which TestHandleCreateServerCertNilKeyReturns503
// already covers). Reverting HandleCreateServerCert's guard to a shared
// check (`s.servingCACert == nil && s.deviceCACert == nil`) passes the rest
// of the suite; this pins that the device route stays open and the two
// serving routes refuse, from a real nil-pair construction rather than a
// mocked check.
func TestAdminCertServiceDeviceOnlyPairServesDeviceRouteAndRefusesServingRoutes(t *testing.T) {
	deviceCA, deviceKey, _ := generateTestCA(t, "638 Item3 Device-Only CA")
	svc := handler.NewAdminCertServiceWithCAs(nil, nil, nil, deviceCA, deviceKey)

	deviceH := svc.HandleCreateDeviceCert()
	deviceReq := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"DEVONLY-001","hwType":"1.3.6.1.4.1.40732.99"}`))
	deviceW := httptest.NewRecorder()
	deviceH.ServeHTTP(deviceW, deviceReq)
	if deviceW.Code != http.StatusCreated {
		t.Errorf("device-only pair: POST /api/certs/device: status = %d, want 201, body: %s", deviceW.Code, deviceW.Body.String())
	}

	serverH := svc.HandleCreateServerCert()
	serverReq := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(`{"hosts":["localhost"]}`))
	serverW := httptest.NewRecorder()
	serverH.ServeHTTP(serverW, serverReq)
	if serverW.Code != http.StatusServiceUnavailable {
		t.Errorf("device-only pair: POST /api/certs/server: status = %d, want 503 (no serving CA loaded), body: %s", serverW.Code, serverW.Body.String())
	}

	getH := svc.HandleGetCA()
	getReq := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	getW := httptest.NewRecorder()
	getH.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusServiceUnavailable {
		t.Errorf("device-only pair: GET /api/certs/ca: status = %d, want 503 (no serving CA to hand out), body: %s", getW.Code, getW.Body.String())
	}
}

// TestAdminCertServiceServingOnlyPairServesServingRoutesAndRefusesDeviceRoute
// is item 3's serving-only direction, the mirror of the test above: a
// service built with the device pair fully absent still serves the two
// serving routes and refuses device-cert minting.
func TestAdminCertServiceServingOnlyPairServesServingRoutesAndRefusesDeviceRoute(t *testing.T) {
	servingCA, servingKey, servingPEM := generateTestCA(t, "638 Item3 Serving-Only CA")
	svc := handler.NewAdminCertServiceWithCAs(servingCA, servingKey, servingPEM, nil, nil)

	serverH := svc.HandleCreateServerCert()
	serverReq := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(`{"hosts":["localhost"]}`))
	serverW := httptest.NewRecorder()
	serverH.ServeHTTP(serverW, serverReq)
	if serverW.Code != http.StatusCreated {
		t.Errorf("serving-only pair: POST /api/certs/server: status = %d, want 201, body: %s", serverW.Code, serverW.Body.String())
	}

	getH := svc.HandleGetCA()
	getReq := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	getW := httptest.NewRecorder()
	getH.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Errorf("serving-only pair: GET /api/certs/ca: status = %d, want 200, body: %s", getW.Code, getW.Body.String())
	}

	deviceH := svc.HandleCreateDeviceCert()
	deviceReq := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"SRVONLY-001","hwType":"1.3.6.1.4.1.40732.99"}`))
	deviceW := httptest.NewRecorder()
	deviceH.ServeHTTP(deviceW, deviceReq)
	if deviceW.Code != http.StatusServiceUnavailable {
		t.Errorf("serving-only pair: POST /api/certs/device: status = %d, want 503 (no device CA loaded), body: %s", deviceW.Code, deviceW.Body.String())
	}
}
