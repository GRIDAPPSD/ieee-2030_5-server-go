package subscription

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"
)

// SetDestinationSeams replaces the resolver and the underlying dialer the
// Manager uses for creation checks and delivery. Call before Start.
func SetDestinationSeams(
	m *Manager,
	lookup func(ctx context.Context, host string) ([]netip.Addr, error),
	dial func(ctx context.Context, network, address string) (net.Conn, error),
) {
	m.guard.lookup = lookup
	m.guard.dial = dial
}

// SetDeliveryTimeout sets the per-delivery timeout and the connect budget
// shared across a host's addresses to d. Call before Start.
func SetDeliveryTimeout(m *Manager, d time.Duration) {
	m.client.Timeout = d
	m.guard.dialTimeout = d
}

// SetCreationResolveTimeout bounds the lookup ValidateNotificationURI
// performs. Call before any request.
func SetCreationResolveTimeout(m *Manager, d time.Duration) {
	m.guard.resolveTimeout = d
}

// CountTransportDials counts every connection the delivery transport asks
// for, before the destination policy decides. Call before Start.
func CountTransportDials(m *Manager) *atomic.Int32 {
	n := new(atomic.Int32)
	tr := m.client.Transport.(*http.Transport)
	inner := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		n.Add(1)
		return inner(ctx, network, address)
	}
	return n
}
