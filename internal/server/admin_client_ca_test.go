package server_test

// #624: the admin listener never set ClientCAs, so an operator certificate
// signed by a private CA verified against the HOST ROOT set and failed the
// handshake before AdminAuthMiddleware's policy-OID check ever ran (#418).
// The fix routes the admin listener's ClientCAs pool through
// Config.EffectiveAdminClientCA, defaulting to the serving CA (the CA that
// signs the operator cert, per #622's role split), with an explicit
// "system" value that keeps host-root verification. These tests drive real
// TLS handshakes against a real admin listener, both ways, per issue
// #624's three done-conditions.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
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
