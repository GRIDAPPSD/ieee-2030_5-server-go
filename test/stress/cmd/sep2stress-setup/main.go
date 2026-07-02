// cmd/sep2stress-setup generates PKI and registers virtual clients for
// the IEEESRV-007 stress harness.
//
// Two modes:
//
//  1. PKI generation (default): generates a CA + N device certs and writes
//     them under -out-dir.
//
//  2. Client registration (-register): reads device certs from -pki-dir and
//     POSTs to POST /edev on the server to register each device before load.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

func main() {
	var (
		register  = flag.Bool("register", false, "register mode: POST /edev for each device cert")
		seed      = flag.Uint64("seed", 42, "RNG seed for PKI generation")
		count     = flag.Int("count", 5, "number of device certs to generate")
		outDir    = flag.String("out-dir", "", "PKI output directory (PKI mode)")
		pkiDir    = flag.String("pki-dir", "", "PKI directory to read certs from (register mode)")
		serverURL = flag.String("server", "https://127.0.0.1:8443", "server base URL (register mode)")
		serverCN  = flag.String("server-cn", "SEP2StressServer", "server cert CommonName (PKI mode)")
	)
	flag.Parse()

	if *register {
		if err := runRegister(*pkiDir, *serverURL, *count); err != nil {
			log.Fatalf("register: %v", err)
		}
		return
	}

	if err := runGenPKI(*outDir, *serverCN, *seed, *count); err != nil {
		log.Fatalf("gen pki: %v", err)
	}
}

// runGenPKI generates a CA, a server cert, and count device certs under outDir.
func runGenPKI(outDir, serverCN string, seed uint64, count int) error {
	if outDir == "" {
		return fmt.Errorf("-out-dir required for PKI generation")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	log.Printf("generating CA (seed=%d)...", seed)
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: fmt.Sprintf("SEP2StressCA-%d", seed),
		ValidYears: 1,
	})
	if err != nil {
		return fmt.Errorf("generate CA: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "ca.crt"), caCertPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "ca.key"), caKeyPEM, 0o600); err != nil {
		return err
	}
	log.Printf("CA written to %s", outDir)

	// Parse CA for signing.
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}

	// Server cert (localhost + 127.0.0.1).
	log.Printf("generating server cert (CN=%s)...", serverCN)
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: serverCN,
		ValidYears: 1,
	})
	if err != nil {
		return fmt.Errorf("generate server cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "server.crt"), serverCertPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "server.key"), serverKeyPEM, 0o600); err != nil {
		return err
	}
	log.Printf("server cert written")

	// Device certs.
	log.Printf("generating %d device certs...", count)
	for i := 0; i < count; i++ {
		devCertPEM, devKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceTypeGeneric,
			HWSerialNum: fmt.Sprintf("STRESS-%d-%05d", seed, i),
		})
		if err != nil {
			return fmt.Errorf("generate device cert %d: %w", i, err)
		}
		if err := os.WriteFile(filepath.Join(outDir, fmt.Sprintf("device-%05d.crt", i)), devCertPEM, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, fmt.Sprintf("device-%05d.key", i)), devKeyPEM, 0o600); err != nil {
			return err
		}
	}
	log.Printf("device certs written (%d total)", count)
	return nil
}

// runRegister POSTs to /edev for each device cert to register it with the server.
func runRegister(pkiDir, serverURL string, count int) error {
	if pkiDir == "" {
		return fmt.Errorf("-pki-dir required for registration")
	}

	// Read CA for server trust.
	caCertPEM, err := os.ReadFile(filepath.Join(pkiDir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read ca.crt: %w", err)
	}
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caCertPEM) {
		return fmt.Errorf("no certs in ca.crt")
	}

	// Derive the server hostname from the URL so non-loopback runs do not
	// spuriously fail TLS verification due to a hardcoded "127.0.0.1" ServerName.
	serverHost, err := hostFromURL(serverURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	log.Printf("registering %d clients at %s/edev...", count, serverURL)
	ok, failed := 0, 0
	for i := 0; i < count; i++ {
		certPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.crt", i))
		keyPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.key", i))
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			log.Printf("  [%d] read cert: %v", i, err)
			failed++
			continue
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			log.Printf("  [%d] read key: %v", i, err)
			failed++
			continue
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			log.Printf("  [%d] key pair: %v", i, err)
			failed++
			continue
		}
		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{tlsCert},
					RootCAs:      rootPool,
					ServerName:   serverHost, // derived from -server flag, not hardcoded
					MinVersion:   tls.VersionTLS12,
					MaxVersion:   tls.VersionTLS12,
				},
			},
		}
		req, err := http.NewRequest(http.MethodPost, serverURL+"/edev", bytes.NewReader(nil))
		if err != nil {
			log.Printf("  [%d] build request: %v", i, err)
			failed++
			continue
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("  [%d] POST /edev: %v", i, err)
			failed++
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			log.Printf("  [%d] POST /edev: unexpected status %d", i, resp.StatusCode)
			failed++
			continue
		}
		ok++
	}
	log.Printf("registration complete: %d ok, %d failed", ok, failed)
	if failed > 0 {
		return fmt.Errorf("%d registrations failed", failed)
	}
	return nil
}

// hostFromURL extracts the hostname (without port) from a URL string.
// Used to derive the TLS ServerName from the -server flag value.
func hostFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
