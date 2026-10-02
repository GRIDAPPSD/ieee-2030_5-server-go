package subscription_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestNotificationTimeoutsDefaults(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]subscription.ManagerOption{
		"no option":  nil,
		"zero value": {subscription.WithNotificationTimeouts(subscription.NotificationTimeouts{})},
	} {
		m := subscription.NewManager(memory.NewSubscriptionStore(), 1, 1, opts...)
		post, dialer, budget, resolve := subscription.DeliveryTimeouts(m)
		if post != 30*time.Second || dialer != 30*time.Second || budget != 30*time.Second || resolve != 5*time.Second {
			t.Errorf("%s: post=%v dialer=%v budget=%v resolve=%v, want 30s 30s 30s 5s", name, post, dialer, budget, resolve)
		}
	}
}

func TestNotificationTimeoutsReachClientDialerAndResolve(t *testing.T) {
	t.Parallel()
	m := subscription.NewManager(memory.NewSubscriptionStore(), 1, 1,
		subscription.WithNotificationTimeouts(subscription.NotificationTimeouts{
			Post: 7 * time.Second, Dial: 3 * time.Second, CreationResolve: 900 * time.Millisecond,
		}))
	post, dialer, budget, resolve := subscription.DeliveryTimeouts(m)
	if post != 7*time.Second {
		t.Errorf("client timeout = %v, want 7s", post)
	}
	if dialer != 3*time.Second || budget != 3*time.Second {
		t.Errorf("dialer timeout = %v, dial budget = %v, want 3s both", dialer, budget)
	}
	if resolve != 900*time.Millisecond {
		t.Errorf("resolve timeout = %v, want 900ms", resolve)
	}

	var remaining time.Duration
	subscription.SetDestinationSeams(m,
		func(ctx context.Context, _ string) ([]netip.Addr, error) {
			d, ok := ctx.Deadline()
			if !ok {
				t.Error("creation resolve context has no deadline")
			}
			remaining = time.Until(d)
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		},
		func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed })
	if err := m.ValidateNotificationURI(context.Background(), "http://host.test/n"); err != nil {
		t.Fatalf("ValidateNotificationURI: %v", err)
	}
	if remaining <= 0 || remaining > 900*time.Millisecond {
		t.Errorf("resolve context deadline in %v, want within (0, 900ms]", remaining)
	}
}

func TestNotificationTimeoutsValidateNamesNegativeSetting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   subscription.NotificationTimeouts
		want string
	}{
		{"post", subscription.NotificationTimeouts{Post: -1}, "Post"},
		{"dial", subscription.NotificationTimeouts{Dial: -time.Second}, "Dial"},
		{"resolve", subscription.NotificationTimeouts{CreationResolve: -1}, "CreationResolve"},
	} {
		err := tc.in.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate() = %v, want an error naming %s", tc.name, err, tc.want)
		}
	}
	if err := (subscription.NotificationTimeouts{Post: time.Second}).Validate(); err != nil {
		t.Errorf("valid timeouts refused: %v", err)
	}
	if err := (subscription.NotificationTimeouts{}).Validate(); err != nil {
		t.Errorf("zero timeouts refused: %v", err)
	}
}

func TestNotificationTimeoutsNegativeLeavesDefaults(t *testing.T) {
	t.Parallel()
	m := subscription.NewManager(memory.NewSubscriptionStore(), 1, 1,
		subscription.WithNotificationTimeouts(subscription.NotificationTimeouts{Post: -1, Dial: -1, CreationResolve: -1}))
	post, dialer, budget, resolve := subscription.DeliveryTimeouts(m)
	if post != 30*time.Second || dialer != 30*time.Second || budget != 30*time.Second || resolve != 5*time.Second {
		t.Errorf("negative values changed defaults: %v %v %v %v", post, dialer, budget, resolve)
	}
}

// net/http detaches the dial from the request context, so a dial that
// outlives the POST timeout keeps running after the worker has moved on. The
// dial budget is therefore capped at the POST timeout.
func TestNotificationDialBudgetCappedAtPostTimeout(t *testing.T) {
	t.Parallel()

	const (
		post  = 200 * time.Millisecond
		dial  = 3 * time.Second
		count = 6
	)
	store := memory.NewSubscriptionStore()
	for i := range count {
		seedStored(t, store, fmt.Sprintf("s%d", i), "1", "/edev/1/fsa", destURI("blackhole.test"))
	}
	mgr := subscription.NewManager(store, 1, count*2,
		subscription.WithNotificationTimeouts(subscription.NotificationTimeouts{Post: post, Dial: dial}))

	var inFlight, peak, attempts atomic.Int32
	var firstBudget atomic.Int64
	subscription.SetDestinationSeams(mgr,
		func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		},
		func(ctx context.Context, _, _ string) (net.Conn, error) {
			if d, ok := ctx.Deadline(); ok {
				firstBudget.CompareAndSwap(0, int64(time.Until(d)))
			}
			attempts.Add(1)
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-ctx.Done()
			return nil, ctx.Err()
		})
	runManager(t, mgr)

	mgr.Notify(context.Background(), "/edev/1/fsa", sep2.NotificationStatusChanged)
	waitUntil(t, "every notification attempted", func() bool { return attempts.Load() == count })

	if got := peak.Load(); got > 2 {
		t.Errorf("peak in-flight dials = %d with one worker, want at most 2 (dial budget must not outlive the POST timeout)", got)
	}
	if b := time.Duration(firstBudget.Load()); b <= 0 || b > post {
		t.Errorf("first dial deadline in %v, want within (0, %v]", b, post)
	}
}

func TestNotificationTimeoutsCapDialAtPost(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		in         subscription.NotificationTimeouts
		wantBudget time.Duration
	}{
		"dial above post":         {subscription.NotificationTimeouts{Post: 2 * time.Second, Dial: 10 * time.Second}, 2 * time.Second},
		"post only below 30s":     {subscription.NotificationTimeouts{Post: 2 * time.Second}, 2 * time.Second},
		"dial below post":         {subscription.NotificationTimeouts{Post: 10 * time.Second, Dial: 3 * time.Second}, 3 * time.Second},
		"dial above default post": {subscription.NotificationTimeouts{Dial: 40 * time.Second}, 30 * time.Second},
		"dial only":               {subscription.NotificationTimeouts{Dial: 3 * time.Second}, 3 * time.Second},
		"post only above 30s":     {subscription.NotificationTimeouts{Post: time.Minute}, 30 * time.Second},
	} {
		m := subscription.NewManager(memory.NewSubscriptionStore(), 1, 1, subscription.WithNotificationTimeouts(tc.in))
		_, dialer, budget, _ := subscription.DeliveryTimeouts(m)
		if budget != tc.wantBudget || dialer != tc.wantBudget {
			t.Errorf("%s: dialer=%v budget=%v, want %v", name, dialer, budget, tc.wantBudget)
		}
	}
}
