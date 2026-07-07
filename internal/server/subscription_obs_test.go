package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/obs"
)

// obsSubStore is a minimal SubscriptionLister for the obs counter tests.
// Using the real memory store avoids interface drift when SubscriptionLister
// gains methods.
type obsSubStore struct {
	subs []sep2.Subscription
}

func (s *obsSubStore) ListByResource(_ context.Context, href string) ([]memory.SubscriptionRecord, error) {
	var out []memory.SubscriptionRecord
	for i, sub := range s.subs {
		if sub.SubscribedResource == href {
			out = append(out, memory.SubscriptionRecord{
				ID:           fmt.Sprintf("obs-sub-%d", i),
				Subscription: sub,
			})
		}
	}
	return out, nil
}

// metricByOutcome reads the current sep2_subscription_notifications_total
// value for one outcome label via the exposition handler. The obs counters
// are unexported, so the test goes through the public Handler() surface
// (the same path Prometheus scrapes) rather than poking the collector.
func metricByOutcome(t *testing.T, outcome string) float64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, req)
	return parseObsCounter(t, rec.Body.String(),
		`sep2_subscription_notifications_total{outcome="`+outcome+`"}`)
}

// TestNotifySuccessIncrementsCounter asserts a delivered notification moves
// the success counter (value assertion, not non-panic) per data-invariants.
func TestNotifySuccessIncrementsCounter(t *testing.T) {
	before := metricByOutcome(t, obs.OutcomeSuccess)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &obsSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/9/sub/1"},
		},
		SubscribedResource: "/edev/9",
		NotificationURI:    srv.URL + "/notify",
	}}}

	mgr := coresub.NewManager(store, 2, 10)
	mgr.SetObserver(obs.RecordNotification)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/9", sep2.NotificationStatusChanged)

	obsWaitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeSuccess) >= before+1 })
}

// TestNotifyQueueFullIncrementsCounter asserts the queue-full drop path
// moves the queue_full counter. A zero-capacity-effective queue (size 1)
// plus a never-draining receiver guarantees the second enqueue drops.
func TestNotifyQueueFullIncrementsCounter(t *testing.T) {
	before := metricByOutcome(t, obs.OutcomeQueueFull)

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // hold the single worker so the queue fills
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(block)

	// Three subscriptions on the same resource, one worker, queue size 1:
	// worker grabs #1 and blocks on the receiver, #2 fills the queue, #3 is
	// dropped => queue_full increments at least once.
	subs := make([]sep2.Subscription, 3)
	for i := range subs {
		subs[i] = sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/8/sub"},
			},
			SubscribedResource: "/edev/8",
			NotificationURI:    srv.URL + "/notify",
		}
	}
	store := &obsSubStore{subs: subs}

	mgr := coresub.NewManager(store, 1, 1)
	mgr.SetObserver(obs.RecordNotification)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/8", sep2.NotificationStatusChanged)

	obsWaitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeQueueFull) >= before+1 })
}

// TestNotifyClientErrorIncrementsCounter asserts a receiver that returns a
// 4xx drives the OutcomeClientError counter. A httptest receiver returns
// 400; Notify sets the record ID via the store's ListByResource, so
// deliver's 4xx branch fires errDeleteAfter4xx and the worker records
// OutcomeClientError.
func TestNotifyClientErrorIncrementsCounter(t *testing.T) {
	before := metricByOutcome(t, obs.OutcomeClientError)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // 400 => receiver rejects subscription
	}))
	defer srv.Close()

	store := &obsSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/7/sub/1"},
		},
		SubscribedResource: "/edev/7",
		NotificationURI:    srv.URL + "/notify",
	}}}

	mgr := coresub.NewManager(store, 2, 10)
	mgr.SetObserver(obs.RecordNotification)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/7", sep2.NotificationStatusChanged)

	obsWaitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeClientError) >= before+1 })
}

func obsWaitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition not met within deadline")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// parseObsCounter pulls a single counter sample matching the given
// metric+label prefix out of Prometheus exposition text. Returns 0 if absent.
func parseObsCounter(t *testing.T, exposition, prefix string) float64 {
	t.Helper()
	for _, line := range strings.Split(exposition, "\n") {
		if line == "" || line[0] == '#' || !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parse counter value %q: %v", fields[len(fields)-1], err)
		}
		return v
	}
	return 0
}
