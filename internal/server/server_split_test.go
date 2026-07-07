package server_test

// IEEE-094 admin-listener split tests.
//
// The SEP2 protocol listener is RequireAnyClientCert + manual verify per
// CSIP V1.2. A non-cert client (typical browser) must fail to handshake on
// that port. The admin listener, when configured, must accept non-cert
// clients via Bearer/cookie so the IEEE-095 dashboard works. Both listeners
// must come up under a single server.Run() and shut down on the same
// signal.
//
// Two integration tests cover the matrix:
//
//	TestAdminListenerSplit_PlainHTTP   — SEP2_ADMIN_TLS=false, Caddy mode
//	TestAdminListenerSplit_HTTPS       — SEP2_ADMIN_TLS=true, self-signed
//
// Plus a back-compat test:
//
//	TestAdminListenerBackCompatAdminAddr — empty AdminListen + AdminAddr set
//	                                       (pre-IEEE-094 env layout)

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestAdminListenerSplit_PlainHTTP boots a server with the admin listener
// in Caddy mode (plain HTTP). Asserts:
//   - SEP2 listener rejects a TLS-cert-less client (handshake failure).
//   - Admin listener accepts a plain-HTTP request and 401s without a Bearer
//     (i.e., AdminAuthMiddleware is wired through).
//   - Admin Bearer request to an IEEE-095 endpoint (POST /api/certs/info)
//     returns 200 — smoke check that the dashboard endpoints work on the
//     new listener.
func TestAdminListenerSplit_PlainHTTP(t *testing.T) {
	t.Parallel()
	env := bootSplitListener(t, splitListenerOpts{adminTLS: false, useAdminAddr: false})
	defer env.cancel()

	// 1. SEP2 listener: a cert-less client must fail to handshake.
	cleanClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	if _, err := cleanClient.Get("https://" + env.sep2Addr + "/dcap"); err == nil {
		t.Fatal("SEP2 listener accepted a cert-less client; want handshake failure")
	}

	// 2. Admin listener (plain HTTP): no Bearer → 401. IEEE-132: this test
	//    binds 127.0.0.1, so we set X-Forwarded-For to simulate the
	//    Caddy-fronted production case and force the loopback bypass to
	//    decline so AdminAuthMiddleware actually runs.
	adminClient := &http.Client{Timeout: 2 * time.Second}
	noAuthReq, _ := http.NewRequest(http.MethodGet, "http://"+env.adminAddr+"/api/certs/ca", nil)
	noAuthReq.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := adminClient.Do(noAuthReq)
	if err != nil {
		t.Fatalf("admin GET (no auth): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-Bearer status = %d, want 401", resp.StatusCode)
	}

	// 3. Admin listener: Bearer → 200 on an IEEE-095 endpoint.
	req, _ := http.NewRequest(http.MethodPost, "http://"+env.adminAddr+"/api/certs/info", nil)
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err = adminClient.Do(req)
	if err != nil {
		t.Fatalf("admin POST /api/certs/info: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// /api/certs/info with no body is a 400 (no cert provided) — but the
	// fact that we got past AdminAuthMiddleware proves the listener and
	// auth wiring. Status must NOT be 401 / 404 / 405.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("Bearer was rejected: status=%d body=%s", resp.StatusCode, body)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		t.Errorf("IEEE-095 endpoint not mounted on admin listener: status=%d body=%s", resp.StatusCode, body)
	}
}

// TestAdminListenerSplit_HTTPS boots with admin TLS on (self-signed). Same
// assertions as the plain-HTTP test, with https:// on the admin port and
// InsecureSkipVerify on the client (test trust model).
func TestAdminListenerSplit_HTTPS(t *testing.T) {
	t.Parallel()
	env := bootSplitListener(t, splitListenerOpts{adminTLS: true, useAdminAddr: false})
	defer env.cancel()

	// 1. SEP2 listener: cert-less client rejected.
	cleanClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	if _, err := cleanClient.Get("https://" + env.sep2Addr + "/dcap"); err == nil {
		t.Fatal("SEP2 listener accepted a cert-less client; want handshake failure")
	}

	// 2. Admin listener: TLS handshake succeeds without a client cert
	//    (VerifyClientCertIfGiven), and no Bearer → 401. IEEE-132: XFF
	//    forces the loopback bypass to decline so the auth chain runs.
	adminClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	noAuthReq, _ := http.NewRequest(http.MethodGet, "https://"+env.adminAddr+"/api/certs/ca", nil)
	noAuthReq.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := adminClient.Do(noAuthReq)
	if err != nil {
		t.Fatalf("admin GET on HTTPS listener (no auth): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-Bearer status on HTTPS admin = %d, want 401", resp.StatusCode)
	}

	// 3. Admin listener: Bearer → IEEE-095 endpoint reachable.
	req, _ := http.NewRequest(http.MethodPost, "https://"+env.adminAddr+"/api/certs/info", nil)
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err = adminClient.Do(req)
	if err != nil {
		t.Fatalf("admin POST /api/certs/info: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		t.Errorf("IEEE-095 endpoint not reachable: status=%d", resp.StatusCode)
	}
}

// TestAdminListenerBackCompatAdminAddr asserts that empty AdminListen +
// non-empty AdminAddr (pre-IEEE-094 env layout: SEP2_ADMIN_ADDR=...)
// still works. The pre-IEEE-094 deployment used HTTPS self-signed on
// AdminAddr; the back-compat path matches that posture when AdminTLS is
// also set (Craig's IEEE-094 brief defaults AdminTLS=false, so the
// migration story for pre-IEEE-094 deployments is "flip AdminTLS=true to
// keep HTTPS, or front with Caddy and keep AdminTLS=false").
func TestAdminListenerBackCompatAdminAddr(t *testing.T) {
	t.Parallel()
	env := bootSplitListener(t, splitListenerOpts{adminTLS: true, useAdminAddr: true})
	defer env.cancel()

	adminClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodPost, "https://"+env.adminAddr+"/api/certs/info", nil)
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := adminClient.Do(req)
	if err != nil {
		t.Fatalf("admin POST via back-compat AdminAddr: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		t.Errorf("back-compat AdminAddr path broken: status=%d", resp.StatusCode)
	}
}

// TestEffectiveAdminListen covers the small config helper standalone so the
// fallback contract is locked down independent of server.Run.
func TestEffectiveAdminListen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, listen, addr, want string
	}{
		{"both empty", "", "", ""},
		{"listen only", ":9443", "", ":9443"},
		{"addr only", "", ":8443", ":8443"},
		{"listen wins", ":9443", ":8443", ":9443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &config.Config{AdminListen: tc.listen, AdminAddr: tc.addr}
			if got := c.EffectiveAdminListen(); got != tc.want {
				t.Errorf("EffectiveAdminListen() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAdminListenerOperatorCert exercises the matrix corner where the
// operator supplies an explicit admin cert/key pair (AdminCert +
// AdminKeyFile both set). Asserts the listener serves HTTPS using the
// operator-supplied cert, not the self-signed fallback. Verified by chain
// validation against the operator CA.
func TestAdminListenerOperatorCert(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)

	// Generate a second cert/key pair to use as the admin cert. We reuse
	// the same CA as the SEP2 server so the test client can chain-validate
	// without skipping verification.
	caCert, _, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}
	adminCertPEM, adminKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "IEEE-094 Admin Cert",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(admin): %v", err)
	}
	adminCertFile := filepath.Join(t.TempDir(), "admin.pem")
	adminKeyFile := filepath.Join(filepath.Dir(adminCertFile), "admin-key.pem")
	if err := os.WriteFile(adminCertFile, adminCertPEM, 0o600); err != nil {
		t.Fatalf("write admin cert: %v", err)
	}
	if err := os.WriteFile(adminKeyFile, adminKeyPEM, 0o600); err != nil {
		t.Fatalf("write admin key: %v", err)
	}

	cfg := &config.Config{
		Addr:         c.sep2Probe,
		CertFile:     c.certFile,
		KeyFile:      c.keyFile,
		CAFile:       c.caFile,
		AdminListen:  c.adminProbe,
		AdminKey:     adminTestKey,
		AdminTLS:     true,
		AdminCert:    adminCertFile,
		AdminKeyFile: adminKeyFile,
		TZOffset:     -28800,
		TimeQuality:  sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	// Build a chain-validating client. CN/SAN must match "127.0.0.1".
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.caCertPEM) {
		t.Fatal("AppendCertsFromPEM: failed")
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: "127.0.0.1",
			},
		},
		Timeout: 500 * time.Millisecond,
	}

	// Poll until the operator-cert HTTPS listener accepts a chain-validated
	// connection.
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodPost, "https://"+c.adminProbe+"/api/certs/info", nil)
		req.Header.Set("Authorization", "Bearer "+adminTestKey)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("Bearer rejected on operator-cert listener: status=%d", resp.StatusCode)
			}
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("operator-cert admin listener never validated: %v", lastErr)
}

// TestAdminTLSConfigBrokenPair asserts the matrix's invalid corner — only
// one of AdminCert/AdminKeyFile set — fails fast instead of silently
// generating a self-signed cert. The helper is unexported; we exercise it
// via server.Run() returning an error.
func TestAdminTLSConfigBrokenPair(t *testing.T) {
	t.Parallel()
	env := newSplitListenerCerts(t)

	cfg := &config.Config{
		Addr:        env.sep2Probe,
		CertFile:    env.certFile,
		KeyFile:     env.keyFile,
		CAFile:      env.caFile,
		AdminListen: env.adminProbe,
		AdminKey:    adminTestKey,
		AdminTLS:    true,
		AdminCert:   "/nonexistent/cert.pem",
		// AdminKeyFile intentionally empty — broken pair.
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, cfg, env.svc) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("server.Run accepted broken AdminCert/AdminKeyFile pair; want error")
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server.Run did not fail-fast on broken admin cert/key pair within 2s")
	}
}

// adminTestKey is the Bearer used by the split-listener tests. Long enough
// to survive constant-time compare.
const adminTestKey = "ieee-094-split-listener-bearer-token"

type splitListenerOpts struct {
	adminTLS     bool
	useAdminAddr bool // wire via deprecated AdminAddr instead of AdminListen
}

type splitListenerEnv struct {
	sep2Addr  string
	adminAddr string
	cancel    context.CancelFunc
	runErrCh  chan error
}

// bootSplitListener spins up a real server.Run() with both listeners bound
// to ephemeral ports. Returns once both listeners are responsive or
// t.Fatals on timeout.
func bootSplitListener(t *testing.T, opts splitListenerOpts) *splitListenerEnv {
	t.Helper()

	c := newSplitListenerCerts(t)

	// Build a device cert for the SEP2 readiness probe — SEP2 listener is
	// RequireAnyClientCert and won't complete a handshake without one.
	caCert, _, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "IEEE-094-PROBE",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, c.caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminKey:    adminTestKey,
		AdminTLS:    opts.adminTLS,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
	if opts.useAdminAddr {
		cfg.AdminAddr = c.adminProbe
	} else {
		cfg.AdminListen = c.adminProbe
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

	// Wait for SEP2 listener (full TLS handshake with a device cert).
	if !waitForServerReady(c.sep2Probe, 3*time.Second, clientTLSCfg) {
		cancel()
		<-runErrCh
		t.Fatalf("SEP2 listener never became ready on %s", c.sep2Probe)
	}

	// Wait for admin listener (HTTP- or HTTPS-level probe depending on
	// AdminTLS). A plain TCP connect would race against the goroutine that
	// calls Serve, so we hit /api/certs/ca with a tight timeout and accept
	// any HTTP status as proof-of-life.
	scheme := "http"
	transport := &http.Transport{}
	if opts.adminTLS {
		scheme = "https"
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	probeClient := &http.Client{Transport: transport, Timeout: 500 * time.Millisecond}
	probeDeadline := time.Now().Add(3 * time.Second)
	adminReady := false
	for time.Now().Before(probeDeadline) {
		resp, err := probeClient.Get(scheme + "://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			adminReady = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !adminReady {
		cancel()
		<-runErrCh
		t.Fatalf("admin listener never became ready on %s", c.adminProbe)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	return &splitListenerEnv{
		sep2Addr:  c.sep2Probe,
		adminAddr: c.adminProbe,
		cancel:    cancel,
		runErrCh:  runErrCh,
	}
}

// splitListenerCerts bundles the on-disk + in-memory bits needed by every
// split-listener subtest. Keeping it separate from splitListenerEnv lets
// TestAdminTLSConfigBrokenPair reuse the cert/file scaffolding without
// booting a full server.
type splitListenerCerts struct {
	caFile, certFile, keyFile, caKeyFile string
	caCertPEM, caKeyPEM                  []byte
	sep2Probe, adminProbe                string
	svc                                  *handler.AdminCertService
}

func newSplitListenerCerts(t *testing.T) *splitListenerCerts {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "IEEE-094 Split Listener CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(ca): %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "IEEE-094 Split Listener Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	caFile := filepath.Join(dir, "ca.pem")
	caKeyFile := filepath.Join(dir, "ca-key.pem")
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	for _, p := range []struct {
		path string
		data []byte
	}{
		{caFile, caCertPEM},
		{caKeyFile, caKeyPEM},
		{certFile, serverCertPEM},
		{keyFile, serverKeyPEM},
	} {
		if err := os.WriteFile(p.path, p.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", p.path, err)
		}
	}

	sep2Probe := mustProbePort(t)
	adminProbe := mustProbePort(t)

	svc := handler.NewAdminCertService(caCert, caKey, caCertPEM)

	return &splitListenerCerts{
		caFile:     caFile,
		certFile:   certFile,
		keyFile:    keyFile,
		caKeyFile:  caKeyFile,
		caCertPEM:  caCertPEM,
		caKeyPEM:   caKeyPEM,
		sep2Probe:  sep2Probe,
		adminProbe: adminProbe,
		svc:        svc,
	}
}

func mustProbePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}
