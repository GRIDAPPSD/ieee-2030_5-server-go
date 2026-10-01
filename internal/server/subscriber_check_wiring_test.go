package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// The server wraps its Manager before handing it to the protocol router, so
// the router's delivery-time read check must pass through that wrapper.
func TestBuildProtocolRouter_SubscriberCheckReachesTheWrappedManager(t *testing.T) {
	t.Parallel()
	paths := make(chan string, 8)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)

	ctx := context.Background()
	stores := newTestStores()
	for id, lfdi := range map[string]string{
		"3": "0000000000000000000000000000000000000003",
		"5": "0000000000000000000000000000000000000005",
	} {
		if err := stores.EndDevices.Create(ctx, id, sep2.EndDevice{LFDI: lfdi}); err != nil {
			t.Fatalf("seed EndDevice %q: %v", id, err)
		}
	}
	plant := func(id, href, path string) {
		t.Helper()
		sub := sep2.Subscription{SubscribedResource: "/edev/5/fsa", NotificationURI: rcv.URL + path, Limit: 1}
		sub.Href = href
		if err := stores.Subscriptions.Create(ctx, id, sub); err != nil {
			t.Fatalf("insert subscription %q: %v", id, err)
		}
	}
	plant("cross", "/edev/3/sub/cross", "/cross-device")
	plant("own", "/edev/5/sub/own", "/own")

	mgr := coresub.NewManager(stores.Subscriptions, 2, 16, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(runCtx) }()
	t.Cleanup(func() { stop(); <-done })

	server.BuildProtocolRouter(&config.Config{}, stores, nil, "", "", mgr)
	mgr.Notify(ctx, "/edev/5/fsa", sep2.NotificationStatusDefault)

	select {
	case p := <-paths:
		if p != "/own" {
			t.Fatalf("first delivery went to %q, want /own", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the owner's subscription was not notified within 5s")
	}
	select {
	case p := <-paths:
		t.Errorf("a second delivery went to %q; the cross-device subscription must not be notified", p)
	case <-time.After(300 * time.Millisecond):
	}
}
