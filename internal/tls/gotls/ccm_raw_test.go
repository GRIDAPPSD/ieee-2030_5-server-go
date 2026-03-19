package gotls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestCCMRawHandshake(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)

	sKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sT := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	sDER, _ := x509.CreateCertificate(rand.Reader, sT, caCert, &sKey.PublicKey, caKey)

	cKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	cDER, _ := x509.CreateCertificate(rand.Reader, cTmpl, caCert, &cKey.PublicKey, caKey)

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		srv := Server(raw, &Config{
			Certificates: []Certificate{{Certificate: [][]byte{sDER}, PrivateKey: sKey}},
			ClientCAs:    pool,
			ClientAuth:   RequireAndVerifyClientCert,
			CipherSuites: []uint16{TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
			MinVersion:   VersionTLS12,
			MaxVersion:   VersionTLS12,
		})
		err = srv.Handshake()
		if err != nil {
			serverErr <- err
			raw.Close()
			return
		}
		t.Logf("SERVER: cipher=0x%04X", srv.ConnectionState().CipherSuite)
		srv.Write([]byte("hello"))
		srv.Close()
		serverErr <- nil
	}()

	conn, err := Dial("tcp", ln.Addr().String(), &Config{
		Certificates: []Certificate{{Certificate: [][]byte{cDER}, PrivateKey: cKey}},
		RootCAs:      pool,
		CipherSuites: []uint16{TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		MinVersion:   VersionTLS12,
		MaxVersion:   VersionTLS12,
	})
	if err != nil {
		sErr := <-serverErr
		t.Fatalf("CLIENT dial failed: %v (server: %v)", err, sErr)
	}
	defer conn.Close()

	t.Logf("CLIENT: cipher=0x%04X", conn.ConnectionState().CipherSuite)

	if conn.ConnectionState().CipherSuite != TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
		t.Errorf("cipher=0x%04X, want 0xC0AE", conn.ConnectionState().CipherSuite)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}
