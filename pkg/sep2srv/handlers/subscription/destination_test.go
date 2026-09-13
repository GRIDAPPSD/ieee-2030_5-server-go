package subscription_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// destPort is the port every test notificationURI names. fakeNet routes
// IP:destPort to a local recordingServer, so a connection that must never
// happen lands somewhere countable rather than on the real network.
const destPort = "8080"

// refusedDestinations are refused at creation and at delivery. Hostnames
// resolve through fakeNet only.
var refusedDestinations = []struct{ name, host string }{
	{"ipv4 loopback", "127.0.0.1"},
	{"ipv4 loopback range", "127.9.9.9"},
	{"ipv6 loopback", "::1"},
	{"ipv4-mapped loopback", "::ffff:127.0.0.1"},
	{"ipv4-compatible loopback", "::127.0.0.1"},
	{"nat64 loopback", "64:ff9b::127.0.0.1"},
	{"ipv4 link-local metadata", "169.254.169.254"},
	{"ipv4-mapped link-local", "::ffff:169.254.169.254"},
	{"ipv4-compatible link-local", "::169.254.169.254"},
	{"nat64 link-local", "64:ff9b::169.254.169.254"},
	{"ipv6 link-local", "fe80::1"},
	{"ipv6 link-local with zone", "fe80::1%lo"},
	{"ipv4 unspecified", "0.0.0.0"},
	{"ipv4 this-network range", "0.1.2.3"},
	{"ipv6 unspecified", "::"},
	{"ipv4-mapped unspecified", "::ffff:0.0.0.0"},
	{"ipv4-compatible this-network", "::0.1.2.3"},
	{"nat64 unspecified", "64:ff9b::"},
	{"ipv4 link-local multicast", "224.0.0.251"},
	{"ipv6 link-local multicast", "ff02::1"},
	{"ipv6 interface-local multicast", "ff01::1"},
	{"aws ipv6 metadata", "fd00:ec2::254"},
	{"gcp ipv6 metadata", "fd20:ce::254"},
	{"alibaba metadata", "100.100.100.200"},
	{"ipv4-mapped alibaba metadata", "::ffff:100.100.100.200"},
	{"nat64 alibaba metadata", "64:ff9b::100.100.100.200"},
	{"local-use nat64 loopback", "64:ff9b:1::127.0.0.1"},
	{"local-use nat64 link-local", "64:ff9b:1::169.254.169.254"},
	{"local-use nat64 unspecified", "64:ff9b:1::"},
	{"hostname resolving to loopback", "loopback.test"},
	{"hostname resolving to link-local", "metadata.test"},
	{"hostname with one refused address", "mixed.test"},
}

// refusedIPs are every address a refusedDestinations entry dials if the
// guard fails to stop it.
var refusedIPs = []string{
	"127.0.0.1", "127.9.9.9", "::1", "::ffff:127.0.0.1", "::127.0.0.1", "64:ff9b::127.0.0.1",
	"169.254.169.254", "::ffff:169.254.169.254", "::169.254.169.254", "64:ff9b::169.254.169.254",
	"fe80::1", "fe80::1%lo", "0.0.0.0", "0.1.2.3", "::", "::ffff:0.0.0.0", "::0.1.2.3", "64:ff9b::",
	"224.0.0.251", "ff02::1", "ff01::1", "fd00:ec2::254", "fd20:ce::254", "100.100.100.200",
	"::ffff:100.100.100.200", "64:ff9b::100.100.100.200", "64:ff9b:1::127.0.0.1", "64:ff9b:1::169.254.169.254",
	"64:ff9b:1::", "192.0.2.11",
}

const allowedDial = "192.0.2.10:" + destPort

// loopbackReceivers opts a Manager into delivering to httptest servers, which
// listen on 127.0.0.1 and are refused by the default policy.
var loopbackReceivers = subscription.WithDestinationPolicy(subscription.DestinationPolicy{AllowLoopback: true})

func destURI(host string) string {
	if strings.Contains(host, ":") {
		host = "[" + strings.Replace(host, "%", "%25", 1) + "]"
	}
	return "http://" + host + ":" + destPort + "/n"
}

// recordingServer is an HTTP server that records the requests it serves. It
// listens on an in-memory pipe that only fakeNet can dial, so it has no
// loopback port: other processes and tests cannot reach it, and it takes no
// port another test might be about to bind.
type recordingServer struct {
	srv      *httptest.Server
	pipe     *pipeListener
	accepted atomic.Int32
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	requestURI string // origin-form when sent directly, absolute-form via a proxy
	body       []byte
}

func newRecordingServer(t *testing.T, respond http.HandlerFunc) *recordingServer {
	t.Helper()
	rs := &recordingServer{pipe: newPipeListener()}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		rs.mu.Lock()
		rs.requests = append(rs.requests, recordedRequest{requestURI: r.RequestURI, body: body})
		rs.mu.Unlock()
		if respond != nil {
			respond(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rs.srv = &httptest.Server{
		Listener: countingListener{Listener: rs.pipe, n: &rs.accepted},
		Config:   &http.Server{Handler: handler},
	}
	rs.srv.Start()
	t.Cleanup(rs.srv.Close)
	return rs
}

// acceptedConns counts connections the server accepted. Only fakeNet can
// connect, so every one was opened by the code under test.
func (rs *recordingServer) acceptedConns() int {
	return int(rs.accepted.Load())
}

type countingListener struct {
	net.Listener
	n *atomic.Int32
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

// pipeListener is an in-memory net.Listener whose connections come only from
// its dial method.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// dial returns the client end of a new in-memory connection to l.
func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
	case <-ctx.Done():
	}
	_ = client.Close()
	_ = server.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, net.ErrClosed
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// randomPath returns a notification path no other process can guess.
func randomPath() string {
	return "/notify/" + rand.Text()
}

func (rs *recordingServer) received() []recordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]recordedRequest(nil), rs.requests...)
}

// fakeNet stands in for DNS and the socket layer. Names resolve only from
// hosts. A dial to an address in routes gets an in-memory connection to that
// server; any other address is dialed for real with a short timeout.
type fakeNet struct {
	mu        sync.Mutex
	hosts     map[string][]netip.Addr
	lookupErr map[string]error
	routes    map[string]*recordingServer
	hang      map[string]bool
	budgets   map[string]time.Duration // time left on the dial context, per address
	dialed    []string

	hangLookup    map[string]bool
	lookupEntered chan string
}

func newFakeNet() *fakeNet {
	return &fakeNet{
		hosts:     map[string][]netip.Addr{},
		lookupErr: map[string]error{},
		routes:    map[string]*recordingServer{},
		hang:      map[string]bool{},
		budgets:   map[string]time.Duration{},

		hangLookup:    map[string]bool{},
		lookupEntered: make(chan string, 16),
	}
}

// hangLookupOn makes lookups of name block until their context ends, like a
// resolver that never answers. Each such lookup is announced on lookupEntered.
func (f *fakeNet) hangLookupOn(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangLookup[name] = true
}

func (f *fakeNet) setLookupErr(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookupErr[name] = err
}

// hangOn makes a dial to ip:port block until its context ends, like a
// blackholed address that drops SYNs.
func (f *fakeNet) hangOn(ip, port string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang[net.JoinHostPort(netip.MustParseAddr(ip).String(), port)] = true
}

func (f *fakeNet) budget(address string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.budgets[address]
	return b, ok
}

// standardNet resolves the shared test names, sends every refused address
// to sink, and sends allowed.test to receiver.
func standardNet(sink, receiver *recordingServer) *fakeNet {
	f := newFakeNet()
	f.setHost("allowed.test", "192.0.2.10")
	f.setHost("loopback.test", "127.0.0.1")
	f.setHost("metadata.test", "169.254.169.254")
	f.setHost("mixed.test", "192.0.2.11", "127.0.0.1")
	f.setHost("empty.test")
	f.setLookupErr("timeout.test", &net.DNSError{Err: "i/o timeout", Name: "timeout.test", IsTimeout: true})
	for _, ip := range refusedIPs {
		f.route(ip, destPort, sink)
	}
	f.route("192.0.2.10", destPort, receiver)
	return f
}

func (f *fakeNet) setHost(name string, addrs ...string) {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		parsed = append(parsed, netip.MustParseAddr(a))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[name] = parsed
}

func (f *fakeNet) route(ip, port string, rs *recordingServer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[net.JoinHostPort(netip.MustParseAddr(ip).String(), port)] = rs
}

func (f *fakeNet) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	if f.hangLookup[host] {
		f.mu.Unlock()
		select {
		case f.lookupEntered <- host:
		default:
		}
		<-ctx.Done()
		return nil, &net.DNSError{
			Err: ctx.Err().Error(), Name: host,
			IsTimeout: errors.Is(ctx.Err(), context.DeadlineExceeded), UnwrapErr: ctx.Err(),
		}
	}
	defer f.mu.Unlock()
	if err, ok := f.lookupErr[host]; ok {
		return nil, err
	}
	addrs, ok := f.hosts[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return append([]netip.Addr(nil), addrs...), nil
}

func (f *fakeNet) dial(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	f.dialed = append(f.dialed, address)
	if deadline, ok := ctx.Deadline(); ok {
		f.budgets[address] = time.Until(deadline)
	}
	hang := f.hang[address]
	rs, routed := f.routes[address]
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if routed {
		return rs.pipe.dial(ctx)
	}
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	return d.DialContext(ctx, network, address)
}

func (f *fakeNet) dials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

func newSeamedManager(t *testing.T, store subscription.SubscriptionLister, fn *fakeNet, opts ...subscription.ManagerOption) *subscription.Manager {
	t.Helper()
	mgr := subscription.NewManager(store, 1, 8, opts...)
	subscription.SetDestinationSeams(mgr, fn.lookup, fn.dial)
	return mgr
}

func runManager(t *testing.T, mgr *subscription.Manager) {
	t.Helper()
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
		case <-time.After(5 * time.Second):
			t.Errorf("manager did not shut down")
		}
	})
}

// postSubscription drives POST /edev/{id}/sub through a mux registered the
// way the protocol router registers it.
func postSubscription(t *testing.T, h http.HandlerFunc, edevID, resource, uri string) *httptest.ResponseRecorder {
	t.Helper()
	return serveSubscription(h, newSubscriptionRequest(t, context.Background(), edevID, resource, uri))
}

func newSubscriptionRequest(t *testing.T, ctx context.Context, edevID, resource, uri string) *http.Request {
	t.Helper()
	body, err := xml.Marshal(&sep2.Subscription{
		SubscribedResource: resource,
		NotificationURI:    uri,
		Encoding:           sep2.EncodingXML,
	})
	if err != nil {
		t.Fatalf("marshal Subscription: %v", err)
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/edev/"+edevID+"/sub", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/sep+xml")
	return req
}

func serveSubscription(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/sub", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func seedStored(t *testing.T, store *memory.SubscriptionStore, id, edevID, resource, uri string) {
	t.Helper()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/sub/" + id},
		},
		SubscribedResource: resource,
		NotificationURI:    uri,
	}
	if err := store.Create(context.Background(), id, sub); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func assertNothingStored(t *testing.T, store *memory.SubscriptionStore, edevID, resource string) {
	t.Helper()
	ctx := context.Background()
	byDevice, err := store.ListByDeviceWithIDs(ctx, edevID)
	if err != nil {
		t.Fatalf("ListByDeviceWithIDs: %v", err)
	}
	byResource, err := store.ListByResource(ctx, resource)
	if err != nil {
		t.Fatalf("ListByResource: %v", err)
	}
	if len(byDevice) != 0 || len(byResource) != 0 {
		t.Errorf("stored %d by device, %d by resource; want nothing stored", len(byDevice), len(byResource))
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateSubscriptionRefusesDestination(t *testing.T) {
	t.Parallel()

	type tcase struct{ name, uri string }
	cases := make([]tcase, 0, len(refusedDestinations)+8)
	for _, d := range refusedDestinations {
		cases = append(cases, tcase{d.name, destURI(d.host)})
	}
	cases = append(cases,
		tcase{"unresolvable hostname fails closed", "http://nxdomain.test:8080/n"},
		tcase{"resolver timeout fails closed", "http://timeout.test:8080/n"},
		tcase{"hostname with no addresses fails closed", "http://empty.test:8080/n"},
		tcase{"ftp scheme", "ftp://allowed.test:8080/n"},
		tcase{"file scheme", "file:///etc/passwd"},
		tcase{"javascript scheme", "javascript:alert(1)"},
		tcase{"empty", ""},
		tcase{"relative reference", "/n"},
		tcase{"missing host", "http:///n"},
		tcase{"unparseable", "http://[::1"},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := newRecordingServer(t, nil)
			receiver := newRecordingServer(t, nil)
			fn := standardNet(sink, receiver)
			store := memory.NewSubscriptionStore()
			mgr := newSeamedManager(t, store, fn)
			transportDials := subscription.CountTransportDials(mgr)

			rec := postSubscription(t, subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI), "1", "/edev/1/fsa", tc.uri)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("Location = %q, want none", loc)
			}
			assertNothingStored(t, store, "1", "/edev/1/fsa")

			// Notify the refused resource, then a control. Dials are counted at
			// the transport, before the delivery-time check, so a subscription
			// creation wrongly stored is caught here even though delivery would
			// still refuse to connect.
			seedStored(t, store, "control", "9", "/edev/9/fsa", destURI("allowed.test"))
			runManager(t, mgr)
			ctx := context.Background()
			mgr.Notify(ctx, "/edev/1/fsa", sep2.NotificationStatusChanged)
			mgr.Notify(ctx, "/edev/9/fsa", sep2.NotificationStatusChanged)
			waitUntil(t, "control delivery", func() bool { return len(receiver.received()) == 1 })

			if n := transportDials.Load(); n != 1 {
				t.Errorf("transport dial attempts = %d, want 1 (the control only)", n)
			}
			for _, a := range fn.dials() {
				if a != allowedDial {
					t.Errorf("dialed %s, want only %s", a, allowedDial)
				}
			}
			if n := sink.acceptedConns(); n != 0 {
				t.Errorf("sink accepted %d connections, want 0", n)
			}
		})
	}
}

func TestCreateSubscriptionNilValidatorAppliesDefaultPolicy(t *testing.T) {
	t.Parallel()

	store := memory.NewSubscriptionStore()
	h := subscription.HandleCreateSubscription(store, nil)

	if rec := postSubscription(t, h, "1", "/edev/1/fsa", "http://127.0.0.1:8080/n"); rec.Code != http.StatusBadRequest {
		t.Errorf("loopback status = %d, want 400", rec.Code)
	}
	assertNothingStored(t, store, "1", "/edev/1/fsa")

	if rec := postSubscription(t, h, "2", "/edev/2/fsa", "http://192.0.2.10:8080/n"); rec.Code != http.StatusCreated {
		t.Errorf("TEST-NET status = %d, want 201", rec.Code)
	}
}

// Control: allowed destinations, including private ranges, are stored as
// submitted and delivered.
func TestAllowedDestinationIsStoredAndDelivered(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, host, routeIP string }{
		{"hostname resolving to a public address", "allowed.test", "192.0.2.10"},
		{"rfc1918 private address", "10.1.2.3", "10.1.2.3"},
		{"ula private address", "fd00::1", "fd00::1"},
		{"nat64 of a public address", "64:ff9b::192.0.2.10", "64:ff9b::192.0.2.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receiver := newRecordingServer(t, nil)
			fn := newFakeNet()
			fn.setHost("allowed.test", "192.0.2.10")
			fn.route(tc.routeIP, destPort, receiver)
			store := memory.NewSubscriptionStore()
			mgr := newSeamedManager(t, store, fn)
			uri := destURI(tc.host)

			rec := postSubscription(t, subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI), "1", "/edev/1/fsa", uri)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}
			loc := rec.Header().Get("Location")
			stored, err := store.ListByDeviceWithIDs(context.Background(), "1")
			if err != nil {
				t.Fatalf("ListByDeviceWithIDs: %v", err)
			}
			if len(stored) != 1 {
				t.Fatalf("stored %d subscriptions, want 1", len(stored))
			}
			if got := stored[0].Subscription.NotificationURI; got != uri {
				t.Errorf("stored NotificationURI = %q, want %q", got, uri)
			}
			if got := stored[0].Subscription.Href; got != loc {
				t.Errorf("stored Href = %q, want Location %q", got, loc)
			}

			runManager(t, mgr)
			mgr.Notify(context.Background(), "/edev/1/fsa", sep2.NotificationStatusChanged)
			waitUntil(t, "delivery", func() bool { return len(receiver.received()) == 1 })

			got := receiver.received()[0]
			if got.requestURI != "/n" {
				t.Errorf("request URI = %q, want /n", got.requestURI)
			}
			var n sep2.Notification
			if err := xml.Unmarshal(got.body, &n); err != nil {
				t.Fatalf("unmarshal Notification: %v", err)
			}
			if n.SubscriptionURI != loc || n.SubscribedResource != "/edev/1/fsa" {
				t.Errorf("Notification SubscriptionURI=%q SubscribedResource=%q, want %q and /edev/1/fsa",
					n.SubscriptionURI, n.SubscribedResource, loc)
			}
		})
	}
}

// Delivery re-checks stored URIs, such as records persisted before
// validation existed. A later subscription on the same worker still delivers.
func TestDeliveryRefusesDestination(t *testing.T) {
	t.Parallel()

	for _, d := range refusedDestinations {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			sink := newRecordingServer(t, nil)
			receiver := newRecordingServer(t, nil)
			fn := standardNet(sink, receiver)
			store := memory.NewSubscriptionStore()
			seedStored(t, store, "refused", "1", "/edev/1/fsa", destURI(d.host))
			seedStored(t, store, "control", "2", "/edev/2/fsa", destURI("allowed.test"))
			mgr := newSeamedManager(t, store, fn)
			runManager(t, mgr)

			ctx := context.Background()
			mgr.Notify(ctx, "/edev/1/fsa", sep2.NotificationStatusChanged)
			mgr.Notify(ctx, "/edev/2/fsa", sep2.NotificationStatusChanged)
			// One worker drains the queue in order, so the refused task is
			// finished once the control arrives.
			waitUntil(t, "control delivery", func() bool { return len(receiver.received()) == 1 })

			if n := sink.acceptedConns(); n != 0 {
				t.Errorf("refused destination accepted %d connections, want 0", n)
			}
			for _, a := range fn.dials() {
				if a != allowedDial {
					t.Errorf("dialed %s, want only %s", a, allowedDial)
				}
			}
			if _, err := store.Store.Get(ctx, "refused"); err != nil {
				t.Errorf("refused subscription was removed (%v); refusal must not delete", err)
			}
		})
	}
}

func TestDeliveryRechecksHostnameResolvedAfterCreation(t *testing.T) {
	t.Parallel()

	sink := newRecordingServer(t, nil)
	receiver := newRecordingServer(t, nil)
	fn := standardNet(sink, receiver)
	fn.setHost("rebind.test", "192.0.2.20")
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn)

	rec := postSubscription(t, subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI), "1", "/edev/1/fsa", destURI("rebind.test"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create while rebind.test is allowed: status = %d, want 201", rec.Code)
	}
	seedStored(t, store, "control", "2", "/edev/2/fsa", destURI("allowed.test"))

	fn.setHost("rebind.test", "127.0.0.1")
	runManager(t, mgr)
	ctx := context.Background()
	mgr.Notify(ctx, "/edev/1/fsa", sep2.NotificationStatusChanged)
	mgr.Notify(ctx, "/edev/2/fsa", sep2.NotificationStatusChanged)
	waitUntil(t, "control delivery", func() bool { return len(receiver.received()) == 1 })

	if n := sink.acceptedConns(); n != 0 {
		t.Errorf("rebound loopback destination accepted %d connections, want 0", n)
	}
	for _, a := range fn.dials() {
		if a != allowedDial {
			t.Errorf("dialed %s, want only %s", a, allowedDial)
		}
	}
}

func TestDeliveryDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	loopbackSink := newRecordingServer(t, nil)
	secondHop := newRecordingServer(t, nil)
	receiver := newRecordingServer(t, nil)
	origin := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		target := "http://second.test:8080/n"
		if r.URL.Path == "/to-loopback" {
			target = "http://127.0.0.1:8080/n"
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})

	fn := newFakeNet()
	fn.setHost("origin.test", "192.0.2.30")
	fn.route("192.0.2.30", destPort, origin)
	fn.setHost("second.test", "192.0.2.40")
	fn.route("192.0.2.40", destPort, secondHop)
	fn.route("127.0.0.1", destPort, loopbackSink)
	fn.setHost("allowed.test", "192.0.2.10")
	fn.route("192.0.2.10", destPort, receiver)

	store := memory.NewSubscriptionStore()
	seedStored(t, store, "to-loopback", "1", "/edev/1/fsa", "http://origin.test:8080/to-loopback")
	seedStored(t, store, "to-allowed", "2", "/edev/2/fsa", "http://origin.test:8080/to-allowed")
	seedStored(t, store, "control", "3", "/edev/3/fsa", destURI("allowed.test"))
	mgr := newSeamedManager(t, store, fn)
	runManager(t, mgr)

	ctx := context.Background()
	for _, res := range []string{"/edev/1/fsa", "/edev/2/fsa", "/edev/3/fsa"} {
		mgr.Notify(ctx, res, sep2.NotificationStatusChanged)
	}
	waitUntil(t, "control delivery", func() bool { return len(receiver.received()) == 1 })

	if n := len(origin.received()); n != 2 {
		t.Errorf("origin received %d requests, want 2", n)
	}
	if n := loopbackSink.acceptedConns(); n != 0 {
		t.Errorf("redirect to loopback accepted %d connections, want 0", n)
	}
	if n := secondHop.acceptedConns(); n != 0 {
		t.Errorf("redirect to an allowed host accepted %d connections, want 0 (redirects are not followed)", n)
	}
	for _, id := range []string{"to-loopback", "to-allowed"} {
		if _, err := store.Store.Get(ctx, id); err != nil {
			t.Errorf("subscription %s removed after redirect (%v), want kept", id, err)
		}
	}
}

const (
	proxyChildEnv  = "SUBSCRIPTION_PROXY_TEST_CHILD"
	proxyChildDone = "proxy child assertions complete"
)

func TestDeliveryIgnoresProxyEnvironment(t *testing.T) {
	if os.Getenv(proxyChildEnv) != "1" {
		// net/http reads the proxy variables once per process, and earlier
		// tests have already triggered that read, so assert in a fresh process.
		t.Setenv("HTTP_PROXY", "http://proxy.test:3128")
		t.Setenv("HTTPS_PROXY", "http://proxy.test:3128")
		t.Setenv("NO_PROXY", "")
		t.Setenv(proxyChildEnv, "1")
		out, err := exec.Command(os.Args[0], "-test.run=^TestDeliveryIgnoresProxyEnvironment$", "-test.v=true").CombinedOutput()
		if err != nil {
			t.Fatalf("proxy child failed: %v\n%s", err, out)
		}
		if !bytes.Contains(out, []byte(proxyChildDone)) {
			t.Fatalf("proxy child did not reach its assertions:\n%s", out)
		}
		return
	}

	proxy := newRecordingServer(t, nil)
	sink := newRecordingServer(t, nil)
	receiver := newRecordingServer(t, nil)
	fn := newFakeNet()
	fn.setHost("proxy.test", "192.0.2.99")
	fn.route("192.0.2.99", "3128", proxy)
	fn.setHost("allowed.test", "192.0.2.10")
	fn.route("192.0.2.10", destPort, receiver)
	fn.setHost("loopback.test", "127.0.0.1")
	fn.route("127.0.0.1", destPort, sink)

	store := memory.NewSubscriptionStore()
	seedStored(t, store, "refused", "1", "/edev/1/fsa", destURI("loopback.test"))
	seedStored(t, store, "control", "2", "/edev/2/fsa", destURI("allowed.test"))
	mgr := newSeamedManager(t, store, fn)
	runManager(t, mgr)

	ctx := context.Background()
	mgr.Notify(ctx, "/edev/1/fsa", sep2.NotificationStatusChanged)
	mgr.Notify(ctx, "/edev/2/fsa", sep2.NotificationStatusChanged)
	waitUntil(t, "direct control delivery", func() bool { return len(receiver.received()) == 1 })

	if n := proxy.acceptedConns(); n != 0 {
		t.Errorf("proxy accepted %d connections, want 0", n)
	}
	if n := sink.acceptedConns(); n != 0 {
		t.Errorf("loopback destination accepted %d connections, want 0", n)
	}
	if got := receiver.received()[0].requestURI; got != "/n" {
		t.Errorf("receiver request URI = %q, want origin-form /n", got)
	}
	t.Log(proxyChildDone)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Not parallel: it swaps the process-wide log output.
func TestRefusalsAreLogged(t *testing.T) {
	var logs syncBuffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	sink := newRecordingServer(t, nil)
	receiver := newRecordingServer(t, nil)
	fn := standardNet(sink, receiver)
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn)

	uri := destURI("loopback.test")
	if rec := postSubscription(t, subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI), "1", "/edev/1/fsa", uri); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if want := `subscription: refused notificationURI "` + uri + `"`; !strings.Contains(logs.String(), want) {
		t.Errorf("log missing %q; got:\n%s", want, logs.String())
	}

	origin := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:8080/n", http.StatusTemporaryRedirect)
	})
	fn.setHost("origin.test", "192.0.2.30")
	fn.route("192.0.2.30", destPort, origin)
	unresolvedURI := destURI("nxdomain.test")
	redirectURI := "http://origin.test:8080/r"

	seedStored(t, store, "refused", "1", "/edev/1/fsa", uri)
	seedStored(t, store, "unresolved", "3", "/edev/3/fsa", unresolvedURI)
	seedStored(t, store, "redirect", "4", "/edev/4/fsa", redirectURI)
	seedStored(t, store, "control", "2", "/edev/2/fsa", destURI("allowed.test"))
	runManager(t, mgr)
	ctx := context.Background()
	for _, res := range []string{"/edev/1/fsa", "/edev/3/fsa", "/edev/4/fsa", "/edev/2/fsa"} {
		mgr.Notify(ctx, res, sep2.NotificationStatusChanged)
	}
	waitUntil(t, "control delivery", func() bool { return len(receiver.received()) == 1 })

	got := logs.String()
	for _, want := range []string{
		`notification: refused destination "` + uri + `" for subscription "refused"`,
		`notification: cannot resolve destination "` + unresolvedURI + `" for subscription "unresolved"`,
		`notification: refused destination "` + redirectURI + `" for subscription "redirect"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q; got:\n%s", want, got)
		}
	}
	if bad := `refused destination "` + unresolvedURI + `"`; strings.Contains(got, bad) {
		t.Errorf("resolution failure logged as a policy refusal: %q", bad)
	}
}

// AllowLoopback admits loopback only; link-local stays refused.
func TestAllowLoopbackPolicyAdmitsOnlyLoopback(t *testing.T) {
	t.Parallel()

	sink := newRecordingServer(t, nil)
	fn := standardNet(sink, sink)
	store := memory.NewSubscriptionStore()
	mgr := newSeamedManager(t, store, fn, subscription.WithDestinationPolicy(subscription.DestinationPolicy{AllowLoopback: true}))
	h := subscription.HandleCreateSubscription(store, mgr.ValidateNotificationURI)

	if rec := postSubscription(t, h, "1", "/edev/1/fsa", destURI("loopback.test")); rec.Code != http.StatusCreated {
		t.Fatalf("loopback with AllowLoopback: status = %d, want 201", rec.Code)
	}
	if rec := postSubscription(t, h, "2", "/edev/2/fsa", destURI("metadata.test")); rec.Code != http.StatusBadRequest {
		t.Errorf("link-local with AllowLoopback: status = %d, want 400", rec.Code)
	}
	assertNothingStored(t, store, "2", "/edev/2/fsa")

	runManager(t, mgr)
	mgr.Notify(context.Background(), "/edev/1/fsa", sep2.NotificationStatusChanged)
	waitUntil(t, "loopback delivery", func() bool { return len(sink.received()) == 1 })
}
