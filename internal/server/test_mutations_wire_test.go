//go:build csip_test_hooks

package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// wireAnyHref subscribes one receiver to every resource href.
type wireAnyHref struct{ uri string }

func (l wireAnyHref) ListByResource(_ context.Context, href string) ([]memory.SubscriptionRecord, error) {
	var sub sep2.Subscription
	sub.Href = "/sub/1"
	sub.SubscribedResource = href
	sub.NotificationURI = l.uri
	return []memory.SubscriptionRecord{{ID: "s1", Subscription: sub}}, nil
}

// routerWithWireManager builds the protocol router with a real subscription
// manager and returns a func that waits for one raw notification body.
func routerWithWireManager(t *testing.T) (http.Handler, *server.Stores, func() string) {
	t.Helper()
	t.Setenv(tmTokenEnv, tmTestToken)
	bodies := make(chan string, 16)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)
	mgr := coresub.NewManager(wireAnyHref{uri: rcv.URL}, 1, 16,
		coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	stores := newTestStores()
	h, _ := server.BuildProtocolRouter(&config.Config{}, stores, nil, "", "", mgr)
	return h, stores, func() string {
		t.Helper()
		select {
		case b := <-bodies:
			return b
		case <-time.After(3 * time.Second):
			t.Fatal("no notification within 3s")
			return ""
		}
	}
}

func TestDERControlAdd_NotificationStatusZeroOnWire(t *testing.T) {
	h, stores, recv := routerWithWireManager(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-7", "prog-9", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id": "edev-7", "fsa_id": "fsa-3", "der_program_id": "prog-9",
		"control_id": "ctl-42", "control": sep2.DERControl{},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if b := recv(); !strings.Contains(b, "<status>0</status>") {
		t.Errorf("wire body lacks <status>0</status>: %s", b)
	}
}

func TestStressNotify_DefaultStatusZeroOnWire(t *testing.T) {
	h, _, recv := routerWithWireManager(t)
	rr := postJSON(t, h, tmStressNotify, tmTestToken, map[string]any{"href": "/edev/42/fsa"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if b := recv(); !strings.Contains(b, "<status>0</status>") {
		t.Errorf("wire body lacks <status>0</status>: %s", b)
	}
}
