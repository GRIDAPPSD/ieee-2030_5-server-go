package sep2server

import (
	"net"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Config configures an embedded IEEE 2030.5 protocol server.
//
// The zero value is not usable: [New] requires Addr, the three TLS file paths
// and a non-nil Auth.Wrap. Everything else has a documented zero-value
// meaning, and the zero value is the safe one in every case.
type Config struct {
	// Addr is the "host:port" the protocol listener binds. Required by
	// [New]; ":0" lets the OS assign a port, which [Server.Addr] reads back.
	// [BuildHandler] ignores it.
	Addr string

	// CertFile, KeyFile and CAFile name the server's leaf certificate and
	// key and the trusted client-CA bundle. All three are required by [New].
	// The server's own SFDI and LFDI are derived from the leaf, which is why
	// [Server.Identity] is only meaningful on a [Server] built by [New].
	CertFile string
	KeyFile  string
	CAFile   string

	// ExtraClientCAs names additional client-CA bundles trusted alongside
	// CAFile, for multi-root device-certificate trust.
	ExtraClientCAs []string

	// EnableCCM selects the CCM-8 mandatory cipher suite (IEEE 2030.5-2018
	// section 6.7) through core's forked crypto/tls. False (the zero value)
	// serves the stdlib GCM fallback, which is still mutual TLS: this knob
	// selects the cipher suite, not whether TLS is required.
	//
	// It also decides whether the handler chain carries core's CCM identity
	// middleware, which is what populates r.TLS from the forked connection.
	// See [BuildHandler] for where that lands in the chain.
	EnableCCM bool

	// Router carries the time-zone and DST scalars the /tm resource serves,
	// and the PostRateProvider behind POST /mup. The zero value is a valid
	// configuration: UTC, no DST, and no server-stated posting preference.
	//
	// Rate policy is deployment policy, not protocol: core ships no default
	// and neither does this package.
	Router assembly.RouterConfig

	// Stores are the resource stores the protocol handlers read and write.
	// Nil builds the pure in-memory set from [NewStores]; supply your own to
	// pre-seed a fleet or to use persistence-backed implementations.
	//
	// The same handle comes back from [Server.Stores], which is how a
	// consumer seeds and injects control after construction.
	Stores *assembly.Stores

	// Auth is the identity and ACL policy wrapped around the protocol mux.
	// [New] REFUSES a nil Auth.Wrap rather than serving without enforcement:
	// core treats nil as "no identity extraction and no ACL", which is a
	// fail-open posture no deployment should reach by omission. Pass
	// [DefaultAuthPolicy] to adopt this server's enforcement unchanged.
	//
	// [BuildHandler] does not enforce that, because it is the composition
	// primitive tests drive with pass-through stubs; it inherits core's own
	// warn-and-continue behaviour.
	Auth assembly.AuthPolicy

	// Notifier drives subscription fan-out on resource state changes. Nil is
	// core's documented sentinel for "no fan-out": handlers still serve and
	// still store, they simply do not notify.
	//
	// The notifier's worker pool is the consumer's to start and stop. This
	// package neither constructs nor runs one, because a pool's lifetime
	// belongs with whoever owns the context that bounds it.
	Notifier assembly.ResourceNotifier

	// Middleware wraps the protocol handler OUTERMOST, outside the CCM
	// identity middleware. It is the seam for instrumentation and for any
	// surface a consumer wants to mount in front of the protocol routes
	// without reaching into them. Nil applies nothing.
	//
	// See [BuildHandler] for the exact composition order.
	Middleware func(http.Handler) http.Handler

	// ConnState observes protocol-listener connection-state transitions. It
	// is chained ahead of any hook core's CCM setup installs, so both fire.
	// Nil installs nothing. The signature matches
	// [net/http.Server.ConnState] so a consumer can pass one straight
	// through.
	ConnState func(net.Conn, http.ConnState)

	// ShutdownTimeout bounds [Server.Run]'s graceful drain after its context
	// is cancelled. Zero (the zero value) drains without a bound, which is
	// what this server has always done; set a bound if a hung request must
	// not be able to hold the process open.
	ShutdownTimeout time.Duration

	// Capture, when non-nil, attaches traffic recording (#611) to the
	// protocol listener: [New] calls Capture.Attach after the listener and
	// handler are built and before [Server.Run] serves it. Nil (the zero
	// value) leaves the listener exactly as it was before #611: nothing is
	// allocated, opened or written. The caller owns Capture's lifetime
	// (construction and Close) and may attach the same Recorder to more
	// than one listener.
	Capture *sep2capture.Recorder
}
