package server_test

// #624 gave the admin listener a client-certificate trust anchor (see
// adminClientCAPool's doc comment, internal/server/server.go, for the #418
// mechanism this closes); #657 covers the anchor's failure and refusal
// shapes. These tests drive real TLS handshakes and real server.Run boots
// against a real admin listener, not the config-resolution functions
// directly.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// genPlainClientCert mints a client-auth-capable leaf signed by caCert/caKey
// with NO extra extensions: no policy OID of any kind, no critical
// extensions. It exists because every certs.Generate* helper that could
// otherwise stand in for "trusted CA, wrong shape" (GenerateDeviceCert,
// GenerateServerCert) marks its own policy or SAN extension critical for
// spec reasons unrelated to this test, and Go's x509.Verify (the same
// Verify the admin listener's TLS handshake runs) refuses any certificate
// with a critical extension it does not recognize - a different, unrelated
// refusal that would masquerade as the one #624 criterion 2 asks for.
func genPlainClientCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genPlainClientCert: generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("genPlainClientCert: CreateCertificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("genPlainClientCert: MarshalECPrivateKey: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// bootAdminClientCAListener boots a real server.Run with the admin listener
// on HTTPS, mutating cfg via configure before boot so each test can set
// AdminClientCA (or leave it at its zero value for the default anchor).
// Returns the admin address once the listener answers.
func bootAdminClientCAListener(t *testing.T, c *splitListenerCerts, configure func(*config.Config)) string {
	t.Helper()

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminTLS:    true,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
	if configure != nil {
		configure(cfg)
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

	probeClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   500 * time.Millisecond,
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := probeClient.Get("https://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			return c.adminProbe
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-runErrCh
	t.Fatal("admin listener never became ready")
	return ""
}

// adminClientFor dials with certPEM/keyPEM as the presented client
// certificate and nothing else: no client-side trust override of any kind
// verifies the ADMIN listener's own server certificate either, matching
// TestAdminListenerSplit_HTTPS's InsecureSkipVerify convention, since that
// axis (server-cert trust) is not what #624 changes.
//
// GetClientCertificate, not the static Certificates field: Go's client
// (crypto/tls, getClientCertificate) silently omits a configured cert that
// does not chain to one of the server's CertificateRequest
// certificate_authorities (present whenever ClientCAs is non-nil), rather
// than sending it and letting the server's Verify reject it. A real
// operator's browser or curl sends whatever cert it is told to; forcing
// presentation here is what makes the "wrong CA" tests exercise the
// server's own Verify (crypto/tls handshake_server.go
// processCertsFromClient) instead of a polite client-side no-op.
func adminClientFor(t *testing.T, certPEM, keyPEM []byte) *http.Client {
	t.Helper()
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	if certPEM != nil {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair: %v", err)
		}
		tlsCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   1 * time.Second,
	}
}

// TestAdminListenerDefaultAnchorTrustsOperatorCert is #624 done-conditions 1
// and 2, driven at a real listener. With no AdminClientCA set, the default
// anchor is the serving CA (here, c.caFile, the pre-#622-split shared CA).
// An operator cert signed by that CA is admitted with no client-side trust
// override; one signed by an unrelated CA is refused at the handshake; one
// signed by the trusted CA but lacking the admin policy OID still gets the
// existing 401 (the OID gate is unchanged by this fix).
func TestAdminListenerDefaultAnchorTrustsOperatorCert(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)

	caCert, caKey, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA(serving): %v", err)
	}
	otherCACertPEM, otherCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "624 other CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(other): %v", err)
	}
	otherCACert, err := certs.ParseCertificatePEM(otherCACertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(other): %v", err)
	}
	otherCAKey, err := certs.ParseKeyPEM(otherCAKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(other): %v", err)
	}

	goodOperatorCertPEM, goodOperatorKeyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "624 operator, serving CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert(serving CA): %v", err)
	}
	otherOperatorCertPEM, otherOperatorKeyPEM, err := certs.GenerateAdminCert(otherCACert, otherCAKey, certs.AdminCertOptions{
		CommonName: "624 operator, other CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert(other CA): %v", err)
	}
	// Trusted CA, no admin policy OID at all: signed by the same anchor as
	// the good operator cert, otherwise plain.
	noOIDCertPEM, noOIDKeyPEM := genPlainClientCert(t, caCert, caKey, "624 no admin OID")

	adminAddr := bootAdminClientCAListener(t, c, nil)

	// 1. Serving-CA-signed operator cert: admitted, no client-side override.
	resp, err := adminClientFor(t, goodOperatorCertPEM, goodOperatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if err != nil {
		t.Fatalf("operator cert signed by the serving CA was refused at the handshake: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("serving-CA operator cert: status = %d, want 200 (admitted via mTLS)", resp.StatusCode)
	}

	// 2. Cert from an unrelated CA: refused at the handshake, not just at
	// the application layer, so this cannot pass by trusting everything.
	badResp, badErr := adminClientFor(t, otherOperatorCertPEM, otherOperatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if badErr == nil {
		_ = badResp.Body.Close()
		t.Fatal("operator cert signed by an unrelated CA was accepted by the admin listener; ClientCAs pool is not anchored to the serving CA")
	}
	if !strings.Contains(badErr.Error(), "tls:") {
		t.Errorf("other-CA cert: err = %v, want a TLS-layer refusal (a hang or reset would satisfy err != nil alone, but not this)", badErr)
	}

	// 3. Trusted CA, no admin policy OID: handshake succeeds (same anchor),
	// application layer still refuses with the existing 401 - the OID gate
	// is untouched by this change.
	noOIDResp, err := adminClientFor(t, noOIDCertPEM, noOIDKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if err != nil {
		t.Fatalf("cert without the admin OID: handshake itself failed, want a TLS-layer success and an application-layer 401: %v", err)
	}
	defer func() { _ = noOIDResp.Body.Close() }()
	if noOIDResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("cert without the admin OID: status = %d, want 401 (existing OID gate)", noOIDResp.StatusCode)
	}
	body := make([]byte, len(sensitiveRefusalBody)+1)
	n, _ := noOIDResp.Body.Read(body)
	if got := string(body[:n]); got != sensitiveRefusalBody {
		t.Errorf("cert without the admin OID: body = %q, want %q", got, sensitiveRefusalBody)
	}
}

// TestAdminListenerSystemRootsSentinel is #624 done-condition 3's explicit
// value: AdminClientCA="system" keeps host-root verification instead of the
// serving-CA default. A cert signed by our own test CA is refused, proving
// the sentinel actually changed the anchor away from that CA (the same cert
// is ADMITTED by TestAdminListenerDefaultAnchorTrustsOperatorCert's default
// case, so this is a real behavioral difference, not a tautology).
func TestAdminListenerSystemRootsSentinel(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)

	caCert, caKey, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA(serving): %v", err)
	}
	operatorCertPEM, operatorKeyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "624 operator, system-roots test",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert: %v", err)
	}

	adminAddr := bootAdminClientCAListener(t, c, func(cfg *config.Config) {
		cfg.AdminClientCA = config.AdminClientCASystemRoots
	})

	resp, err := adminClientFor(t, operatorCertPEM, operatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("operator cert signed by a private test CA was accepted with AdminClientCA=system; want host-root verification to refuse it")
	}
	if !strings.Contains(err.Error(), "tls:") {
		t.Errorf("err = %v, want a TLS-layer refusal", err)
	}
}

// TestAdminListenerExplicitClientCAPath is #624 done-condition 3's file
// form: AdminClientCA set to a path REPLACES the serving-CA default rather
// than adding to it. A cert from the named CA is admitted; a cert from the
// serving CA (admitted by default, per the sibling test above) is now
// refused, proving the explicit setting is not merely additive.
func TestAdminListenerExplicitClientCAPath(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)

	servingCACert, servingCAKey, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA(serving): %v", err)
	}
	namedCACertPEM, namedCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "624 named client CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(named): %v", err)
	}
	namedCACert, err := certs.ParseCertificatePEM(namedCACertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(named): %v", err)
	}
	namedCAKey, err := certs.ParseKeyPEM(namedCAKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(named): %v", err)
	}
	namedCAFile := filepath.Join(t.TempDir(), "named-client-ca.pem")
	if err := os.WriteFile(namedCAFile, namedCACertPEM, 0o600); err != nil {
		t.Fatalf("write named CA: %v", err)
	}

	namedOperatorCertPEM, namedOperatorKeyPEM, err := certs.GenerateAdminCert(namedCACert, namedCAKey, certs.AdminCertOptions{
		CommonName: "624 operator, named CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert(named): %v", err)
	}
	servingOperatorCertPEM, servingOperatorKeyPEM, err := certs.GenerateAdminCert(servingCACert, servingCAKey, certs.AdminCertOptions{
		CommonName: "624 operator, serving CA (should now be refused)",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert(serving): %v", err)
	}

	adminAddr := bootAdminClientCAListener(t, c, func(cfg *config.Config) {
		cfg.AdminClientCA = namedCAFile
	})

	resp, err := adminClientFor(t, namedOperatorCertPEM, namedOperatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if err != nil {
		t.Fatalf("operator cert signed by the named AdminClientCA was refused at the handshake: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("named-CA operator cert: status = %d, want 200", resp.StatusCode)
	}

	badResp, badErr := adminClientFor(t, servingOperatorCertPEM, servingOperatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if badErr == nil {
		_ = badResp.Body.Close()
		t.Fatal("operator cert signed by the serving CA was accepted although AdminClientCA names a different CA; the explicit setting did not replace the default")
	}
	if !strings.Contains(badErr.Error(), "tls:") {
		t.Errorf("serving-CA cert: err = %v, want a TLS-layer refusal", badErr)
	}
}

// TestAdminMTLSAdmissionIsLogged is #624 done-condition 1's logging half:
// a successful mTLS admission through the new anchor logs admission_path
// "mtls", the same structured line AdminAuthMiddleware's Path A has always
// emitted (internal/auth/admin.go LogSuccessfulAdminCredential) - #624 does
// not add new logging, it makes the path reachable, and this proves it is
// reached. slog.SetDefault is process-global, so this test does not run in
// parallel with the rest of this file (same convention as
// admin_sensitive_routes_test.go).
func TestAdminMTLSAdmissionIsLogged(t *testing.T) {
	c := newSplitListenerCerts(t)

	caCert, caKey, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	operatorCertPEM, operatorKeyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "624 operator, logging test",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert: %v", err)
	}

	adminAddr := bootAdminClientCAListener(t, c, nil)

	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resp, err := adminClientFor(t, operatorCertPEM, operatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if !strings.Contains(buf.String(), `"event":"admin_auth_success"`) || !strings.Contains(buf.String(), `"admission_path":"mtls"`) {
		t.Errorf("mTLS admission not logged: captured = %s", buf.String())
	}
}

// bootAdminListener runs a real server.Run to completion under cfg/svc and
// waits for the admin listener to answer, the same readiness probe
// bootAdminClientCAListener uses. Unlike that helper, cfg is taken as
// given rather than built from a shared splitListenerCerts bundle, so a
// caller can put an on-disk CA anywhere in the role split (#657's
// device-CA-only and non-CA-anchor shapes both need that).
func bootAdminListener(t *testing.T, cfg *config.Config, svc *handler.AdminCertService) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	probeClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   500 * time.Millisecond,
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case runErr := <-runErrCh:
			t.Fatalf("server.Run returned before the admin listener answered: %v", runErr)
		default:
		}
		resp, err := probeClient.Get("https://" + cfg.AdminListen + "/login")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("admin listener never became ready")
}

// TestAdminListenerDeviceCAOnlyBoots is #657 design section 3 test 1. A
// deployment that only ever set SEP2_DEVICE_CA (+key) and SEP2_ADMIN_TLS,
// with no serving CA on disk, is live: docs/operator-guide.md documents
// each CA role as independently settable, and CanMint() starts the admin
// listener on the device key alone. Red before the fix: server.Run
// returned "admin client CA: load ... no such file or directory" and
// started neither listener (security and error-handling lanes' HIGH-1;
// reproduced against the built binary as this task's RED evidence).
//
// Not t.Parallel(): it redirects the shared stdlib log.Writer() output to
// capture the boot log, the same process-global constraint that keeps
// TestAdminMTLSAdmissionIsLogged out of this file's parallel group.
func TestAdminListenerDeviceCAOnlyBoots(t *testing.T) {
	dir := t.TempDir()

	deviceCACertPEM, deviceCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 device-only CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(device): %v", err)
	}
	deviceCACert, err := certs.ParseCertificatePEM(deviceCACertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}
	deviceCAKey, err := certs.ParseKeyPEM(deviceCAKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(device): %v", err)
	}
	deviceCAFile := filepath.Join(dir, "device-ca.pem")
	if err := os.WriteFile(deviceCAFile, deviceCACertPEM, 0o600); err != nil {
		t.Fatalf("write device CA: %v", err)
	}

	// The protocol listener's own leaf: which CA signs it is unrelated to
	// which CA verifies admin clients against it, so signing with the
	// device CA (the only one on disk here) is fine.
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(deviceCACert, deviceCAKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1"},
		CommonName: "657 device-only server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	if err := os.WriteFile(certFile, serverCertPEM, 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyFile, serverKeyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}

	svc := handler.NewAdminCertServiceWithCAs(nil, nil, nil, deviceCACert, deviceCAKey)

	cfg := &config.Config{
		Addr: mustProbePort(t),
		// #657: a path that is configured but absent on disk, matching
		// what envPathOrCertDir actually resolves SEP2_CA to when unset
		// (the cert-dir default), never a truly empty CAFile - that shape
		// is not reachable from the built binary (design doc section 1).
		CAFile:       filepath.Join(dir, "no-serving-ca-here.crt"),
		CertFile:     certFile,
		KeyFile:      keyFile,
		DeviceCAFile: deviceCAFile,
		AdminListen:  mustProbePort(t),
		AdminTLS:     true,
		TZOffset:     -28800,
		TimeQuality:  sep2.TimeQualityNTP,
	}

	// #657: capture the boot log (like TestAdminMTLSAdmissionIsLogged) so
	// the description half of item 4 is asserted here too: the admin
	// listener's "Admin server listening" line must say the anchor was not
	// loaded, not read as a working one. server.Run keeps logging (the
	// connection banner) from its own goroutine after the readiness probe
	// first answers, so the buffer needs its own lock rather than relying
	// on happens-before with the test goroutine.
	logBuf := &syncLogBuffer{}
	prevOut := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	bootAdminListener(t, cfg, svc)

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logBuf.String(), "NOT LOADED") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := logBuf.String(); !strings.Contains(got, "NOT LOADED") {
		t.Errorf("boot log = %s, want the admin listener's description to say the anchor was NOT LOADED", got)
	}
}

// syncLogBuffer is a mutex-guarded io.Writer for capturing the shared
// stdlib log.Writer() output across goroutines: strings.Builder alone is
// not safe for the concurrent write (server.Run's own goroutines) and read
// (the test asserting on it) this capture needs.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAdminListenerDegradedAnchorDeniesButServesCertless is #657 design
// section 3 test 3, driven at shape 1 (no CA path configured anywhere):
// the listener still starts, an operator certificate is refused, and a
// certless client still reaches sign-in. Red before the fix: adminClientCAPool's
// empty-anchor branch returned an error, so the admin listener never came up
// at all (nothing to assert a refusal against).
//
// The refusal here is read from the CLIENT's handshake error (adminClientFor
// forces presentation past the polite hint-honoring a real client would do),
// per this task's evidence discipline on TLS 1.3 client-side false
// positives. That error text does not distinguish a correct empty ClientCAs
// pool from the #418 regression (a nil pool falling back to host roots this
// test's CA is not in either), so it cannot guard #418 by itself:
// TestAdminClientCAPoolEmptyAnchorDegrades and
// TestBuildAdminTLSConfigDegradedAnchorHasNonNilClientCAs do that, by
// reading the pool directly, because nil-vs-empty is not observable on the
// wire (an empty pool's Subjects() has len 0, so crypto/tls omits
// certificate_authorities for it exactly as it does for nil - see
// handshake_server_tls13.go's len(certificateAuthorities) > 0 gate). What
// this test proves, and can prove: a certificate outside the anchor is
// refused end-to-end, and a certless client is not swept up in that refusal.
func TestAdminListenerDegradedAnchorDeniesButServesCertless(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Any CA signs the operator cert under test and the listener's own
	// leaf; shape 1's anchor is empty regardless of which CA this is, so
	// it must refuse a certificate from THIS one same as any other.
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 shape-1 CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1"},
		CommonName: "657 shape-1 server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	if err := os.WriteFile(certFile, serverCertPEM, 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyFile, serverKeyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}
	operatorCertPEM, operatorKeyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "657 operator, shape-1 anchor",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAdminCert: %v", err)
	}

	svc := handler.NewAdminCertServiceWithCAs(caCert, caKey, caCertPEM, nil, nil)

	// The protocol listener's device pool needs a real file on disk; reuse
	// the same CA there so only the ADMIN anchor (CAFile/ServingCAFile/
	// AdminClientCA, all left empty below) is shape 1.
	deviceCAFile := filepath.Join(dir, "device-ca.pem")
	if err := os.WriteFile(deviceCAFile, caCertPEM, 0o600); err != nil {
		t.Fatalf("write device CA: %v", err)
	}

	cfg := &config.Config{
		Addr:         mustProbePort(t),
		CertFile:     certFile,
		KeyFile:      keyFile,
		DeviceCAFile: deviceCAFile,
		AdminListen:  mustProbePort(t),
		AdminTLS:     true,
		TZOffset:     -28800,
		TimeQuality:  sep2.TimeQualityNTP,
	}

	bootAdminListener(t, cfg, svc)
	adminAddr := cfg.AdminListen

	badResp, badErr := adminClientFor(t, operatorCertPEM, operatorKeyPEM).Get("https://" + adminAddr + "/api/certs/ca")
	if badErr == nil {
		_ = badResp.Body.Close()
		t.Fatal("operator cert was accepted although shape 1 has no anchor configured at all; the degraded pool is not empty")
	}
	if !strings.Contains(badErr.Error(), "tls:") {
		t.Errorf("err = %v, want a TLS-layer refusal", badErr)
	}

	loginResp, err := adminClientFor(t, nil, nil).Get("https://" + adminAddr + "/login")
	if err != nil {
		t.Fatalf("certless client on a degraded anchor: handshake itself failed, want it to still complete: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()
	if loginResp.StatusCode != http.StatusOK {
		t.Errorf("certless GET /login on a degraded anchor: status = %d, want 200", loginResp.StatusCode)
	}
}

// TestAdminListenerExplicitClientCAMissingFileRefusesToStart is #657
// design section 3 test 4's missing-path half, at the server.Run level
// TestAdminTLSConfigBrokenPair already uses for a fail-fast boot refusal.
func TestAdminListenerExplicitClientCAMissingFileRefusesToStart(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.pem")

	cfg := &config.Config{
		Addr:          c.sep2Probe,
		CertFile:      c.certFile,
		KeyFile:       c.keyFile,
		CAFile:        c.caFile,
		AdminListen:   c.adminProbe,
		AdminTLS:      true,
		AdminClientCA: missing,
		TZOffset:      -28800,
		TimeQuality:   sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, cfg, c.svc) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("server.Run accepted a missing SEP2_ADMIN_CLIENT_CA path; want error")
		}
		if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "SEP2_ADMIN_CLIENT_CA") {
			t.Errorf("err = %v, want it to name the path %q and SEP2_ADMIN_CLIENT_CA", err, missing)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server.Run did not fail-fast on a missing SEP2_ADMIN_CLIENT_CA within 2s")
	}
}

// TestAdminListenerExplicitClientCANonCARefusesToStart is #657 design
// section 3 test 4's non-CA half. Red before the fix: AppendCertsFromPEM
// never checks IsCA, so this configuration booted successfully.
func TestAdminListenerExplicitClientCANonCARefusesToStart(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)

	caCert, caKey, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA(serving): %v", err)
	}
	nonCALeafPEM, _ := genPlainClientCert(t, caCert, caKey, "657 explicit non-CA anchor")
	nonCAFile := filepath.Join(t.TempDir(), "non-ca-anchor.pem")
	if err := os.WriteFile(nonCAFile, nonCALeafPEM, 0o600); err != nil {
		t.Fatalf("write non-CA anchor: %v", err)
	}

	cfg := &config.Config{
		Addr:          c.sep2Probe,
		CertFile:      c.certFile,
		KeyFile:       c.keyFile,
		CAFile:        c.caFile,
		AdminListen:   c.adminProbe,
		AdminTLS:      true,
		AdminClientCA: nonCAFile,
		TZOffset:      -28800,
		TimeQuality:   sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, cfg, c.svc) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("server.Run accepted a non-CA SEP2_ADMIN_CLIENT_CA file; want error")
		}
		if !strings.Contains(err.Error(), "no CA certificate") || !strings.Contains(err.Error(), "SEP2_ADMIN_CLIENT_CA") {
			t.Errorf("err = %v, want it to name \"no CA certificate\" and SEP2_ADMIN_CLIENT_CA", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server.Run did not fail-fast on a non-CA SEP2_ADMIN_CLIENT_CA within 2s")
	}
}
