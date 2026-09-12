package subscription

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
// already refuses before reaching it, so it is exercised directly here.
func TestGuardDialerControlChecksConnectedAddress(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	firstPeer := make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case firstPeer <- c.RemoteAddr().String():
			default:
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
	defer func() { _ = conn.Close() }()

	// The accept queue is FIFO: had the refused dial connected, its peer
	// would be accepted before this one.
	select {
	case peer := <-firstPeer:
		if peer != conn.LocalAddr().String() {
			t.Fatalf("first accepted peer = %s, want the AllowLoopback dial %s", peer, conn.LocalAddr())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener accepted nothing")
	}
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
