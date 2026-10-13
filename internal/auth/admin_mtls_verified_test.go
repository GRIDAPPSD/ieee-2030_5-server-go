package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// selfSignedAdminCert is a client certificate carrying the admin policy OID
// that no CA the server trusts has signed.
func selfSignedAdminCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Self-signed Admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	arcs := make([]uint64, len(certs.OIDPolicyAdmin))
	for i, a := range certs.OIDPolicyAdmin {
		arcs[i] = uint64(a)
	}
	oid, err := x509.OIDFromInts(arcs)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.Policies = []x509.OID{oid}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if !certs.HasPolicyOID(leaf, certs.OIDPolicyAdmin) {
		t.Fatal("test certificate does not carry the admin policy OID")
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// caIssuedAdminCert returns an admin certificate and the pool of the CA that
// signed it.
func caIssuedAdminCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caCert, caKey := mustGenCA(t)
	certPEM, keyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{CommonName: "CA Admin", ValidYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pair, pool
}

// getOverTLS serves the bypass-off admin middleware on a real TLS listener
// with the given client-auth policy and returns the status a client
// presenting cert gets for GET /api/fsas.
func getOverTLS(t *testing.T, clientAuth tls.ClientAuthType, clientCAs *x509.CertPool, cert tls.Certificate) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(auth.AdminAuthMiddleware("test-key", nil, nil, false)(okHandler()))
	srv.TLS = &tls.Config{ClientAuth: clientAuth, ClientCAs: clientCAs, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client()
	// GetClientCertificate sends cert whatever CAs the server names, as a
	// hostile client would.
	client.Transport.(*http.Transport).TLSClientConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return &cert, nil
	}
	resp, err := client.Get(srv.URL + "/api/fsas")
	if err != nil {
		t.Fatalf("GET /api/fsas: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// A listener that requests a client certificate without verifying it hands
// the middleware an unverified chain; only a verified chain is a credential.
func TestAdminAuthMTLSRequiresAVerifiedChain(t *testing.T) {
	selfSigned := selfSignedAdminCert(t)
	caIssued, pool := caIssuedAdminCert(t)

	for _, tc := range []struct {
		name       string
		clientAuth tls.ClientAuthType
		cert       tls.Certificate
	}{
		{"self-signed under RequestClientCert", tls.RequestClientCert, selfSigned},
		{"self-signed under RequireAnyClientCert", tls.RequireAnyClientCert, selfSigned},
		{"CA-issued but unverified under RequestClientCert", tls.RequestClientCert, caIssued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := getOverTLS(t, tc.clientAuth, pool, tc.cert); got != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", got)
			}
		})
	}

	// The control: a certificate the listener verified against the admin CA
	// is still admitted.
	if got := getOverTLS(t, tls.VerifyClientCertIfGiven, pool, caIssued); got != http.StatusOK {
		t.Fatalf("verified admin certificate: status = %d, want 200", got)
	}
}
