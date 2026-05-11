// Package csip_test holds the IEEE-2030.5 CSIP conformance harness.
//
// Smoke-level coverage today: one test, TestCSIPHandshakeWithSunSpecDeviceCert,
// proves the cert path + verifier hook + mTLS handshake end-to-end against
// real CSIP §6.11 external materials (SunSpec V1.2 test PKI). It does NOT
// (yet) cover the 25 SunSpec V1.2 conformance requirements — those land
// once the scaffold is in place.
//
// Fixtures are provisioned out of band; see test/csip/README.md. The test
// skips cleanly when fixtures are unavailable so fresh clones never fail.
package csip_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// Fixture resolution: env first, then default to test/csip/fixtures/sunspec/.
const (
	envCert  = "CSIP_SUNSPEC_CERT"
	envKey   = "CSIP_SUNSPEC_KEY"
	envRoots = "CSIP_SUNSPEC_ROOTS"
)

// TestCSIPHandshakeWithSunSpecDeviceCert exercises the full CSIP-cipher-aware
// mTLS handshake against an in-process spec server running in CCM mode, using
// the SunSpec V1.2 test device cert as the external client.
//
// What this test asserts today:
//   - The server's verifier hook (verifyClientCertWithHardwareModuleSAN)
//     accepts the SunSpec leaf's critical HardwareModuleName SAN.
//   - The chain validates to the SunSpec Test 2030.5 Root (loaded into
//     ClientCAs via roots.pem).
//   - The mTLS handshake completes.
//   - GET /dcap returns HTTP 200 and a parseable DeviceCapability XML body.
//
// What this test DOES NOT assert (deferred):
//   - Negotiated cipher suite. The test logs it but does not require CCM-8.
//     The stdlib http.Client used here cannot offer CCM-8; tightening this
//     assertion is gated on IEEE-019 (move the client onto vendored gotls)
//     and IEEE-020 (SEP2_CSIP_STRICT=true server mode that drops the GCM
//     fallback). When both land, this test (or a sibling) asserts CCM-8.
func TestCSIPHandshakeWithSunSpecDeviceCert(t *testing.T) {
	certPath, keyPath, rootsPath, ok := resolveFixtures()
	if !ok {
		t.Skip("CSIP fixtures not provisioned; see test/csip/README.md")
	}

	// 1. Generate a local CA + server cert. The server presents this to the
	// client; the client trusts our CA via RootCAs. Separate from CSIP
	// ClientCAs, which trusts the SunSpec roots.
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "CSIP Smoke CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "CSIP Smoke Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}

	// 2. NewCCMServerConfig reads from disk; write generated material to
	// a temp dir alongside the SunSpec roots so the server trusts SunSpec
	// device certs at handshake.
	dir := t.TempDir()
	serverCertFile := filepath.Join(dir, "server.crt")
	serverKeyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(serverCertFile, serverCertPEM, 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(serverKeyFile, serverKeyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}

	ccmCfg, err := sepTLS.NewCCMServerConfig(serverCertFile, serverKeyFile, rootsPath)
	if err != nil {
		t.Fatalf("CCM server config: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	tlsListener := gotls.NewListener(listener, ccmCfg)

	cfg := &config.Config{
		TZOffset:    -28800,
		DSTOffset:   3600,
		DSTStart:    1583661600,
		DSTEnd:      1604214000,
		TimeQuality: sep2.TimeQualityNTP,
	}
	router := server.NewRouter(cfg, newSmokeStores(), nil, "", "")

	protocolSrv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
	}
	sepTLS.SetupCCMServer(protocolSrv)
	protocolSrv.Handler = sepTLS.CCMIdentityMiddleware(router)

	serveErr := make(chan error, 1)
	go func() { serveErr <- protocolSrv.Serve(tlsListener) }()
	t.Cleanup(func() { _ = protocolSrv.Close() })

	// 3. Build a stdlib TLS client config presenting the SunSpec device
	// cert + key, trusting our locally-generated CA for the server cert.
	clientCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read SunSpec cert: %v", err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read SunSpec key: %v", err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("parse SunSpec cert+key: %v", err)
	}
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("append local CA to root pool")
	}

	clientTLSCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      rootPool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
	}

	httpClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}

	baseURL := "https://" + listener.Addr().String()

	// 4. Probe the handshake first via a raw dial so we can log cipher
	// negotiation independently from the HTTP layer.
	rawConn, err := tls.Dial("tcp", listener.Addr().String(), clientTLSCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	state := rawConn.ConnectionState()
	if !state.HandshakeComplete {
		t.Fatal("HandshakeComplete = false")
	}
	t.Logf("TLS handshake OK: version=0x%04x cipher=0x%04x (%s) peerCerts=%d",
		state.Version, state.CipherSuite, tls.CipherSuiteName(state.CipherSuite), len(state.PeerCertificates))
	_ = rawConn.Close()

	// 5. GET /dcap over a fresh connection, expect 200 and parseable XML.
	resp, err := httpClient.Get(baseURL + "/dcap")
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var dcap sep2.DeviceCapability
	if err := xml.Unmarshal(body, &dcap); err != nil {
		t.Fatalf("unmarshal DeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("DeviceCapability.Href = %q, want /dcap", dcap.Href)
	}

	// Shut down cleanly before draining serveErr so we get the expected
	// http.ErrServerClosed instead of a leaked goroutine.
	_ = protocolSrv.Close()
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("server exited: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s")
	}
}

// resolveFixtures returns (cert, key, roots, ok). ok is false when none of
// the three paths resolve to an existing file. Env vars override the default
// test/csip/fixtures/sunspec/ paths, evaluated per-variable.
func resolveFixtures() (certPath, keyPath, rootsPath string, ok bool) {
	cert := fixturePath(envCert, "cert.pem")
	key := fixturePath(envKey, "key.pem")
	roots := fixturePath(envRoots, "roots.pem")
	for _, p := range []string{cert, key, roots} {
		if _, err := os.Stat(p); err != nil {
			return "", "", "", false
		}
	}
	return cert, key, roots, true
}

func fixturePath(env, leaf string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return filepath.Join("fixtures", "sunspec", leaf)
}

// newSmokeStores returns a fully-populated in-memory store set sufficient
// for the /dcap smoke. Kept inline (not extracted) until a second CSIP test
// needs it — premature abstraction is its own smell.
func newSmokeStores() *server.Stores {
	return &server.Stores{
		EndDevices:               memory.NewEndDeviceStore(),
		MirrorUsagePoints:        memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings:      memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:                     memory.NewScopedStore[sep2.DER](),
		DERCapabilities:          memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:              memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:              memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:        memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:              memory.NewScopedStore[sep2.DERProgram](),
		DERControls:              memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		FSAs:                     memory.NewScopedStore[sep2.FunctionSetAssignments](),
		Subscriptions:            memory.NewSubscriptionStore(),
		UsagePoints:              memory.NewStore[sep2.UsagePoint](),
		MeterReadings:            memory.NewScopedStore[sep2.MeterReading](),
		Readings:                 memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:             memory.NewStore[sep2.ReadingType](),
		Configurations:           memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:           memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:                memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:            memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:        memory.NewStore[sep2.MessagingProgram](),
		TextMessages:             memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),
	}
}

