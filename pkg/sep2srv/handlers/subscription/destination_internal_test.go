package subscription

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// loopbackReceivers opts a Manager into delivering to httptest servers, which
// listen on 127.0.0.1 and are refused by the default policy.
var loopbackReceivers = WithDestinationPolicy(DestinationPolicy{AllowLoopback: true})

func TestDestinationPolicyCheckAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr          string
		strict        bool // allowed under the zero-value policy
		allowLoopback bool // allowed with AllowLoopback set
	}{
		{"127.0.0.1", false, true},
		{"127.255.255.254", false, true},
		{"::1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"::127.0.0.1", false, true},
		{"64:ff9b::127.0.0.1", false, true},

		{"169.254.0.1", false, false},
		{"169.254.169.254", false, false},
		{"::ffff:169.254.169.254", false, false},
		{"::169.254.169.254", false, false},
		{"64:ff9b::169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"fe80::1%eth0", false, false},
		{"febf::1", false, false},
		{"224.0.0.251", false, false},
		{"ff02::1", false, false},
		{"ff01::1", false, false},
		{"ff01::fb", false, false},

		{"fd00:ec2::254", false, false},
		{"fd20:ce::254", false, false},
		{"100.100.100.200", false, false},
		{"::ffff:100.100.100.200", false, false},
		{"64:ff9b::100.100.100.200", false, false},
		{"fd00:ec2::253", true, true},
		{"100.100.100.201", true, true},

		{"64:ff9b:1::127.0.0.1", false, true},
		{"64:ff9b:1::169.254.169.254", false, false},
		{"64:ff9b:1::", false, false},
		{"64:ff9b:1::192.0.2.10", true, true},

		{"0.0.0.0", false, false},
		{"0.255.255.255", false, false},
		{"::", false, false},
		{"::ffff:0.0.0.0", false, false},
		{"::ffff:0.1.2.3", false, false},
		{"::0.1.2.3", false, false},
		{"64:ff9b::", false, false},
		{"64:ff9b::0.1.2.3", false, false},

		{"10.0.0.1", true, true},
		{"172.16.5.4", true, true},
		{"192.168.1.1", true, true},
		{"fd00::1", true, true},
		{"fc00::1", true, true},
		{"192.0.2.10", true, true},
		{"2001:db8::1", true, true},
		{"::ffff:10.0.0.1", true, true},
		{"::10.0.0.1", true, true},
		{"64:ff9b::192.0.2.10", true, true},
	}

	for _, tc := range tests {
		ip := netip.MustParseAddr(tc.addr)
		for _, p := range []struct {
			name    string
			policy  DestinationPolicy
			allowed bool
		}{
			{"default", DestinationPolicy{}, tc.strict},
			{"allow loopback", DestinationPolicy{AllowLoopback: true}, tc.allowLoopback},
		} {
			err := p.policy.checkAddr(ip)
			if p.allowed && err != nil {
				t.Errorf("%s policy: checkAddr(%s) = %v, want allowed", p.name, tc.addr, err)
			}
			if !p.allowed && !errors.Is(err, ErrRefusedDestination) {
				t.Errorf("%s policy: checkAddr(%s) = %v, want ErrRefusedDestination", p.name, tc.addr, err)
			}
		}
	}
}

// The dialer's Control hook is the last check before connect(2). dialContext
// already refuses before reaching it, so it is exercised directly here. Control
// runs before connect, so a refusal error means no connection was made; the
// listener only gives the permissive dial somewhere to connect.
func TestGuardDialerControlChecksConnectedAddress(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ctx := context.Background()
	strict := newDestinationGuard(DestinationPolicy{})
	if conn, err := strict.dial(ctx, "tcp", ln.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("default policy dialer connected to loopback, want refusal")
	} else if !errors.Is(err, ErrRefusedDestination) {
		t.Fatalf("default policy dialer error = %v, want ErrRefusedDestination", err)
	}

	permissive := newDestinationGuard(DestinationPolicy{AllowLoopback: true})
	conn, err := permissive.dial(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("AllowLoopback dialer: %v", err)
	}
	_ = conn.Close()
}

func TestDeliverClassifiesUnresolvedApartFromRefused(t *testing.T) {
	t.Parallel()

	m := NewManager(&mockSubStore{}, 1, 1)
	m.guard.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "loopback.test":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "empty.test":
			return nil, nil
		case "timeout.test":
			return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	m.guard.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial must not be reached")
	}

	for _, tc := range []struct {
		uri        string
		unresolved bool
	}{
		{"http://loopback.test:8080/n", false},
		{"http://127.0.0.1:8080/n", false},
		{"http://nxdomain.test:8080/n", true},
		{"http://timeout.test:8080/n", true},
		{"http://empty.test:8080/n", true},
	} {
		err := m.deliver(context.Background(), notificationTask{notificationURI: tc.uri, payload: []byte("<Notification/>")})
		refused, unresolved := errors.Is(err, ErrRefusedDestination), errors.Is(err, ErrDestinationUnresolved)
		if tc.unresolved && (!unresolved || refused) {
			t.Errorf("%s: err = %v; want ErrDestinationUnresolved and not ErrRefusedDestination", tc.uri, err)
		}
		if !tc.unresolved && (!refused || unresolved) {
			t.Errorf("%s: err = %v; want ErrRefusedDestination and not ErrDestinationUnresolved", tc.uri, err)
		}
	}
}

func TestDeliverRedirectIsARefusal(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://192.0.2.10/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	m := NewManager(&mockSubStore{}, 1, 1, loopbackReceivers)
	err := m.deliver(context.Background(), notificationTask{notificationURI: srv.URL, payload: []byte("<Notification/>")})
	if !errors.Is(err, ErrRefusedDestination) {
		t.Fatalf("deliver to a redirecting receiver: err = %v, want ErrRefusedDestination", err)
	}
}

// A resolver that never answers must not outlive the delivery attempt, and
// the failure stays a resolution failure rather than a client timeout.
func TestDeliverBoundsHungLookup(t *testing.T) {
	t.Parallel()

	const deliveryTimeout = time.Second
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	returned := make(chan struct{})
	var once sync.Once

	m := NewManager(&mockSubStore{}, 1, 1)
	m.client.Timeout = deliveryTimeout
	m.guard.dialTimeout = deliveryTimeout
	var lookupBudget atomic.Int64
	m.guard.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		defer once.Do(func() { close(returned) })
		if d, ok := ctx.Deadline(); ok {
			lookupBudget.Store(int64(time.Until(d)))
		}
		select {
		case <-ctx.Done():
			return nil, &net.DNSError{Err: ctx.Err().Error(), Name: host, IsTimeout: true, UnwrapErr: ctx.Err()}
		case <-release:
			return nil, errors.New("released by test cleanup")
		}
	}
	m.guard.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial must not be reached")
	}

	start := time.Now()
	err := m.deliver(context.Background(), notificationTask{notificationURI: "http://hang.test:8080/n", payload: []byte("<Notification/>")})
	elapsed := time.Since(start)

	if elapsed >= deliveryTimeout {
		t.Errorf("delivery attempt took %v, want it to end within the %v delivery timeout", elapsed, deliveryTimeout)
	}
	if !errors.Is(err, ErrDestinationUnresolved) || errors.Is(err, ErrRefusedDestination) {
		t.Errorf("err = %v; want ErrDestinationUnresolved and not ErrRefusedDestination", err)
	}
	select {
	case <-returned:
	default:
		t.Errorf("the lookup was still running when the delivery attempt returned after %v", elapsed)
	}
	// The lookup gets half the delivery timeout, leaving the rest to connect.
	if b := time.Duration(lookupBudget.Load()); b < 300*time.Millisecond || b > deliveryTimeout/2+50*time.Millisecond {
		t.Errorf("lookup budget = %v, want about half of the %v delivery timeout", b, deliveryTimeout)
	}
}

// Dial hooks or TLS settings an embedder put on the base transport must not
// reach the delivery client: for https, a DialTLSContext hook replaces
// DialContext and would skip the destination guard. Every dial path is counted
// at its own hook or at the guard's dial seam, so traffic from elsewhere on
// the loopback port cannot affect the result.
func TestDeliveryClientIgnoresInheritedDialHooks(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	var hookCalls atomic.Int32
	standIn := http.DefaultTransport.(*http.Transport).Clone()
	standIn.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		hookCalls.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	standIn.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		hookCalls.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	standIn.DialTLS = func(network, addr string) (net.Conn, error) {
		hookCalls.Add(1)
		return net.Dial(network, addr)
	}
	standIn.Dial = func(network, addr string) (net.Conn, error) {
		hookCalls.Add(1)
		return net.Dial(network, addr)
	}
	standIn.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	standIn.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{
		"inherited-proto": func(string, *tls.Conn) http.RoundTripper { return nil },
	}

	m := NewManager(&mockSubStore{}, 1, 1)
	m.client = newNotificationClient(m.guard, standIn)
	var guardDials atomic.Int32
	guardDial := m.guard.dial
	m.guard.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		guardDials.Add(1)
		return guardDial(ctx, network, address)
	}
	err = m.deliver(context.Background(), notificationTask{notificationURI: "https://" + ln.Addr().String() + "/n", payload: []byte("<Notification/>")})

	if !errors.Is(err, ErrRefusedDestination) {
		t.Errorf("https delivery to loopback: err = %v, want ErrRefusedDestination", err)
	}
	if n := hookCalls.Load(); n != 0 {
		t.Errorf("inherited dial hooks called %d times, want 0", n)
	}
	if n := guardDials.Load(); n != 0 {
		t.Errorf("guard dialed %d times for a refused destination, want 0", n)
	}
	tr := m.client.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("delivery client inherited InsecureSkipVerify from the base transport")
	}
	if _, ok := tr.TLSNextProto["inherited-proto"]; ok {
		t.Error("delivery client inherited a TLSNextProto handler from the base transport")
	}

	if base := http.DefaultTransport.(*http.Transport); base.DialTLSContext != nil || base.DialTLS != nil {
		t.Error("the test must not modify the process-wide http.DefaultTransport")
	}
}

// Creation resolves under creationResolveTimeout in the production wiring. The
// bound keeps a Subscription POST from holding its client for long.
func TestManagerCreationLookupUsesResolveTimeout(t *testing.T) {
	t.Parallel()

	if creationResolveTimeout <= 0 || creationResolveTimeout > 10*time.Second {
		t.Fatalf("creationResolveTimeout = %v, want a positive bound of at most 10s", creationResolveTimeout)
	}
	m := NewManager(&mockSubStore{}, 1, 1)
	var budget time.Duration
	var hasDeadline bool
	m.guard.lookup = func(ctx context.Context, _ string) ([]netip.Addr, error) {
		d, ok := ctx.Deadline()
		budget, hasDeadline = time.Until(d), ok
		return []netip.Addr{netip.MustParseAddr("192.0.2.10")}, nil
	}
	if err := m.ValidateNotificationURI(context.Background(), "http://allowed.test/n"); err != nil {
		t.Fatalf("ValidateNotificationURI: %v", err)
	}
	if !hasDeadline || budget <= creationResolveTimeout-time.Second || budget > creationResolveTimeout {
		t.Errorf("creation lookup budget = %v (deadline set: %v), want just under %v", budget, hasDeadline, creationResolveTimeout)
	}
}

// A query that url.ParseQuery rejects is replaced whole: its values cannot be
// told apart from its names.
func TestRedactURIUnparseableQuery(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://allowed.test/n?token=%zzs3cret",
		"http://allowed.test/n?token=s3cret;site=north",
	} {
		if got, want := redactURI(raw), "http://allowed.test/n?redacted"; got != want {
			t.Errorf("redactURI(%q) = %q, want %q", raw, got, want)
		}
	}
}
