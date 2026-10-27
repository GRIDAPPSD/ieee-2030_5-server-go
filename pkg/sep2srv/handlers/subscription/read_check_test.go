package subscription_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// allowAnyResource admits every subscribedResource, for tests about other
// parts of the create path.
func allowAnyResource(*http.Request, string) error { return nil }

func postSubscriptionWith(t *testing.T, h http.HandlerFunc, resource string) *httptest.ResponseRecorder {
	t.Helper()
	body := `<Subscription xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>` + resource +
		`</subscribedResource><encoding>0</encoding><level>+S1</level><limit>1</limit>` +
		`<notificationURI>http://192.0.2.1/n</notificationURI></Subscription>`
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/sub", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/edev/3/sub", strings.NewReader(body)))
	return rec
}

func TestHandleCreateSubscription_ReadCheckDecides(t *testing.T) {
	t.Parallel()
	allowURI := func(context.Context, string) error { return nil }
	cases := []struct {
		name    string
		check   subscription.ReadCheck
		want    int
		wantSub int
	}{
		{"admitted resource is stored", allowAnyResource, http.StatusCreated, 1},
		{"refused resource is 400 and not stored", func(*http.Request, string) error {
			return fmt.Errorf("%w: not-owner", subscription.ErrSubscribedResourceRefused)
		}, http.StatusBadRequest, 0},
		{"a check that cannot complete is 500 and not stored", func(*http.Request, string) error {
			return errors.New("store down")
		}, http.StatusInternalServerError, 0},
		{"no check wired refuses", nil, http.StatusInternalServerError, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := memory.NewSubscriptionStore()
			var gotResource string
			check := tc.check
			if check != nil {
				inner := check
				check = func(r *http.Request, resource string) error {
					gotResource = resource
					return inner(r, resource)
				}
			}
			rec := postSubscriptionWith(t, subscription.HandleCreateSubscription(store, allowURI, check), "/edev/5/frp")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if check != nil && gotResource != "/edev/5/frp" {
				t.Errorf("check saw resource %q, want the posted /edev/5/frp", gotResource)
			}
			stored, err := store.ListByResource(context.Background(), "/edev/5/frp")
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != tc.wantSub {
				t.Errorf("stored %d subscriptions, want %d", len(stored), tc.wantSub)
			}
		})
	}
}

func TestSubscriberEndDevice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		href string
		want string
		ok   bool
	}{
		{"/edev/3/sub/sub-1", "3", true},
		{"/edev/abc/sub/x", "abc", true},
		{"/edev//sub/x", "", false},
		{"/edev/3/sub/", "", false},
		{"/edev/3/sub/a/b", "", false},
		{"/edev/3/4/sub/x", "", false},
		{"/mup/3/sub/x", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := subscription.SubscriberEndDevice(tc.href)
		if got != tc.want || ok != tc.ok {
			t.Errorf("SubscriberEndDevice(%q) = %q, %v; want %q, %v", tc.href, got, ok, tc.want, tc.ok)
		}
	}
}

func TestManagerNotify_SubscriberCheckWithholdsDelivery(t *testing.T) {
	t.Parallel()
	hits := newDeliveries()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	sub := func(href, path string) sep2.Subscription {
		return sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: href}},
			SubscribedResource:   "/edev/5/frp",
			NotificationURI:      srv.URL + path,
		}
	}
	store := &mockSubStore{subs: []sep2.Subscription{sub("/edev/3/sub/1", "/denied"), sub("/edev/5/sub/1", "/admitted")}}
	mgr := subscription.NewManager(store, 1, 10, loopbackReceivers)
	var checked atomic.Int32
	mgr.SetSubscriberCheck(func(_ context.Context, s sep2.Subscription) error {
		checked.Add(1)
		if s.Href == "/edev/3/sub/1" {
			return fmt.Errorf("%w: not-owner", subscription.ErrSubscribedResourceRefused)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	mgr.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
	hits.waitFor(t, 1)
	// The admitted delivery and the withheld one were enqueued by the same
	// Notify call; give a wrongly enqueued second task time to land.
	time.Sleep(100 * time.Millisecond)
	if got := hits.paths(); len(got) != 1 || got[0] != "/admitted" {
		t.Errorf("deliveries = %v, want only /admitted", got)
	}
	if checked.Load() != 3 {
		t.Errorf("check ran %d times, want once per subscription at Notify and once more for the admitted one at send (3)", checked.Load())
	}
}

func TestManagerNotify_ClearedCheckDeliversToAll(t *testing.T) {
	t.Parallel()
	hits := newDeliveries()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	store := &mockSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/sub/1"}},
		SubscribedResource:   "/edev/5/frp",
		NotificationURI:      srv.URL + "/n",
	}}}
	mgr := subscription.NewManager(store, 1, 10, loopbackReceivers)
	mgr.SetSubscriberCheck(func(context.Context, sep2.Subscription) error { return errors.New("refuse") })
	mgr.SetSubscriberCheck(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	mgr.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
	hits.waitFor(t, 1)
}

// deliveries records the paths notifications were delivered to.
type deliveries struct {
	ch  chan string
	got []string
}

func newDeliveries() *deliveries { return &deliveries{ch: make(chan string, 16)} }

func (d *deliveries) add(p string) { d.ch <- p }

// waitFor blocks until n deliveries in total have arrived.
func (d *deliveries) waitFor(t *testing.T, n int) {
	t.Helper()
	for len(d.got) < n {
		select {
		case p := <-d.ch:
			d.got = append(d.got, p)
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d of %d deliveries within 5s", len(d.got), n)
		}
	}
}

// paths returns every delivery received so far.
func (d *deliveries) paths() []string {
	for {
		select {
		case p := <-d.ch:
			d.got = append(d.got, p)
		default:
			return d.got
		}
	}
}

func TestManagerNotify_CheckIsRepeatedAtSendTime(t *testing.T) {
	t.Parallel()
	hits := newDeliveries()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	store := &mockSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/sub/1"}},
		SubscribedResource:   "/edev/5/frp",
		NotificationURI:      srv.URL + "/n",
	}}}
	mgr := subscription.NewManager(store, 1, 10, loopbackReceivers)
	var refuse atomic.Bool
	mgr.SetSubscriberCheck(func(context.Context, sep2.Subscription) error {
		if refuse.Load() {
			return errors.New("paired since enqueue")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// Hold the single worker so the notification sits in the queue while the
	// check's answer changes.
	release := make(chan struct{})
	blocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(blocker.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if err := mgr.NotifyRemoved(ctx, sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/9/sub/1"}},
		SubscribedResource:   "/edev/9/frp",
		NotificationURI:      blocker.URL,
	}); err != nil {
		t.Fatalf("NotifyRemoved: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	mgr.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
	refuse.Store(true)
	close(release)

	time.Sleep(300 * time.Millisecond)
	if got := hits.paths(); len(got) != 0 {
		t.Errorf("deliveries = %v, want none: the check refused before send", got)
	}
}

func TestManagerNotify_RecordedWithholdIsNotLoggedAgain(t *testing.T) {
	store := &mockSubStore{subs: []sep2.Subscription{{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/sub/1"}},
		SubscribedResource:   "/edev/5/frp",
		NotificationURI:      "http://127.0.0.1:1/n",
	}}}
	mgr := subscription.NewManager(store, 1, 10, loopbackReceivers)
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	ctx := context.Background()
	mgr.SetSubscriberCheck(func(context.Context, sep2.Subscription) error {
		return fmt.Errorf("%w: refused", subscription.ErrWithholdRecorded)
	})
	for range 5 {
		mgr.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
	}
	if n := strings.Count(buf.String(), "not notifying subscription"); n != 0 {
		t.Errorf("recorded withholds wrote %d unbudgeted lines, want 0; log=%q", n, buf.String())
	}

	mgr.SetSubscriberCheck(func(context.Context, sep2.Subscription) error { return errors.New("plain refusal") })
	mgr.Notify(ctx, "/edev/5/frp", sep2.NotificationStatusDefault)
	if n := strings.Count(buf.String(), "not notifying subscription"); n != 1 {
		t.Errorf("control: unrecorded refusal wrote %d lines, want 1", n)
	}
}
