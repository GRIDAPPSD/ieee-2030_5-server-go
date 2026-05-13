package handler_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// errNotifyBoom is a sentinel returned by the recordingNotifier when the
// test wants to drive the best-effort failure branch in
// HandleDeleteSubscription.
var errNotifyBoom = errors.New("synthetic notifier failure")

// newRealManager starts a *subscription.Manager bound to t and returns
// it. Pike-rule: the manager has goroutines, so its lifetime is tied to
// a context whose cancel runs on t.Cleanup — no leaks across tests.
func newRealManager(t *testing.T, store subscription.SubscriptionLister) *subscription.Manager {
	t.Helper()
	mgr := subscription.NewManager(store, 1, 4)
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
		case <-time.After(2 * time.Second):
			t.Errorf("manager did not shut down in time")
		}
	})
	return mgr
}

// readAll is a tiny alias so the integration test file stays free of
// io imports; keeps each test file focused on its assertions.
func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 1<<20))
}

// unmarshalNotification parses an XML body into a sep2.Notification.
func unmarshalNotification(body []byte) (sep2.Notification, error) {
	var n sep2.Notification
	if err := xml.Unmarshal(body, &n); err != nil {
		return sep2.Notification{}, err
	}
	return n, nil
}

// timeoutAfter returns a channel that closes after secs seconds. Used
// to keep `select` arms readable in the integration test.
func timeoutAfter(secs int) <-chan time.Time {
	return time.After(time.Duration(secs) * time.Second)
}

// memorySubStoreFor returns a typed alias so the test file reads more
// naturally. (Package-internal; only the tests use it.)
//
//nolint:unused // retained for readability in adjacent test files
type memorySubStore = memory.SubscriptionStore
