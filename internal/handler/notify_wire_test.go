package handler_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// anyHrefLister subscribes one receiver to every resource href.
type anyHrefLister struct{ uri string }

func (l anyHrefLister) ListByResource(_ context.Context, href string) ([]memory.SubscriptionRecord, error) {
	var sub sep2.Subscription
	sub.Href = "/sub/1"
	sub.SubscribedResource = href
	sub.NotificationURI = l.uri
	return []memory.SubscriptionRecord{{ID: "s1", Subscription: sub}}, nil
}

// wireNotifier is a real subscription manager delivering to a local receiver;
// bodies returns the raw notification bodies received so far.
func wireNotifier(t *testing.T) (*coresub.Manager, func(want int) []string) {
	t.Helper()
	bodies := make(chan string, 16)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)
	mgr := coresub.NewManager(anyHrefLister{uri: rcv.URL}, 1, 16,
		coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	return mgr, func(want int) []string {
		t.Helper()
		var got []string
		for len(got) < want {
			select {
			case b := <-bodies:
				got = append(got, b)
			case <-time.After(3 * time.Second):
				t.Fatalf("received %d notifications, want %d", len(got), want)
			}
		}
		return got
	}
}

func requireStatusZeroOnWire(t *testing.T, bodies []string) {
	t.Helper()
	for _, b := range bodies {
		if !strings.Contains(b, "<status>0</status>") {
			t.Errorf("notification body lacks <status>0</status>: %s", b)
		}
	}
}

func TestDERControlNotificationsCarryStatusZeroOnWire(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	p := sep2.DERProgram{MRID: "LINKED", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/3/derp/8/derc"}}
	p.Href = "/edev/0/fsa/3/derp/8"
	if err := d.programs.Create(context.Background(), dcDevice, "8", p); err != nil {
		t.Fatal(err)
	}
	mgr, recv := wireNotifier(t)
	d.h.Notifier = mgr

	w := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/9/derp/8","type":"connect","startTime":%d,"durationSeconds":300}`, futureStart(60)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	bodies := recv(2)
	requireStatusZeroOnWire(t, bodies)
	if !strings.Contains(bodies[0]+bodies[1], "/edev/0/fsa/3/derp/8/derc") {
		t.Errorf("control list notification missing: %v", bodies)
	}
}
