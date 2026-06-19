package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/obs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// metricByOutcome reads the current sep2_subscription_notifications_total
// value for one outcome label via the exposition handler. The obs counters
// are unexported, so the test goes through the public Handler() surface
// (the same path Prometheus scrapes) rather than poking the collector.
func metricByOutcome(t *testing.T, outcome string) float64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, req)
	return parseCounter(t, rec.Body.String(),
		`sep2_subscription_notifications_total{outcome="`+outcome+`"}`)
}

// TestNotifySuccessIncrementsCounter asserts a delivered notification moves
// the success counter (value assertion, not non-panic) — data-invariants.
func TestNotifySuccessIncrementsCounter(t *testing.T) {
	before := metricByOutcome(t, obs.OutcomeSuccess)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &mockSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/9/sub/1"},
		},
		SubscribedResource: "/edev/9",
		NotificationURI:    srv.URL + "/notify",
	}}}

	mgr := subscription.NewManager(store, 2, 10)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/9", sep2.NotificationStatusChanged)

	waitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeSuccess) >= before+1 })
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
	// dropped → queue_full increments at least once.
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
	store := &mockSubStore{subs: subs}

	mgr := subscription.NewManager(store, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/8", sep2.NotificationStatusChanged)

	waitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeQueueFull) >= before+1 })
}

// TestNotifyClientErrorIncrementsCounter asserts a receiver that returns a 4xx
// drives the OutcomeClientError counter (Dutch M2: the 4xx outcome was
// previously untested). A httptest receiver returns 400; Notify sets the
// record ID via the store's ListByResource, so deliver's 4xx branch fires
// errDeleteAfter4xx and the worker records OutcomeClientError.
func TestNotifyClientErrorIncrementsCounter(t *testing.T) {
	before := metricByOutcome(t, obs.OutcomeClientError)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // 400 → receiver rejects subscription
	}))
	defer srv.Close()

	store := &mockSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/7/sub/1"},
		},
		SubscribedResource: "/edev/7",
		NotificationURI:    srv.URL + "/notify",
	}}}

	mgr := subscription.NewManager(store, 2, 10)
	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Start(ctx)
	defer cancel()

	mgr.Notify(ctx, "/edev/7", sep2.NotificationStatusChanged)

	waitFor(t, func() bool { return metricByOutcome(t, obs.OutcomeClientError) >= before+1 })
}

func waitFor(t *testing.T, cond func() bool) {
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

// parseCounter pulls a single counter sample matching the given metric+label
// prefix out of Prometheus exposition text. Returns 0 if absent.
func parseCounter(t *testing.T, exposition, prefix string) float64 {
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
