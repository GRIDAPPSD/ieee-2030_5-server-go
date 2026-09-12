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
// resolved address the DestinationPolicy does not permit.
var ErrRefusedDestination = errors.New("notification destination refused")

// ErrDestinationUnresolved reports a notificationURI host that could not be
// resolved. It is kept apart from ErrRefusedDestination so a resolver outage
// is not mistaken for a policy refusal; both fail closed.
var ErrDestinationUnresolved = errors.New("notification destination could not be resolved")

// errRedirectRefused is returned for a 3xx from a notification receiver.
var errRedirectRefused = fmt.Errorf("%w: receiver redirected; redirects are not followed", ErrRefusedDestination)

// creationResolveTimeout bounds the lookup a Subscription POST performs
// before anything is stored.
const creationResolveTimeout = 5 * time.Second

// minAddressDialTimeout is the least connect time one resolved address gets
// while that much time remains, matching net.Dialer.
const minAddressDialTimeout = 2 * time.Second

// nat64Prefixes are the well-known (RFC 6052) and local-use (RFC 8215) NAT64
// prefixes. Network-specific prefixes cannot be listed statically.
var nat64Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// metadataAddrs are cloud metadata services outside the link-local ranges:
// the AWS and GCP IPv6 endpoints and Alibaba Cloud.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("fd00:ec2::254"):   true,
	netip.MustParseAddr("fd20:ce::254"):    true,
	netip.MustParseAddr("100.100.100.200"): true,
}

// DestinationPolicy decides which addresses the server may send a
// notification to. The zero value is the production default: it refuses
// loopback, link-local (which includes cloud metadata at 169.254.169.254),
// unspecified or 0.0.0.0/8, link-local and interface-local multicast, and the
// cloud metadata addresses in metadataAddrs, including the IPv4-mapped,
// IPv4-compatible, and NAT64 IPv6 forms of those IPv4 addresses. RFC 1918 and
// ULA private ranges are allowed because 2030.5 devices commonly sit on them.
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
	case ip.IsLinkLocalUnicast():
		return fmt.Errorf("%w: %s is link-local", ErrRefusedDestination, ip)
	case ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is link-local or interface-local multicast", ErrRefusedDestination, ip)
	case metadataAddrs[ip]:
		return fmt.Errorf("%w: %s is a cloud metadata address", ErrRefusedDestination, ip)
	}
	return nil
}

// embeddedIPv4 returns the IPv4 address in the low 32 bits of an
// IPv4-compatible (::a.b.c.d) or NAT64 address. :: and ::1 are native IPv6
// addresses, not embeddings of 0.0.0.0 and 0.0.0.1.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() || ip == netip.IPv6Unspecified() || ip == netip.IPv6Loopback() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	embedded := [12]byte(b[:12]) == [12]byte{}
	for _, p := range nat64Prefixes {
		embedded = embedded || p.Contains(ip)
	}
	if !embedded {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte(b[12:])), true
}

// redactURI renders a notificationURI for logs and errors with the parts that
// can carry credentials replaced: userinfo, query values (names are kept), and
// the fragment.
func redactURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URI)"
	}
	if u.Opaque != "" {
		return u.Scheme + ":(opaque)"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			u.RawQuery = "redacted"
		} else {
			for name := range q {
				q[name] = []string{"redacted"}
			}
			u.RawQuery = q.Encode()
		}
	}
	if u.Fragment != "" || u.RawFragment != "" {
		u.Fragment, u.RawFragment = "redacted", ""
	}
	return u.String()
}

// withoutURL drops a *url.Error wrapper, whose message repeats the URL and
// any userinfo in it.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// destinationGuard applies a DestinationPolicy to creation-time URIs and to
// every outbound delivery connection.
type destinationGuard struct {
	policy      DestinationPolicy
	lookup      func(ctx context.Context, host string) ([]netip.Addr, error)
	dial        func(ctx context.Context, network, address string) (net.Conn, error)
	dialTimeout time.Duration
	// resolveTimeout bounds the lookup performed at creation.
	resolveTimeout time.Duration
}

func newDestinationGuard(p DestinationPolicy) *destinationGuard {
	g := &destinationGuard{policy: p, lookup: systemLookup, dialTimeout: notificationClientTimeout, resolveTimeout: creationResolveTimeout}
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
		// The parse error can quote part of the URI, so it is not included.
		return fmt.Errorf("%w: unparseable URI", ErrRefusedDestination)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrRefusedDestination, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrRefusedDestination)
	}
	ctx, cancel := context.WithTimeout(ctx, g.resolveTimeout)
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
			return nil, fmt.Errorf("%w: resolve %q: %w", ErrDestinationUnresolved, host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrDestinationUnresolved, host)
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
//
// The transport detaches dial contexts from the request deadline, so the
// budget is dialTimeout, starting before resolution as net.Dialer.Timeout
// does. The lookup gets at most half of it, so a resolver that never answers
// ends the attempt as a resolution failure while the request still waits. The
// rest is split across the addresses as net.Dialer splits it, so an address
// that never answers cannot starve the ones after it.
func (g *destinationGuard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefusedDestination, err)
	}
	deadline := time.Now().Add(g.dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	lookupCtx, cancelLookup := context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
	addrs, err := g.checkHost(lookupCtx, host)
	cancelLookup()
	if err != nil {
		return nil, err
	}
	var errs []error
	for i, a := range addrs {
		addrCtx, cancel := context.WithDeadline(ctx, addressDeadline(time.Now(), deadline, len(addrs)-i))
		conn, err := g.dial(addrCtx, network, net.JoinHostPort(a.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// addressDeadline gives each remaining address an equal share of the time
// left, but at least minAddressDialTimeout while that much remains.
func addressDeadline(now, deadline time.Time, remaining int) time.Time {
	left := deadline.Sub(now)
	share := left / time.Duration(remaining)
	if share < minAddressDialTimeout {
		share = min(left, minAddressDialTimeout)
	}
	return now.Add(share)
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
func newNotificationClient(g *destinationGuard, base http.RoundTripper) *http.Client {
	tr := &http.Transport{}
	if b, ok := base.(*http.Transport); ok {
		tr = b.Clone()
	}
	// Clone copies every exported field. Clear the ones that decide where or
	// how a connection is made or verified, so changes an embedder made to the
	// base transport cannot route a connection around dialContext.
	tr.Proxy = nil
	tr.OnProxyConnectResponse = nil
	tr.ProxyConnectHeader = nil
	tr.GetProxyConnectHeader = nil
	tr.Dial = nil
	tr.DialTLS = nil
	tr.DialTLSContext = nil
	tr.TLSClientConfig = nil
	tr.TLSNextProto = nil
	tr.DialContext = g.dialContext
	return &http.Client{
		Timeout:   notificationClientTimeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectRefused
		},
	}
}
