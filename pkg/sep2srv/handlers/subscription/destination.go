package subscription

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrRefusedDestination reports a notificationURI whose scheme, host, or
// resolved address the DestinationPolicy does not permit, including a host
// that could not be resolved.
var ErrRefusedDestination = errors.New("notification destination refused")

// errRedirectRefused is returned for a 3xx from a notification receiver.
var errRedirectRefused = errors.New("notification receiver redirected; redirects are not followed")

// creationResolveTimeout bounds the lookup a Subscription POST performs
// before anything is stored.
const creationResolveTimeout = 5 * time.Second

var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// DestinationPolicy decides which addresses the server may send a
// notification to. The zero value is the production default: it refuses
// loopback, link-local (which includes cloud metadata at 169.254.169.254),
// and unspecified or 0.0.0.0/8 addresses, including their IPv4-mapped,
// IPv4-compatible, and NAT64 (64:ff9b::/96) IPv6 forms. RFC 1918 and ULA
// private ranges are allowed because 2030.5 devices commonly sit on them.
type DestinationPolicy struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1 for test harnesses whose
	// receivers listen on loopback. The server's admin listener is also on
	// loopback, so enabling this in production lets any client that can
	// create a subscription make the server POST to it.
	AllowLoopback bool
}

// ValidateNotificationURI reports whether uri is an acceptable notification
// destination under p, resolving its host with the system resolver.
func (p DestinationPolicy) ValidateNotificationURI(ctx context.Context, uri string) error {
	return newDestinationGuard(p).validateURI(ctx, uri)
}

func (p DestinationPolicy) checkAddr(ip netip.Addr) error {
	ip = ip.WithZone("").Unmap()
	if v4, ok := embeddedIPv4(ip); ok {
		ip = v4
	}
	switch {
	case !ip.IsValid():
		return fmt.Errorf("%w: invalid address", ErrRefusedDestination)
	case ip.IsLoopback():
		if p.AllowLoopback {
			return nil
		}
		return fmt.Errorf("%w: %s is loopback", ErrRefusedDestination, ip)
	case ip.IsUnspecified() || (ip.Is4() && ip.As4()[0] == 0):
		return fmt.Errorf("%w: %s is unspecified", ErrRefusedDestination, ip)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is link-local", ErrRefusedDestination, ip)
	}
	return nil
}

// embeddedIPv4 returns the IPv4 address inside an IPv4-compatible
// (::a.b.c.d) or NAT64 (64:ff9b::a.b.c.d) address. :: and ::1 are native
// IPv6 addresses, not embeddings of 0.0.0.0 and 0.0.0.1.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() || ip == netip.IPv6Unspecified() || ip == netip.IPv6Loopback() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	if nat64Prefix.Contains(ip) || [12]byte(b[:12]) == [12]byte{} {
		return netip.AddrFrom4([4]byte(b[12:])), true
	}
	return netip.Addr{}, false
}

// destinationGuard applies a DestinationPolicy to creation-time URIs and to
// every outbound delivery connection.
type destinationGuard struct {
	policy DestinationPolicy
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

func newDestinationGuard(p DestinationPolicy) *destinationGuard {
	g := &destinationGuard{policy: p, lookup: systemLookup}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
	g.dial = d.DialContext
	return g
}

func systemLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// validateURI requires an absolute http or https URI whose host resolves only
// to allowed addresses. A host that cannot be resolved is refused.
func (g *destinationGuard) validateURI(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRefusedDestination, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrRefusedDestination, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrRefusedDestination)
	}
	ctx, cancel := context.WithTimeout(ctx, creationResolveTimeout)
	defer cancel()
	_, err = g.checkHost(ctx, host)
	return err
}

// checkHost resolves host and refuses it when any address is refused, so a
// name that mixes allowed and refused answers is never partially trusted.
func (g *destinationGuard) checkHost(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = g.lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve %q: %w", ErrRefusedDestination, host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrRefusedDestination, host)
		}
	}
	for _, a := range addrs {
		if err := g.policy.checkAddr(a); err != nil {
			return nil, fmt.Errorf("host %q: %w", host, err)
		}
	}
	return addrs, nil
}

// dialContext resolves and checks the destination at connect time, so a name
// that resolved to an allowed address when the subscription was created
// cannot later be pointed at a refused one. It dials only the checked IPs.
func (g *destinationGuard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefusedDestination, err)
	}
	addrs, err := g.checkHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, a := range addrs {
		conn, err := g.dial(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// control re-checks the socket's peer address immediately before connect.
func (g *destinationGuard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRefusedDestination, err)
	}
	return g.policy.checkAddr(ap.Addr())
}

// newNotificationClient returns the delivery client. Its Timeout keeps a slow
// subscriber from pinning a worker. Proxy is nil because a proxy would make
// the proxy, not the subscriber, the address dialContext checks. Redirects
// are refused so a receiver cannot steer the POST to a second destination.
func newNotificationClient(g *destinationGuard) *http.Client {
	return &http.Client{
		Timeout: notificationClientTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           g.dialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectRefused
		},
	}
}
