package subscription

import (
	"context"
	"net"
	"net/netip"
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
