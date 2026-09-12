package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

type acceptCountingListener struct {
	net.Listener
	accepts *atomic.Int32
}

func (l acceptCountingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

// newLoopbackReceiver starts an HTTP receiver on 127.0.0.1 and counts its TCP
// accepts separately from the requests it serves.
func newLoopbackReceiver(t *testing.T) (url string, accepts, requests *atomic.Int32) {
	t.Helper()
	accepts, requests = new(atomic.Int32), new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	srv.Listener = acceptCountingListener{Listener: srv.Listener, accepts: accepts}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL, accepts, requests
}

// postSubscriptionThroughRouter stubs a client certificate so the protocol
// router's identity and ACL middleware admit the POST.
func postSubscriptionThroughRouter(t *testing.T, h http.Handler, edevID, uri string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`<Subscription xmlns="urn:ieee:std:2030.5:ns">` +
		`<subscribedResource>/edev/` + edevID + `</subscribedResource>` +
		`<notificationURI>` + uri + `</notificationURI>` +
		`<encoding>0</encoding>` +
		`</Subscription>`)
	req := httptest.NewRequest(http.MethodPost, "/edev/"+edevID+"/sub", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func assertRefusedAndUnstored(t *testing.T, rr *httptest.ResponseRecorder, stores *server.Stores, edevID string, accepts *atomic.Int32) {
	t.Helper()
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	stored, err := stores.Subscriptions.ListByDeviceWithIDs(context.Background(), edevID)
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("stored %d subscriptions, want 0", len(stored))
	}

	// Deliver whatever is stored for the resource through a Manager that
	// allows loopback. A refused create stored nothing, so the receiver sees
	// no connection; had the refusal stored the subscription, this delivers.
	controlURL, _, controlRequests := newLoopbackReceiver(t)
	if err := stores.Subscriptions.Create(context.Background(), "probe-control", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/probe/sub/probe-control"}},
		SubscribedResource:   "/probe-control",
		NotificationURI:      controlURL + "/notify",
	}); err != nil {
		t.Fatalf("seed probe control: %v", err)
	}
	probe := coresub.NewManager(stores.Subscriptions, 1, 4,
		coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		probe.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	probe.Notify(ctx, "/edev/"+edevID, sep2.NotificationStatusChanged)
	probe.Notify(ctx, "/probe-control", sep2.NotificationStatusChanged)
	waitForCondition(t, "probe control delivery", func() bool { return controlRequests.Load() == 1 })

	if n := accepts.Load(); n != 0 {
		t.Errorf("loopback receiver accepted %d connections, want 0", n)
	}
}

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProtocolRouterDefaultConfigRefusesMulticastAndMetadata(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"http://224.0.0.251:8080/notify",
		"http://[ff02::1]:8080/notify",
		"http://[ff01::1]:8080/notify",
		"http://[fd00:ec2::254]/latest/meta-data",
		"http://[fd20:ce::254]/computeMetadata/v1",
		"http://100.100.100.200/latest/meta-data",
		"http://[64:ff9b:1::7f00:1]:8080/notify",
	} {
		t.Run(uri, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			stores := newTestStores()
			mgr := server.NewSubscriptionNotifier(cfg, stores.Subscriptions, 1, 4)
			h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", mgr)

			rr := postSubscriptionThroughRouter(t, h, "edev-1", uri)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			stored, err := stores.Subscriptions.ListByDeviceWithIDs(context.Background(), "edev-1")
			if err != nil {
				t.Fatalf("ListByDeviceWithIDs: %v", err)
			}
			if len(stored) != 0 {
				t.Errorf("stored %d subscriptions, want 0", len(stored))
			}
		})
	}
}

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureServerLog redirects the process-wide logger for the rest of t.
// Callers must not be parallel.
func captureServerLog(t *testing.T) *lockedLogBuffer {
	t.Helper()
	logs := &lockedLogBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return logs
}

// Not parallel: it swaps the process-wide log output.
func TestProtocolRouterLogsValidatorFallbackOnce(t *testing.T) {
	const (
		handlerFallback = "no notificationURI validator wired"
		adapterFallback = "notifier has no notificationURI validator"
	)
	for _, tc := range []struct {
		name                 string
		notifier             func(*server.Stores) handler.ResourceNotifier
		wantHandler, wantAdp int
	}{
		{"nil notifier", func(*server.Stores) handler.ResourceNotifier { return nil }, 1, 0},
		{"notifier without a destination policy", func(*server.Stores) handler.ResourceNotifier { return notifyOnly{} }, 0, 1},
		{"subscription manager", func(s *server.Stores) handler.ResourceNotifier {
			return server.NewSubscriptionNotifier(&config.Config{}, s.Subscriptions, 1, 4)
		}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureServerLog(t)
			stores := newTestStores()
			h, _ := server.BuildProtocolRouter(&config.Config{}, stores, nil, "", "", tc.notifier(stores))
			for range 2 {
				_ = postSubscriptionThroughRouter(t, h, "edev-1", "http://127.0.0.1:8080/notify")
			}
			got := logs.String()
			if n := strings.Count(got, handlerFallback); n != tc.wantHandler {
				t.Errorf("handler fallback logged %d times, want %d; log:\n%s", n, tc.wantHandler, got)
			}
			if n := strings.Count(got, adapterFallback); n != tc.wantAdp {
				t.Errorf("adapter fallback logged %d times, want %d; log:\n%s", n, tc.wantAdp, got)
			}
		})
	}
}

// A zero config is what cmd/sep2server builds when
// SEP2_NOTIFICATION_ALLOW_LOOPBACK is unset.
func TestProtocolRouterDefaultConfigRefusesLoopbackNotificationURI(t *testing.T) {
	t.Parallel()

	uri, accepts, _ := newLoopbackReceiver(t)
	cfg := &config.Config{}
	stores := newTestStores()
	mgr := server.NewSubscriptionNotifier(cfg, stores.Subscriptions, 1, 4)
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", mgr)

	rr := postSubscriptionThroughRouter(t, h, "edev-1", uri+"/notify")
	assertRefusedAndUnstored(t, rr, stores, "edev-1", accepts)
}

type notifyOnly struct{}

func (notifyOnly) Notify(context.Context, string, uint8) {}

func TestProtocolRouterWithoutManagerPolicyRefusesLoopback(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		notifier handler.ResourceNotifier
	}{
		{"nil notifier", nil},
		{"notifier without a destination policy", notifyOnly{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, accepts, _ := newLoopbackReceiver(t)
			stores := newTestStores()
			h, _ := server.BuildProtocolRouter(&config.Config{}, stores, nil, "", "", tc.notifier)

			rr := postSubscriptionThroughRouter(t, h, "edev-1", uri+"/notify")
			assertRefusedAndUnstored(t, rr, stores, "edev-1", accepts)
		})
	}
}

func TestProtocolRouterLoopbackOptInFromConfig(t *testing.T) {
	t.Parallel()

	uri, _, requests := newLoopbackReceiver(t)
	cfg := &config.Config{NotificationAllowLoopback: true}
	stores := newTestStores()
	mgr := server.NewSubscriptionNotifier(cfg, stores.Subscriptions, 1, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("manager did not shut down")
		}
	})
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", mgr)

	rr := postSubscriptionThroughRouter(t, h, "edev-1", uri+"/notify")
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	stored, err := stores.Subscriptions.ListByDeviceWithIDs(ctx, "edev-1")
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs: %v", err)
	}
	if len(stored) != 1 || stored[0].Subscription.NotificationURI != uri+"/notify" {
		t.Fatalf("stored = %+v, want one subscription with NotificationURI %q", stored, uri+"/notify")
	}

	mgr.Notify(ctx, "/edev/edev-1", sep2.NotificationStatusChanged)
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("loopback receiver got no notification with the opt-in set")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
