package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
	pkgserver "github.com/GRIDAPPSD/ieee-2030_5-go/pkg/server"
)

// TestServerStartStop exercises the public Server lifecycle:
// New -> Start (in goroutine) -> Shutdown.
// Verifies both that Start blocks until shutdown and that Shutdown
// returns cleanly with no goroutine leak.
func TestServerStartStop(t *testing.T) {
	dir, port := mustWriteCerts(t)

	srv, err := pkgserver.New(pkgserver.Config{
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		CertFile: filepath.Join(dir, "server.crt"),
		KeyFile:  filepath.Join(dir, "server.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	startErr := make(chan error, 1)
	go func() {
		startErr <- srv.Start(context.Background())
	}()

	// Wait for listener to be ready
	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", port))

	// Make a TLS request to confirm the protocol listener is serving.
	resp, err := mtlsGet(dir, fmt.Sprintf("https://127.0.0.1:%d/dcap", port))
	if err != nil {
		t.Fatalf("mTLS GET /dcap: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Errorf("status = %d, want <500", resp.StatusCode)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	select {
	case err := <-startErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Start returned %v, want nil or ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Start did not return after Shutdown")
	}
}

// TestServerNewRejectsInvalidConfig verifies that New returns an error
// for missing cert files rather than panicking later in Start.
func TestServerNewRejectsInvalidConfig(t *testing.T) {
	_, err := pkgserver.New(pkgserver.Config{
		Addr:     "127.0.0.1:0",
		CertFile: "/nonexistent/server.crt",
		KeyFile:  "/nonexistent/server.key",
		CAFile:   "/nonexistent/ca.crt",
	})
	if err == nil {
		t.Fatal("New with nonexistent files: want error, got nil")
	}
}

// TestServerStartContextCancel confirms Start unblocks when its
// context is cancelled, with no need to call Shutdown.
func TestServerStartContextCancel(t *testing.T) {
	dir, port := mustWriteCerts(t)

	srv, err := pkgserver.New(pkgserver.Config{
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		CertFile: filepath.Join(dir, "server.crt"),
		KeyFile:  filepath.Join(dir, "server.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() {
		startErr <- srv.Start(ctx)
	}()

	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", port))
	cancel()

	select {
	case err := <-startErr:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Start returned %v, want nil/Canceled/ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Start did not return after context cancel")
	}
}

func mustWriteCerts(t *testing.T) (string, int) {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKeyAny, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	caKey := caKeyAny.(*ecdsa.PrivateKey)

	srvCertPEM, srvKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	devCertPEM, devKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-CLIENT",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, data := range map[string][]byte{
		"ca.crt":     caCertPEM,
		"server.crt": srvCertPEM,
		"server.key": srvKeyPEM,
		"device.crt": devCertPEM,
		"device.key": devKeyPEM,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	port := freePort(t)
	return dir, port
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener at %s not ready", addr)
}

func mtlsGet(certDir, url string) (*http.Response, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "device.crt"),
		filepath.Join(certDir, "device.key"),
	)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				MinVersion:   tls.VersionTLS12,
				MaxVersion:   tls.VersionTLS12,
			},
		},
		Timeout: 3 * time.Second,
	}
	return client.Get(url)
}
