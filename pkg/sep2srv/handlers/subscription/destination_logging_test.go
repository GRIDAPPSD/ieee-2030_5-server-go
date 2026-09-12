package subscription_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// captureLog redirects the process-wide logger for the rest of t. Callers
// must not be parallel.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return logs
}

// Not parallel: it swaps the process-wide log output.
func TestLogsRedactNotificationURIUserinfo(t *testing.T) {
	logs := captureLog(t)

	const user, pass = "alice-user", "s3cret-pw"
	withUser := func(host string) string {
		return "http://" + user + ":" + pass + "@" + host + ":" + destPort + "/n"
	}

	sink := newRecordingServer(t, nil)
	control := newRecordingServer(t, nil)
	unavailable := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	gone := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	fn := standardNet(sink, control)
	fn.setHost("unavailable.test", "192.0.2.60")
	fn.route("192.0.2.60", destPort, unavailable)
	fn.setHost("gone.test", "192.0.2.61")
	fn.route("192.0.2.61", destPort, gone)
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn)
	h := subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI)

	for _, uri := range []string{
		withUser("loopback.test"),
		withUser("nxdomain.test"),
		"http://" + user + ":" + pass + "@[::1",
	} {
		if rec := postSubscription(t, h, "1", "/edev/1/fsa", uri); rec.Code != http.StatusBadRequest {
			t.Fatalf("create %q: status = %d, want 400", uri, rec.Code)
		}
	}

	seedStored(t, store, "refused", "2", "/edev/2/fsa", withUser("loopback.test"))
	seedStored(t, store, "unresolved", "3", "/edev/3/fsa", withUser("nxdomain.test"))
	seedStored(t, store, "unavailable", "4", "/edev/4/fsa", withUser("unavailable.test"))
	seedStored(t, store, "gone", "5", "/edev/5/fsa", withUser("gone.test"))
	seedStored(t, store, "control", "6", "/edev/6/fsa", destURI("allowed.test"))
	runManager(t, mgr)
	ctx := context.Background()
	for _, res := range []string{"/edev/2/fsa", "/edev/3/fsa", "/edev/4/fsa", "/edev/5/fsa", "/edev/6/fsa"} {
		mgr.Notify(ctx, res, sep2.NotificationStatusChanged)
	}
	waitUntil(t, "control delivery", func() bool { return len(control.received()) == 1 })

	// An unstarted Manager with room for one task drops the second.
	full := subscription.NewManager(store, 1, 1)
	full.Notify(ctx, "/edev/4/fsa", sep2.NotificationStatusChanged)
	full.Notify(ctx, "/edev/4/fsa", sep2.NotificationStatusChanged)

	seedStored(t, store, "removed", "7", "/edev/7/fsa", withUser("allowed.test"))
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}/sub/{subId}", subscription.HandleDeleteSubscription(store,
		func(context.Context, sep2.Subscription) error { return errors.New("synthetic notify failure") }))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/edev/7/sub/removed", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}

	got := logs.String()
	for _, want := range []string{
		"subscription: refused notificationURI",
		"subscription: could not resolve notificationURI",
		"notification: refused destination",
		"notification: cannot resolve destination",
		"notification: deliver to",
		"receiver returned 4xx",
		"notification: queue full",
		"subscription: notify removed for",
		"loopback.test:" + destPort + "/n",
		"unavailable.test:" + destPort + "/n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q; got:\n%s", want, got)
		}
	}
	for _, secret := range []string{pass, user} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains notificationURI userinfo %q:\n%s", secret, got)
		}
	}
}

// Not parallel: it swaps the process-wide log output.
func TestCreateSubscriptionNilValidatorLogsFallbackOnce(t *testing.T) {
	logs := captureLog(t)
	const marker = "no notificationURI validator wired"

	store := memory.NewSubscriptionStore()
	h := subscription.HandleCreateSubscription(store, nil)
	for range 2 {
		if rec := postSubscription(t, h, "1", "/edev/1/fsa", "http://127.0.0.1:8080/n"); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	}
	if n := strings.Count(logs.String(), marker); n != 1 {
		t.Errorf("fallback logged %d times, want once at construction; log:\n%s", n, logs.String())
	}

	mgr := subscription.NewManager(store, 1, 1)
	_ = subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI)
	if n := strings.Count(logs.String(), marker); n != 1 {
		t.Errorf("a wired validator logged the fallback (count now %d)", n)
	}
}
