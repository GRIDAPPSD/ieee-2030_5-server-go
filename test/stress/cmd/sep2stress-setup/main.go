// cmd/sep2stress-setup generates PKI and registers virtual clients for
// the stress harness.
//
// Three modes:
//
//  1. PKI generation (default): generates a CA + N device certs and writes
//     them under -out-dir.
//
//  2. Client registration (-register): reads device certs from -pki-dir and
//     POSTs to POST /edev on the server to register each device before load.
//     Writes edev-manifest.json to -pki-dir listing the assigned edev IDs
//     so the subscribe mode can read them.
//
//  3. Subscription registration (-subscribe): reads edev-manifest.json and
//     device certs from -pki-dir, then POSTs a Subscription for each device
//     pointing at -notify-url. Used by the fanout dimension to seed the
//     subscription worker pool with real subscribers.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

func main() {
	var (
		register  = flag.Bool("register", false, "register mode: POST /edev for each device cert")
		subscribe = flag.Bool("subscribe", false, "subscribe mode: POST /edev/{id}/sub for each registered device")
		seed      = flag.Uint64("seed", 42, "RNG seed for PKI generation")
		count     = flag.Int("count", 5, "number of device certs to generate")
		outDir    = flag.String("out-dir", "", "PKI output directory (PKI mode)")
		pkiDir    = flag.String("pki-dir", "", "PKI directory to read certs from (register/subscribe mode)")
		serverURL = flag.String("server", "https://127.0.0.1:8443", "server base URL (register/subscribe mode)")
		serverCN  = flag.String("server-cn", "SEP2StressServer", "server cert CommonName (PKI mode)")
		// notifyURL is the plain-HTTP URL where the loadgen receiver listens.
		// The server POSTs outbound Notifications here; it does not need mTLS
		// because the receiver is a plain HTTP listener owned by the harness.
		notifyURL = flag.String("notify-url", "", "notification receiver URL (subscribe mode, e.g. http://127.0.0.1:18081; a loopback URL needs the server run with SEP2_NOTIFICATION_ALLOW_LOOPBACK=true)")
		// subscribedResource is the SHARED href all N subscriptions target.
		// Must be a server-wide resource all device certs can access. /dcap
		// is the default: a SEP2 singleton that a single stress-notify mutation
		// fans out to all N subscribers simultaneously, driving the subscription
		// worker pool by subscriber count rather than injection rate.
		subscribedResource = flag.String("subscribed-resource", "", "shared subscribed-resource href (subscribe mode; default: /dcap)")
	)
	flag.Parse()

	if *subscribe {
		if err := runSubscribe(*pkiDir, *serverURL, *count, *notifyURL, *subscribedResource); err != nil {
			log.Fatalf("subscribe: %v", err)
		}
		return
	}

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

// edevManifest is the JSON file written by runRegister so runSubscribe can
// read back the assigned edev IDs without re-registering.
type edevManifest struct {
	EdevIDs []string `json:"edev_ids"`
}

// manifestPath returns the canonical path for edev-manifest.json in pkiDir.
func manifestPath(pkiDir string) string {
	return filepath.Join(pkiDir, "edev-manifest.json")
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
// It captures the assigned edev ID from the Location header and writes
// edev-manifest.json so the subscribe mode can read back IDs without re-registering.
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
	var edevIDs []string

	for i := 0; i < count; i++ {
		certPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.crt", i))
		keyPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.key", i))
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			log.Printf("  [%d] read cert: %v", i, err)
			failed++
			edevIDs = append(edevIDs, "")
			continue
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			log.Printf("  [%d] read key: %v", i, err)
			failed++
			edevIDs = append(edevIDs, "")
			continue
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			log.Printf("  [%d] key pair: %v", i, err)
			failed++
			edevIDs = append(edevIDs, "")
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
			edevIDs = append(edevIDs, "")
			continue
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("  [%d] POST /edev: %v", i, err)
			failed++
			edevIDs = append(edevIDs, "")
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			log.Printf("  [%d] POST /edev: unexpected status %d", i, resp.StatusCode)
			failed++
			edevIDs = append(edevIDs, "")
			continue
		}
		// Capture the assigned edev ID from Location: /edev/{id}
		loc := resp.Header.Get("Location")
		edevID := edevIDFromLocation(loc)
		if edevID == "" {
			log.Printf("  [%d] POST /edev: no Location or unrecognised format %q", i, loc)
			edevIDs = append(edevIDs, "")
		} else {
			edevIDs = append(edevIDs, edevID)
		}
		ok++
	}
	log.Printf("registration complete: %d ok, %d failed", ok, failed)

	// Write the manifest regardless of partial failure so subscribe mode can
	// skip blank IDs gracefully.
	m := edevManifest{EdevIDs: edevIDs}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if werr := os.WriteFile(manifestPath(pkiDir), mb, 0o644); werr != nil {
		return fmt.Errorf("write manifest: %w", werr)
	}
	log.Printf("edev-manifest.json written (%d IDs)", len(edevIDs))

	if failed > 0 {
		return fmt.Errorf("%d registrations failed", failed)
	}
	return nil
}

// runSubscribe POSTs a Subscription for each registered device, pointing
// the notificationURI at notifyURL. All subscriptions share the same
// subscribedResource (default: /dcap) so a single stress-notify mutation
// fans out to ALL N subscribers, driving the 4-worker/256-queue by
// subscriber count.
func runSubscribe(pkiDir, serverURL string, count int, notifyURL, subscribedResource string) error {
	if pkiDir == "" {
		return fmt.Errorf("-pki-dir required for subscription registration")
	}
	if notifyURL == "" {
		return fmt.Errorf("-notify-url required for subscription registration")
	}

	// Read the edev manifest written by runRegister.
	mf, err := readManifest(pkiDir)
	if err != nil {
		return fmt.Errorf("read edev manifest: %w", err)
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
	serverHost, err := hostFromURL(serverURL)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}

	// cap to min(count, len(mf.EdevIDs))
	n := count
	if n > len(mf.EdevIDs) {
		n = len(mf.EdevIDs)
	}

	log.Printf("subscribing %d devices (notifyURL=%s)...", n, notifyURL)
	ok, failed, skipped := 0, 0, 0

	for i := 0; i < n; i++ {
		edevID := mf.EdevIDs[i]
		if edevID == "" {
			log.Printf("  [%d] skipping: no edev ID in manifest (registration failed)", i)
			skipped++
			continue
		}

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
					ServerName:   serverHost,
					MinVersion:   tls.VersionTLS12,
					MaxVersion:   tls.VersionTLS12,
				},
			},
		}

		// All subscriptions target the SAME shared resource (/dcap by
		// default) so a single stress-notify mutation fans out to ALL N
		// subscribers, driving the 4-worker/256-queue by subscriber count.
		// Using a per-device resource (/edev/{id}/fsa) would mean one
		// mutation reaches exactly ONE subscriber; that measures injection
		// rate, not fan-out capacity. The shared resource must be
		// server-wide and readable by all device certs; /dcap satisfies
		// both constraints. The -subscribed-resource flag overrides.
		subResource := subscribedResource
		if subResource == "" {
			subResource = "/dcap"
		}

		sub := sep2.Subscription{
			SubscribedResource: subResource,
			NotificationURI:    notifyURL,
			Encoding:           sep2.EncodingXML,
		}
		body, err := xml.Marshal(sub)
		if err != nil {
			log.Printf("  [%d] marshal subscription: %v", i, err)
			failed++
			continue
		}

		subURL := serverURL + "/edev/" + edevID + "/sub"
		req, err := http.NewRequest(http.MethodPost, subURL, bytes.NewReader(body))
		if err != nil {
			log.Printf("  [%d] build request: %v", i, err)
			failed++
			continue
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("  [%d] POST %s: %v", i, subURL, err)
			failed++
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			log.Printf("  [%d] POST %s: unexpected status %d", i, subURL, resp.StatusCode)
			failed++
			continue
		}
		ok++
	}

	log.Printf("subscription complete: %d ok, %d failed, %d skipped", ok, failed, skipped)
	if failed > 0 {
		return fmt.Errorf("%d subscriptions failed", failed)
	}
	return nil
}

// readManifest reads edev-manifest.json from pkiDir.
func readManifest(pkiDir string) (*edevManifest, error) {
	b, err := os.ReadFile(manifestPath(pkiDir))
	if err != nil {
		return nil, err
	}
	var m edevManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// edevIDFromLocation extracts the edev ID from a Location header value.
// Handles both relative form ("/edev/{id}") and absolute form
// ("https://host/edev/{id}"). Returns "" when the header is blank or
// the path does not match the /edev/{id} shape.
func edevIDFromLocation(loc string) string {
	if loc == "" {
		return ""
	}
	// Parse as a URL so absolute form (https://host/edev/42) is handled
	// correctly. A plain path (/edev/42) parses fine too: url.Parse
	// treats it as a relative URL with Path="/edev/42".
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	// Strip leading slash for consistent splitting on the path component.
	path := strings.TrimPrefix(u.Path, "/")
	parts := strings.SplitN(path, "/", 3)
	// parts[0]="edev", parts[1]="{id}"
	if len(parts) >= 2 && parts[0] == "edev" && parts[1] != "" {
		return parts[1]
	}
	return ""
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
