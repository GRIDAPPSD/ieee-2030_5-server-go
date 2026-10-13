package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// A notification sent by boot-time recovery, before the protocol router is
// built, is held to the same subscriber read check as one sent later.
func TestRun_BootRecoveryNotifyAppliesTheSubscriberCheck(t *testing.T) {
	e := newFRRunEnv(t)
	paths := make(chan string, 8)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)

	ran := make(chan struct{})
	server.SetRecoverAtBoot(t, func(ctx context.Context, s *server.Stores, q *flowreservation.Queue, n flowreservation.Notifier, l *slog.Logger, now time.Time) (flowreservation.RecoverCounts, error) {
		defer close(ran)
		// "0" is the fixture's device; "5" belongs to another device.
		if err := s.EndDevices.Create(ctx, "5", sep2.EndDevice{LFDI: "0000000000000000000000000000000000000005"}); err != nil {
			t.Errorf("seed EndDevice 5: %v", err)
		}
		plant := func(id, href, path string) {
			sub := sep2.Subscription{SubscribedResource: "/edev/5/frp", NotificationURI: rcv.URL + path, Limit: 1}
			sub.Href = href
			if err := s.Subscriptions.Create(ctx, id, sub); err != nil {
				t.Errorf("insert subscription %q: %v", id, err)
			}
		}
		plant("cross", "/edev/0/sub/cross", "/cross-device")
		plant("own", "/edev/5/sub/own", "/own")
		n.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
		return server.RecoverFlowReservations(ctx, s, q, n, l, now)
	})

	e.start(t, e.config(0))
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the recovery hook never ran")
	}
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
		t.Errorf("a second delivery went to %q; the cross-device subscription must get 0 notifications", p)
	case <-time.After(300 * time.Millisecond):
	}
}
