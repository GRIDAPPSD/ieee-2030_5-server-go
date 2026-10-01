package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Notification of flow reservation response list subscribers (#669), and the
// short pollRate a list advertises while a request waits, through the
// assembled router.

type receivedNotification struct {
	Resource           string
	SubscribedResource string
	Status             uint8
}

// notificationReceiver is a subscriber's notificationURI endpoint.
type notificationReceiver struct {
	mu     sync.Mutex
	got    []receivedNotification
	seen   chan struct{}
	waited int
	srv    *httptest.Server
}

func newNotificationReceiver(t *testing.T) *notificationReceiver {
	t.Helper()
	r := &notificationReceiver{seen: make(chan struct{}, 64)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var n sep2.Notification
		raw, _ := io.ReadAll(req.Body)
		if err := xml.Unmarshal(raw, &n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !strings.Contains(string(raw), "<status>0</status>") {
			t.Errorf("wire body lacks <status>0</status>: %s", raw)
		}
		r.mu.Lock()
		r.got = append(r.got, receivedNotification{n.Href, n.SubscribedResource, n.Status})
		r.mu.Unlock()
		r.seen <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// waitFor blocks until n notifications have arrived in total, driven by the
// receiver's own signal rather than a poll.
func (r *notificationReceiver) waitFor(t *testing.T, n int) []receivedNotification {
	t.Helper()
	for r.waited < n {
		select {
		case <-r.seen:
			r.waited++
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d of %d notifications within 5s", r.waited, n)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedNotification(nil), r.got...)
}

// frpNotifyServer is a fully wired router whose notifier is a real
// subscription Manager over the stores' own subscription store.
func frpNotifyServer(t *testing.T, cfg assembly.RouterConfig, lister coresub.SubscriptionLister) (*httptest.Server, *assembly.Stores) {
	t.Helper()
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1", "deviceA", "deviceB")
	if lister == nil {
		lister = stores.Subscriptions
	}
	mgr := coresub.NewManager(lister, 2, 16, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	if cfg.FlowReservationDeadline == 0 {
		cfg.FlowReservationDeadline = time.Hour
	}
	handler, _ := assembly.BuildProtocolRouter(cfg, stores, testAuthPolicy(), testSFDI, testLFDI, mgr)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

func subscribeToResponseList(t *testing.T, srv *httptest.Server, edevID, notificationURI string) {
	t.Helper()
	body := mustMarshal(t, &sep2.Subscription{
		SubscribedResource: "/edev/" + edevID + "/frp",
		NotificationURI:    notificationURI,
		Limit:              10,
	})
	resp, err := srv.Client().Post(srv.URL+"/edev/"+edevID+"/sub", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST sub: %v", err)
	}
	_ = resp.Body.Close() // only the status is read
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST sub status = %d, want 201", resp.StatusCode)
	}
}

func TestFlowReservationNotify_SubscriberHearsGrantAndCancel(t *testing.T) {
	t.Parallel()
	rcv := newNotificationReceiver(t)
	srv, _ := frpNotifyServer(t, assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond}, nil)
	subscribeToResponseList(t, srv, "e1", rcv.srv.URL)

	href := postWindowRequest(t, srv, "e1")
	granted := rcv.waitFor(t, 1)
	if granted[0] != (receivedNotification{Resource: "/edev/e1/frp", SubscribedResource: "/edev/e1/frp", Status: sep2.NotificationStatusDefault}) {
		t.Fatalf("grant notification = %+v, want a change notification (status 0) on /edev/e1/frp", granted[0])
	}
	list := listResponses(t, srv, "e1")
	if len(list.FlowReservationResponse) != 1 || list.FlowReservationResponse[0].Interval.Duration != 900 {
		t.Fatalf("list after the grant notification = %+v, want the one 900 s grant", list.FlowReservationResponse)
	}

	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), time.Now().Unix())); got != http.StatusNoContent {
		t.Fatalf("PUT cancel status = %d, want 204", got)
	}
	all := rcv.waitFor(t, 2)
	if all[1] != granted[0] {
		t.Errorf("cancel notification = %+v, want %+v", all[1], granted[0])
	}
	served := listResponses(t, srv, "e1").FlowReservationResponse[0]
	if served.EventStatus == nil || served.EventStatus.CurrentStatus != 2 {
		t.Errorf("served status after the cancel notification = %+v, want Cancelled (2)", served.EventStatus)
	}
}

func TestFlowReservationNotify_PendingCancelNotifiesItsDenial(t *testing.T) {
	t.Parallel()
	rcv := newNotificationReceiver(t)
	srv, _ := frpNotifyServer(t, assembly.RouterConfig{}, nil)
	subscribeToResponseList(t, srv, "e1", rcv.srv.URL)
	href := postWindowRequest(t, srv, "e1")

	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), time.Now().Unix())); got != http.StatusNoContent {
		t.Fatalf("PUT cancel status = %d, want 204", got)
	}

	got := rcv.waitFor(t, 1)
	if got[0].Resource != "/edev/e1/frp" || got[0].Status != sep2.NotificationStatusDefault {
		t.Errorf("notification = %+v, want a change notification (status 0) on /edev/e1/frp", got[0])
	}
	if d := listResponses(t, srv, "e1").FlowReservationResponse[0].Interval.Duration; d != 0 {
		t.Errorf("response duration = %d, want the zero-duration denial", d)
	}
}

type failingLister struct{}

func (failingLister) ListByResource(context.Context, string) ([]memory.SubscriptionRecord, error) {
	return nil, errors.New("subscription store down")
}

func TestFlowReservationNotify_AFailedNotificationDoesNotFailTheRequest(t *testing.T) {
	t.Parallel()
	srv, _ := frpNotifyServer(t, assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond}, failingLister{})

	href := postWindowRequest(t, srv, "e1")
	granted := waitForFRPList(t, srv, "/edev/e1/frp", 1).FlowReservationResponse[0]
	if granted.Interval == nil || granted.Interval.Duration != 900 {
		t.Fatalf("grant = %+v, want the 900 s grant stored although notifying failed", granted.Interval)
	}
	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), time.Now().Unix())); got != http.StatusNoContent {
		t.Errorf("PUT cancel status = %d, want 204 although notifying failed", got)
	}
	served := listResponses(t, srv, "e1").FlowReservationResponse[0]
	if served.EventStatus == nil || served.EventStatus.CurrentStatus != 2 {
		t.Errorf("served status = %+v, want the cancel applied (2)", served.EventStatus)
	}
}

func TestFlowReservationNotify_AClientThatNeverSubscribesPollsTheSameGrant(t *testing.T) {
	t.Parallel()
	rcv := newNotificationReceiver(t)
	srv, _ := frpNotifyServer(t, assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond}, nil)
	subscribeToResponseList(t, srv, "e1", rcv.srv.URL)
	postWindowRequest(t, srv, "e1")
	rcv.waitFor(t, 1)
	notified := listResponses(t, srv, "e1").FlowReservationResponse[0]

	plain, _ := frqServer(t)
	postWindowRequest(t, plain, "e1")
	polled := waitForFRPList(t, plain, "/edev/e1/frp", 1).FlowReservationResponse[0]

	// mRID and href are minted per response, so the grant's terms are compared.
	if polled.Interval == nil || notified.Interval == nil || notified.EnergyAvailable == nil || notified.PowerAvailable == nil ||
		polled.EnergyAvailable == nil || polled.PowerAvailable == nil ||
		polled.Interval.Duration != notified.Interval.Duration ||
		*polled.EnergyAvailable != *notified.EnergyAvailable || *polled.PowerAvailable != *notified.PowerAvailable ||
		polled.Subject != notified.Subject {
		t.Errorf("polled grant %+v differs from the subscribed one %+v", polled, notified)
	}
}

func responseListPollRate(t *testing.T, srv *httptest.Server, edevID string) uint32 {
	t.Helper()
	return listResponses(t, srv, edevID).PollRate
}

func TestFlowReservationPollRate_ShortWhilePendingThenRegistered(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour, FlowReservationPendingPollRate: 7 * time.Second})

	if got := responseListPollRate(t, srv, "e1"); got != 900 {
		t.Errorf("pollRate with no request = %d, want the registered 900", got)
	}
	href := postWindowRequest(t, srv, "e1")
	if got := responseListPollRate(t, srv, "e1"); got != 7 {
		t.Errorf("pollRate while a request is pending = %d, want the configured 7", got)
	}
	if got := responseListPollRate(t, srv, "deviceA"); got != 900 {
		t.Errorf("another device's pollRate = %d, want 900: its list has nothing pending", got)
	}

	if got := putRequest(t, srv, href, cancelled(getRequest(t, srv, href), time.Now().Unix())); got != http.StatusNoContent {
		t.Fatalf("PUT cancel status = %d, want 204", got)
	}
	if got := responseListPollRate(t, srv, "e1"); got != 900 {
		t.Errorf("pollRate after the request was withdrawn = %d, want 900", got)
	}
}

func TestFlowReservationPollRate_DefaultsToThirtyWhilePending(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: time.Hour})
	postWindowRequest(t, srv, "e1")
	if got := responseListPollRate(t, srv, "e1"); got != 30 {
		t.Errorf("pollRate while pending with no setting = %d, want the default 30", got)
	}
}

func TestFlowReservationPollRate_RegisteredOnceTheFallbackAnswered(t *testing.T) {
	t.Parallel()
	srv, _ := frqServerWithConfig(t, assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond})
	postWindowRequest(t, srv, "e1")
	waitForFRPList(t, srv, "/edev/e1/frp", 1)
	if got := responseListPollRate(t, srv, "e1"); got != 900 {
		t.Errorf("pollRate after the deadline fallback answered = %d, want 900", got)
	}
}
