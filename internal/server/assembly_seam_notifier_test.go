// package server (white-box): tests for the notifierAdapter forwarding
// behavior. These live in package server (not server_test) so they can
// access the unexported notifierAdapter type directly without exporting
// it as a test helper.
package server

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// fwdNotifier satisfies both handler.ResourceNotifier (via Notify) and
// notifyRemover (via NotifyRemoved). Used to verify that notifierAdapter
// forwards NotifyRemoved to the inner when the inner supports it.
type fwdNotifier struct {
	notifyCalled        bool
	notifyRemovedCalled bool
	notifyRemovedErr    error
	gotSub              sep2.Subscription
}

func (f *fwdNotifier) Notify(_ context.Context, _ string, _ uint8) {
	f.notifyCalled = true
}

func (f *fwdNotifier) NotifyRemoved(_ context.Context, sub sep2.Subscription) error {
	f.notifyRemovedCalled = true
	f.gotSub = sub
	return f.notifyRemovedErr
}

// noopNotifierBasic satisfies handler.ResourceNotifier only (no NotifyRemoved).
// Used to verify that notifierAdapter.NotifyRemoved is a safe nil-return
// no-op when the inner does NOT support NotifyRemoved.
type noopNotifierBasic struct{}

func (n *noopNotifierBasic) Notify(_ context.Context, _ string, _ uint8) {}

// TestNotifierAdapterNotifyRemovedForwards verifies that when the inner
// notifier implements notifyRemover, the adapter calls through to it and
// returns its error. This is the load-bearing path for CSIP V1.2 sec 11.6
// (subscription DELETE final Removed Notification) that was silently dropped
// before the adapter was fixed.
func TestNotifierAdapterNotifyRemovedForwards(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("test-notify-removed-error")
	inner := &fwdNotifier{notifyRemovedErr: wantErr}
	adapter := &notifierAdapter{inner: inner}

	sub := sep2.Subscription{SubscribedResource: "/edev/1/sub/42"}
	ctx := context.Background()

	gotErr := adapter.NotifyRemoved(ctx, sub)

	if !inner.notifyRemovedCalled {
		t.Error("NotifyRemoved was not forwarded to the inner notifier")
	}
	if inner.gotSub != sub {
		t.Errorf("NotifyRemoved: inner received sub %+v, want %+v", inner.gotSub, sub)
	}
	if gotErr != wantErr {
		t.Errorf("NotifyRemoved: got err=%v, want err=%v", gotErr, wantErr)
	}
}

// TestNotifierAdapterNotifyRemovedNoopWhenInnerUnsupported verifies that
// when the inner notifier does NOT implement notifyRemover, the adapter's
// NotifyRemoved is a safe no-op: it returns nil and does not panic.
// This matches the documented contract "safe for core which treats a nil
// return as no final notification."
func TestNotifierAdapterNotifyRemovedNoopWhenInnerUnsupported(t *testing.T) {
	t.Parallel()

	inner := &noopNotifierBasic{}
	adapter := &notifierAdapter{inner: inner}

	sub := sep2.Subscription{SubscribedResource: "/edev/1/sub/99"}
	ctx := context.Background()

	// Must not panic; must return nil.
	gotErr := adapter.NotifyRemoved(ctx, sub)
	if gotErr != nil {
		t.Errorf("NotifyRemoved: got err=%v, want nil (inner does not implement notifyRemover)", gotErr)
	}
}
