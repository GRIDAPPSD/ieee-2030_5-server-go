package sep2capture

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// test material: a CA, a server leaf, and a device leaf

type material struct {
	caCertPEM []byte

	serverCertFile string
	serverKeyFile  string
	caFile         string

	deviceCertPEM []byte
	deviceKeyPEM  []byte
	deviceLeaf    *x509.Certificate
}

func newMaterial(t *testing.T) material {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "sep2capture test CA", ValidYears: 1})
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
		CommonName: "sep2capture test server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "sep2capture-test",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	deviceLeaf, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}

	m := material{
		caCertPEM:      caCertPEM,
		serverCertFile: filepath.Join(dir, "server.pem"),
		serverKeyFile:  filepath.Join(dir, "server-key.pem"),
		caFile:         filepath.Join(dir, "ca.pem"),
		deviceCertPEM:  deviceCertPEM,
		deviceKeyPEM:   deviceKeyPEM,
		deviceLeaf:     deviceLeaf,
	}
	for path, data := range map[string][]byte{
		m.serverCertFile: serverCertPEM,
		m.serverKeyFile:  serverKeyPEM,
		m.caFile:         caCertPEM,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return m
}

func gcmServerConfig(t *testing.T, m material) *tls.Config {
	t.Helper()
	cfg, err := sepTLS.NewServerTLSConfigWithExtraCAs(m.serverCertFile, m.serverKeyFile, m.caFile, nil)
	if err != nil {
		t.Fatalf("NewServerTLSConfigWithExtraCAs: %v", err)
	}
	return cfg
}

func ccmServerConfig(t *testing.T, m material) *gotls.Config {
	t.Helper()
	cfg, err := sepTLS.NewCCMServerConfigWithExtraCAs(m.serverCertFile, m.serverKeyFile, m.caFile, nil)
	if err != nil {
		t.Fatalf("NewCCMServerConfigWithExtraCAs: %v", err)
	}
	return cfg
}

func gcmClientConfig(t *testing.T, m material) *tls.Config {
	t.Helper()
	cfg, err := sepTLS.NewClientTLSConfigFromPEM(m.deviceCertPEM, m.deviceKeyPEM, m.caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}
	return cfg
}

// ccmClientConfig mirrors NewClientTLSConfigFromPEM's shape (config.go) for
// the gotls fork, which has no equivalent constructor. CipherSuites names
// CCM-8 alone, not the GCM fallback NewCCMServerConfig also accepts, so a
// completed handshake proves the CCM-8 path specifically.
func ccmClientConfig(t *testing.T, m material) *gotls.Config {
	t.Helper()
	cert, err := gotls.X509KeyPair(m.deviceCertPEM, m.deviceKeyPEM)
	if err != nil {
		t.Fatalf("gotls.X509KeyPair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.caCertPEM) {
		t.Fatal("AppendCertsFromPEM: no certificates added")
	}
	return &gotls.Config{
		Certificates:     []gotls.Certificate{cert},
		RootCAs:          pool,
		CipherSuites:     []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		MinVersion:       gotls.VersionTLS12,
		MaxVersion:       gotls.VersionTLS12,
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
	}
}

func gcmHTTPClient(t *testing.T, m material) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: gcmClientConfig(t, m)},
		Timeout:   5 * time.Second,
	}
}

// ccmHTTPClient drives net/http's client over a gotls connection dialed by
// hand: http.Transport's own TLS dialing is hardcoded to crypto/tls, which
// cannot negotiate CCM-8 (GOROOT crypto/tls/cipher_suites.go carries no CCM
// suite). DialTLSContext hands Transport an already-negotiated connection
// and it speaks HTTP/1.1 over it exactly as it would over its own.
func ccmHTTPClient(t *testing.T, m material) *http.Client {
	t.Helper()
	cfg := ccmClientConfig(t, m)
	return &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return gotls.DialWithDialer(&net.Dialer{}, network, addr, cfg)
			},
		},
		Timeout: 5 * time.Second,
	}
}

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	return l
}

// the protocol route: internal/auth's real identity middleware, so the
// control (naive wrapper) and the positive case exercise the same consumer
// contract a protocol request goes through in production.

type observedTLS struct {
	mu    sync.Mutex
	state *tls.ConnectionState
}

func (o *observedTLS) set(s *tls.ConnectionState) {
	o.mu.Lock()
	o.state = s
	o.mu.Unlock()
}

func (o *observedTLS) get() *tls.ConnectionState {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

// getWithSeenTLS serves the identity route on l, issues one GET through
// client, and returns the response status alongside whatever r.TLS the
// /dcap handler observed (nil if IdentityMiddleware never reached it).
func getWithSeenTLS(t *testing.T, l net.Listener, client *http.Client, url string) (int, *tls.ConnectionState) {
	t.Helper()

	var seen observedTLS
	mux := http.NewServeMux()
	mux.HandleFunc("/dcap", func(w http.ResponseWriter, r *http.Request) {
		seen.set(r.TLS)
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: auth.IdentityMiddleware(mux)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, seen.get()
}

func assertPeerIsLeaf(t *testing.T, seen *tls.ConnectionState, leaf *x509.Certificate) {
	t.Helper()
	if seen == nil {
		t.Fatal("handler observed r.TLS == nil")
	}
	if len(seen.PeerCertificates) == 0 {
		t.Fatal("handler observed r.TLS with no PeerCertificates")
	}
	if !seen.PeerCertificates[0].Equal(leaf) {
		t.Error("PeerCertificates[0] does not equal the client's leaf certificate")
	}
}

// item 2 and item 3: identity per mode, and the control that proves it

// TestIdentityPreservedThroughListener is item 2: a protocol route sees the
// client's leaf certificate through the listener, in both cipher modes.
func TestIdentityPreservedThroughListener(t *testing.T) {
	t.Parallel()

	t.Run("GCM", func(t *testing.T) {
		t.Parallel()
		m := newMaterial(t)
		l := NewListener(tls.NewListener(listenTCP(t), gcmServerConfig(t, m)), nil)
		status, seen := getWithSeenTLS(t, l, gcmHTTPClient(t, m), "https://"+l.Addr().String()+"/dcap")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		assertPeerIsLeaf(t, seen, m.deviceLeaf)
	})

	t.Run("CCM", func(t *testing.T) {
		t.Parallel()
		m := newMaterial(t)
		l := NewListener(gotls.NewListener(listenTCP(t), ccmServerConfig(t, m)), nil)
		status, seen := getWithSeenTLS(t, l, ccmHTTPClient(t, m), "https://"+l.Addr().String()+"/dcap")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		assertPeerIsLeaf(t, seen, m.deviceLeaf)
		if seen.CipherSuite != gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
			t.Errorf("cipher suite = %#04x, want CCM-8 %#04x (the client offered only CCM-8, so anything else means the wrong path ran)", seen.CipherSuite, gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8)
		}
	})
}

// naiveConn is the bug this package exists to prevent: it forwards every
// net.Conn method by embedding the interface, so it satisfies net.Conn, but
// it does not expose ConnectionState, because net.Conn's own method set
// never declares it, regardless of what the embedded value's concrete type
// is. net/http therefore leaves http.Request.TLS nil for every request
// through it.
type naiveConn struct {
	net.Conn
}

// naiveListener performs the identical handshake Listener does, then hands
// back a naiveConn instead of a *Conn. It isolates the one thing item 3
// tests: that implementing ConnectionState, not merely completing the
// handshake before Accept returns, is what keeps identity alive.
type naiveListener struct {
	inner net.Listener
}

func (l *naiveListener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	hs, ok := c.(handshaker)
	if !ok {
		_ = c.Close()
		return nil, errors.New("naiveListener: connection does not implement a TLS handshake")
	}
	if err := hs.HandshakeContext(context.Background()); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &naiveConn{Conn: c}, nil
}

func (l *naiveListener) Close() error   { return l.inner.Close() }
func (l *naiveListener) Addr() net.Addr { return l.inner.Addr() }

var _ net.Listener = (*naiveListener)(nil)

// TestNaiveWrapperLosesIdentity is item 3, the control: without this, item
// 2 above proves nothing, because a route that always answers 200
// regardless of the listener would pass it too.
func TestNaiveWrapperLosesIdentity(t *testing.T) {
	t.Parallel()

	t.Run("GCM", func(t *testing.T) {
		t.Parallel()
		m := newMaterial(t)
		l := &naiveListener{inner: tls.NewListener(listenTCP(t), gcmServerConfig(t, m))}
		status, seen := getWithSeenTLS(t, l, gcmHTTPClient(t, m), "https://"+l.Addr().String()+"/dcap")
		if status != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (IdentityMiddleware must see r.TLS == nil through the naive wrapper)", status)
		}
		if seen != nil {
			t.Error("handler observed r.TLS through the naive wrapper; it must never be reached")
		}
	})

	t.Run("CCM", func(t *testing.T) {
		t.Parallel()
		m := newMaterial(t)
		l := &naiveListener{inner: gotls.NewListener(listenTCP(t), ccmServerConfig(t, m))}
		status, seen := getWithSeenTLS(t, l, ccmHTTPClient(t, m), "https://"+l.Addr().String()+"/dcap")
		if status != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (IdentityMiddleware must see r.TLS == nil through the naive wrapper)", status)
		}
		if seen != nil {
			t.Error("handler observed r.TLS through the naive wrapper; it must never be reached")
		}
	})
}

// item 4: a refused certificate is reported, not silently dropped

// syncBuffer guards a bytes.Buffer with a mutex. log.Logger serializes its
// own Write calls but a test reading the buffer's contents from outside the
// logger is a second, unsynchronized accessor; -race catches exactly that
// on a plain bytes.Buffer here.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func TestRefusedCertificateIsReported(t *testing.T) {
	t.Parallel()
	m := newMaterial(t)

	logBuf := &syncBuffer{}
	logger := log.New(logBuf, "", 0)
	l := NewListener(tls.NewListener(listenTCP(t), gcmServerConfig(t, m)), logger)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	// No client certificate at all: RequireAnyClientCert (config.go) rejects
	// the handshake before any application data crosses. Same control
	// TestNew_GCM_MTLSAcceptAndReject already relies on in pkg/sep2srv.
	noCertCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only control; the server enforces client auth regardless
	dialErrCh := make(chan error, 1)
	go func() {
		_, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", l.Addr().String(), noCertCfg)
		dialErrCh <- err
	}()

	select {
	case err := <-dialErrCh:
		if err == nil {
			t.Fatal("dial without a client certificate unexpectedly completed the handshake")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dial without a client certificate never returned")
	}

	deadline := time.Now().Add(2 * time.Second)
	for logBuf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	got := logBuf.String()
	if !strings.Contains(got, "sep2capture: TLS handshake error") {
		t.Errorf("log = %q, want a TLS handshake error line", got)
	}
	if !strings.Contains(got, "127.0.0.1:") {
		t.Errorf("log = %q, want the peer address (127.0.0.1:<port>)", got)
	}
}

// item 5: Close leaves no goroutine behind, proved by counting

func TestCloseLeavesNoGoroutineBehind(t *testing.T) {
	m := newMaterial(t)
	l := NewListener(tls.NewListener(listenTCP(t), gcmServerConfig(t, m)), log.New(io.Discard, "", 0))

	baseline := runtime.NumGoroutine()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()

	clientCfg := gcmClientConfig(t, m)
	for i := 0; i < 3; i++ {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", l.Addr().String(), clientCfg)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = conn.Close()
	}

	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Close: %v", err)
	}
	<-acceptDone

	assertGoroutinesSettle(t, baseline)
}

func assertGoroutinesSettle(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.Gosched()
		if n := runtime.NumGoroutine(); n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine count settled at %d, want <= %d (baseline)\n%s", runtime.NumGoroutine(), baseline, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// item 6: an already-handshaken input (core's WrapCCMListener) works

func TestAcceptsAlreadyHandshakenInput(t *testing.T) {
	t.Parallel()
	m := newMaterial(t)

	inner := gotls.NewListener(listenTCP(t), ccmServerConfig(t, m))
	preHandshaken := sepTLS.WrapCCMListener(inner, log.New(io.Discard, "", 0))
	l := NewListener(preHandshaken, nil)

	status, seen := getWithSeenTLS(t, l, ccmHTTPClient(t, m), "https://"+l.Addr().String()+"/dcap")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	assertPeerIsLeaf(t, seen, m.deviceLeaf)
}
