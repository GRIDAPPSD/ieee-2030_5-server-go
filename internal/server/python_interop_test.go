package server_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/server"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

// TestPythonClientInterop simulates the GridAPPS-D Python IEEE 2030.5 client's
// behavior against our server. The Python client:
// - Uses ECDSA P-256 certs (same as ours)
// - Uses GCM TLS (ssl.PROTOCOL_TLS_CLIENT, no CCM)
// - Sets check_hostname=False, verify_mode=CERT_OPTIONAL
// - Sends Accept: application/sep+xml
// - Follows: GET /dcap → GET /edev → POST /edev → GET /tm
// - Posts metering: POST /mup → POST /mup/{id}/mr
// - Uses Connection: keep-alive with Keep-Alive header
func TestPythonClientInterop(t *testing.T) {
	// Generate certs matching Python client pattern (ECDSA P-256)
	caCertPEM, caKeyPEM, _ := certs.GenerateCA(certs.CAOptions{
		CommonName: "Python Interop CA",
		ValidYears: 1,
	})
	caCert, caKey, _ := parsePEMPair(caCertPEM, caKeyPEM)

	serverCertPEM, serverKeyPEM, _ := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts: []string{"127.0.0.1", "localhost"},
	})
	deviceCertPEM, deviceKeyPEM, _ := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "PYTHON-INV-001",
	})

	// Start server
	serverTLSCfg, _ := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, caCertPEM)
	stores := newTestStores()
	cfg := &config.Config{TZOffset: -28800, TimeQuality: sep2.TimeQualityNTP}

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer listener.Close()

	tlsListener := tls.NewListener(listener, serverTLSCfg)
	router := server.NewRouter(cfg, stores, nil, "", "")
	srv := &http.Server{Handler: router}
	go srv.Serve(tlsListener)
	defer srv.Close()

	// Create client mimicking Python behavior:
	// - check_hostname = False → InsecureSkipVerify (for hostname, not cert chain)
	// - verify_mode = CERT_OPTIONAL → still sends client cert
	// - Connection: keep-alive
	cert, _ := tls.X509KeyPair(deviceCertPEM, deviceKeyPEM)
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	pythonLikeTLS := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caPool,
		InsecureSkipVerify: false, // Python sets CERT_OPTIONAL but still verifies CA
		MinVersion:         tls.VersionTLS12,
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   pythonLikeTLS,
			DisableKeepAlives: false, // persistent connections like Python
		},
	}

	baseURL := "https://" + listener.Addr().String()

	// Step 1: GET /dcap (Python client's entry point)
	t.Run("GET /dcap", func(t *testing.T) {
		req, _ := http.NewRequest("GET", baseURL+"/dcap", nil)
		req.Header.Set("Accept", "application/sep+xml")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Keep-Alive", "timeout=30, max=1000")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /dcap: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/sep+xml" {
			t.Errorf("Content-Type = %q", ct)
		}

		body, _ := io.ReadAll(resp.Body)
		xmlStr := string(body)

		// Python client expects these namespace and elements
		if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
			t.Error("missing IEEE 2030.5 namespace")
		}
		if !strings.Contains(xmlStr, "DeviceCapability") {
			t.Error("missing DeviceCapability root element")
		}

		var dcap sep2.DeviceCapability
		if err := xml.Unmarshal(body, &dcap); err != nil {
			t.Fatalf("unmarshal DeviceCapability: %v", err)
		}
		if dcap.TimeLink == nil {
			t.Error("Python client needs TimeLink")
		}
		if dcap.EndDeviceListLink == nil {
			t.Error("Python client needs EndDeviceListLink")
		}
	})

	// Step 2: GET /tm (Python client checks time)
	t.Run("GET /tm", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/tm")
		if err != nil {
			t.Fatalf("GET /tm: %v", err)
		}
		defer resp.Body.Close()

		var tm sep2.Time
		body, _ := io.ReadAll(resp.Body)
		xml.Unmarshal(body, &tm)

		if tm.CurrentTime == 0 {
			t.Error("currentTime should not be 0")
		}
	})

	// Step 3: POST /edev (Python client registers)
	t.Run("POST /edev", func(t *testing.T) {
		edevXML := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><sFDI>123456789012</sFDI></EndDevice>`
		req, _ := http.NewRequest("POST", baseURL+"/edev", strings.NewReader(edevXML))
		req.Header.Set("Content-Type", "application/sep+xml")
		req.Header.Set("Connection", "keep-alive")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /edev: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Error("missing Location header (Python client follows this)")
		}
	})

	// Step 4: GET /edev (Python client lists devices)
	t.Run("GET /edev", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/edev")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		var list sep2.EndDeviceList
		body, _ := io.ReadAll(resp.Body)
		xml.Unmarshal(body, &list)

		if list.All < 1 {
			t.Error("EndDeviceList should have at least 1 device")
		}
	})

	// Step 5: POST /mup (Python client registers for metering)
	t.Run("POST /mup", func(t *testing.T) {
		mupXML := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns">
			<mRID>python-mup-001</mRID>
			<description>Python Inverter</description>
			<serviceCategoryKind>0</serviceCategoryKind>
			<status>1</status>
		</MirrorUsagePoint>`

		req, _ := http.NewRequest("POST", baseURL+"/mup", strings.NewReader(mupXML))
		req.Header.Set("Content-Type", "application/sep+xml")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST /mup status = %d, body: %s", resp.StatusCode, body)
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Error("missing Location header for MUP")
		}

		// Step 6: POST meter reading to the MUP
		if loc != "" {
			mupID := loc[strings.LastIndex(loc, "/")+1:]
			mrXML := `<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns">
				<mRID>mmr-001</mRID>
				<description>Active Power</description>
				<ReadingType><uom>38</uom></ReadingType>
				<Reading><value>8500</value></Reading>
			</MirrorMeterReading>`

			req, _ := http.NewRequest("POST", baseURL+"/mup/"+mupID+"/mr", strings.NewReader(mrXML))
			req.Header.Set("Content-Type", "application/sep+xml")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("POST /mup/%s/mr: %v", mupID, err)
			}
			resp.Body.Close()

			if resp.StatusCode != 201 {
				t.Errorf("POST meter reading status = %d", resp.StatusCode)
			}
		}
	})
}
