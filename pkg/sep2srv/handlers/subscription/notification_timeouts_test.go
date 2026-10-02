package subscription_test

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

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
