package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
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
	if n := accepts.Load(); n != 0 {
		t.Errorf("loopback receiver accepted %d connections, want 0", n)
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
