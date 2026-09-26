package server_test

// #622: the protocol listener's ClientCAs trust bundle must come from the
// DEVICE CA (Config.DeviceCAFile / EffectiveDeviceCA), not the serving CA,
// once the two are split. This is the highest-severity site to get wrong:
// a server cert signed by the wrong CA fails nowhere on this host, but a
// client-trust pool built from the wrong CA is loud immediately, at the
// very next handshake. This test drives that handshake for real, both ways.

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// assertRefusedForCertFailure is #638 fix round 2 item 5: the original
// assertion here only checked err != nil, so a hang (this test's clients
// already carry a short Timeout) or an unrelated transport reset would have
// satisfied it as well as a real certificate refusal. Requiring the "tls:"
// remote-alert prefix rules out anything that is not a TLS-layer refusal;
// requiring wantAlert pins the exact alert the listener sends for a client
// cert outside its ClientCAs pool. The CCM-8 listener, wired through core's
// forked TLS stack, sends "remote error: tls: bad certificate" for this
// refusal.
func assertRefusedForCertFailure(t *testing.T, resp *http.Response, err error, wantAlert string) {
	t.Helper()
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("serving-CA-signed client was accepted by the protocol listener; ClientCAs pool is not routed to the device CA")
	}
	if !strings.Contains(err.Error(), "tls:") || !strings.Contains(err.Error(), wantAlert) {
		t.Errorf("err = %v, want a TLS handshake refusal naming %q (a hang or a reset would satisfy err != nil alone, but not this)", err, wantAlert)
	}
}

// TestProtocolListenerTrustsOnlyTheDeviceCAUnderCCM is #638 fix round 2 item
// 4: with ServingCAFile and DeviceCAFile pointed at two different CAs, a
// device cert signed by the device CA completes the mTLS handshake and
// reaches /dcap, and one signed by the serving CA is refused at the
// handshake. Both directions are asserted so this cannot pass by the
// listener trusting everything. The listener wraps in gotls.NewListener
// (pkg/sep2server/server.go), so the client dials through gotls too.
func TestProtocolListenerTrustsOnlyTheDeviceCAUnderCCM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	servingCACertPEM, servingCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "622 CCM Serving CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(serving): %v", err)
	}
	deviceCACertPEM, deviceCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "622 CCM Device CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(device): %v", err)
	}

	servingCACert, err := certs.ParseCertificatePEM(servingCACertPEM)
	if err != nil {
		t.Fatalf("parse serving CA: %v", err)
	}
	servingCAKey, err := certs.ParseKeyPEM(servingCAKeyPEM)
	if err != nil {
		t.Fatalf("parse serving CA key: %v", err)
	}
	deviceCACert, err := certs.ParseCertificatePEM(deviceCACertPEM)
	if err != nil {
		t.Fatalf("parse device CA: %v", err)
	}
	deviceCAKey, err := certs.ParseKeyPEM(deviceCAKeyPEM)
	if err != nil {
		t.Fatalf("parse device CA key: %v", err)
	}

	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(servingCACert, servingCAKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "622 CCM protocol listener",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	goodDeviceCertPEM, goodDeviceKeyPEM, err := certs.GenerateDeviceCert(deviceCACert, deviceCAKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "622-ccm-good",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert(device CA): %v", err)
	}

	badDeviceCertPEM, badDeviceKeyPEM, err := certs.GenerateDeviceCert(servingCACert, servingCAKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "622-ccm-bad",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert(serving CA): %v", err)
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	servingCAFile := write("serving-ca.pem", servingCACertPEM)
	deviceCAFile := write("device-ca.pem", deviceCACertPEM)
	certFile := write("server.pem", serverCertPEM)
	keyFile := write("server-key.pem", serverKeyPEM)

	sep2Addr := mustProbePort(t)
	cfg := &config.Config{
		Addr:          sep2Addr,
		CertFile:      certFile,
		KeyFile:       keyFile,
		ServingCAFile: servingCAFile,
		DeviceCAFile:  deviceCAFile,
		TZOffset:      -28800,
		TimeQuality:   sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(servingCACertPEM) {
		t.Fatal("failed to parse serving CA into root pool")
	}

	ccmClientConfig := func(certPEM, keyPEM []byte) *gotls.Config {
		cert, err := gotls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("gotls.X509KeyPair: %v", err)
		}
		return &gotls.Config{
			RootCAs:          rootPool,
			Certificates:     []gotls.Certificate{cert},
			MinVersion:       gotls.VersionTLS12,
			MaxVersion:       gotls.VersionTLS12,
			CipherSuites:     []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
			CurvePreferences: []gotls.CurveID{gotls.CurveP256},
		}
	}
	ccmHTTPClient := func(cfg *gotls.Config) *http.Client {
		return &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return (&gotls.Dialer{Config: cfg}).DialContext(ctx, network, addr)
				},
			},
		}
	}

	goodClientCfg := ccmClientConfig(goodDeviceCertPEM, goodDeviceKeyPEM)
	if !waitForServerReady(sep2Addr, 3*time.Second, goodClientCfg) {
		t.Fatal("CCM protocol listener never became ready for the device-CA-signed client")
	}

	goodClient := ccmHTTPClient(goodClientCfg)
	resp, err := goodClient.Get("https://" + sep2Addr + "/dcap")
	if err != nil {
		t.Fatalf("device-CA-signed CCM client: GET /dcap: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("device-CA-signed CCM client: status = %d, want 200", resp.StatusCode)
	}

	badClient := ccmHTTPClient(ccmClientConfig(badDeviceCertPEM, badDeviceKeyPEM))
	badClient.Timeout = 1 * time.Second
	badResp, badErr := badClient.Get("https://" + sep2Addr + "/dcap")
	assertRefusedForCertFailure(t, badResp, badErr, "bad certificate")

	// The client above is cooperative: gotls's default certificate
	// selection filters candidates against the server's advertised
	// AcceptableCAs and withholds one that does not match, so its refusal
	// could be the client declining to send rather than the listener
	// verifying and rejecting. A hostile client that forces the send is the
	// only way to prove the listener itself does the rejecting (#709 fix
	// round 1, closing the gap the security lane's `return nil` mutant
	// found in the callback this drives).
	hostileCert, err := gotls.X509KeyPair(badDeviceCertPEM, badDeviceKeyPEM)
	if err != nil {
		t.Fatalf("gotls.X509KeyPair(hostile): %v", err)
	}
	hostileClientCfg := &gotls.Config{
		RootCAs:          rootPool,
		MinVersion:       gotls.VersionTLS12,
		MaxVersion:       gotls.VersionTLS12,
		CipherSuites:     []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
		GetClientCertificate: func(*gotls.CertificateRequestInfo) (*gotls.Certificate, error) {
			return &hostileCert, nil
		},
	}
	hostileClient := ccmHTTPClient(hostileClientCfg)
	hostileClient.Timeout = 1 * time.Second
	hostileResp, hostileErr := hostileClient.Get("https://" + sep2Addr + "/dcap")
	assertRefusedForCertFailure(t, hostileResp, hostileErr, "bad certificate")
}
