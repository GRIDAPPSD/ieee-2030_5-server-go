package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// Bounds on SEP2_FLOW_RESERVATION_DEADLINE_SECONDS. Zero in Config means
// unset and takes DefaultFlowReservationDeadline.
const (
	MinFlowReservationDeadline     = time.Second
	MaxFlowReservationDeadline     = time.Hour
	DefaultFlowReservationDeadline = 300 * time.Second
)

// Bounds on SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS. Zero in Config
// means unset and takes DefaultFlowReservationRetentionGrace. The floor is
// the 900 s poll rate the response list advertises: a shorter grace lets a
// client polling at that rate miss a chain's final state. The default is
// twice that rate.
const (
	MinFlowReservationRetentionGrace     = 900 * time.Second
	MaxFlowReservationRetentionGrace     = 7 * 24 * time.Hour
	DefaultFlowReservationRetentionGrace = 1800 * time.Second
)

// Bounds on SEP2_MIRROR_READING_RETENTION_SECONDS. The floor keeps every
// reading the DER control delivery figure can still use: the longest control
// the issuer accepts plus the furthest one reading reaches. Zero in Config
// means unset and takes DefaultMirrorReadingRetention.
const (
	MirrorReadingMaxHold          = 900 * time.Second
	MinMirrorReadingRetention     = dercontrol.DefaultMaxDuration + MirrorReadingMaxHold
	MaxMirrorReadingRetention     = 30 * 24 * time.Hour
	DefaultMirrorReadingRetention = 25 * time.Hour
)

// Bounds on SEP2_MIRROR_READING_MAX_PER_SERIES, the readings kept per mirror
// and mRID. The floor holds one reading every MirrorReadingCadence across
// MinMirrorReadingRetention, ends included, so the cap never removes a reading
// the time floor keeps at that rate. The ceiling is one reading a second
// across MaxMirrorReadingRetention. The default, over the default retention,
// is one reading every 4.5 s. Zero in Config means unset.
const (
	MirrorReadingCadence             = 300 * time.Second
	MinMirrorReadingMaxPerSeries     = int(MinMirrorReadingRetention/MirrorReadingCadence) + 1
	MaxMirrorReadingMaxPerSeries     = int(MaxMirrorReadingRetention / time.Second)
	DefaultMirrorReadingMaxPerSeries = 20000
)

// Config holds server configuration.
type Config struct {
	Addr            string // listen address for IEEE 2030.5 protocol (e.g., ":443")
	CertFile        string // server cert PEM path
	KeyFile         string // server key PEM path
	CAFile          string // CA cert PEM path (legacy single-CA setting; also the shared default below)
	BootFixtureFile string // optional YAML topology fixture loaded at startup; empty = no fixture

	// #622: the CA that signs THIS server's own certificates - the server
	// leaf minted at POST /api/certs/server, the admin operator cert, and
	// what GET /api/certs/ca hands an operator to verify this server. Empty
	// falls back to CAFile (EffectiveServingCA), so a deployment that never
	// sets this behaves exactly as it did before the split. Env: SEP2_SERVING_CA.
	ServingCAFile string

	// #622: the CA the protocol listener's ClientCAs pool trusts devices
	// against, and that signs minted device certs (POST /api/certs/device).
	// Empty falls back to CAFile (EffectiveDeviceCA). Env: SEP2_DEVICE_CA.
	DeviceCAFile string

	// ExtraClientCAs lists additional PEM paths appended to the ClientCAs
	// pool. Additive to the device CA (EffectiveDeviceCA), not the serving
	// one: this pool exists to verify DEVICE certs, and #622 does not move
	// its no-per-CA-scoping semantics.
	ExtraClientCAs []string

	// #165: persistence root for admin-mutated stores. Empty = pure
	// in-memory (back-compat with every test path that predates #165).
	// When set, each persistent store auto-files under <DataDir>/<name>.json
	// unless a store-specific dedicated path env var overrides it (see
	// EffectiveStorePath for the precedence rule).
	DataDir string // env SEP2_DATA_DIR

	// #165: dedicated path for the subscription store. Predates DataDir;
	// preserved for back-compat with #224-era deployments. When both are
	// set, the dedicated path wins.
	SubscriptionStorePath string // env SEP2_SUBSCRIPTION_STORE_PATH

	// #628 fix round 1: the permit gate. Capture is off unless this is
	// explicitly set, regardless of TrafficDir or DataDir: review found
	// capture turning itself on from DataDir alone, with no way off, on
	// every deployment that sets SEP2_DATA_DIR for persistence. Env:
	// SEP2_TRAFFIC_CAPTURE.
	TrafficCapture bool

	// #628 fix round 2: the raw SEP2_TRAFFIC_CAPTURE value, kept alongside
	// the parsed TrafficCapture bool so the disabled boot line can tell
	// "never set" apart from "set to something other than the exact
	// literal true" instead of reporting every off case as unset. Empty
	// means the variable was not set.
	TrafficCaptureEnv string

	// #611: dedicated directory for the traffic-capture segment log, read
	// only when TrafficCapture is true. Empty falls back to
	// <DataDir>/traffic; both empty means TrafficCapture had nothing to
	// permit. See EffectiveTrafficDir for the precedence rule.
	TrafficDir string // env SEP2_TRAFFIC_DIR

	// #161: admin listener configuration.
	//
	// The SEP2 protocol listener is RequireAnyClientCert + manual verify per
	// CSIP V1.2 (every device presents a cert). The admin listener
	// intentionally uses a weaker posture (VerifyClientCertIfGiven, or plain
	// HTTP behind Caddy) so browser logins can flow through
	// AdminAuthMiddleware's Bearer + cookie paths without weakening the SEP2
	// wire.
	//
	// AdminListen is the canonical knob (env SEP2_ADMIN_LISTEN). For
	// back-compat with pre-#161 deployments, an empty AdminListen falls
	// back to AdminAddr (env SEP2_ADMIN_ADDR). Use EffectiveAdminListen() to
	// resolve which value the server should bind to.
	AdminListen  string // admin listener address (e.g., ":9443"); empty = falls back to AdminAddr
	AdminAddr    string // DEPRECATED alias for AdminListen; preserved for back-compat
	AdminKey     string // Bearer token for admin API (empty = Bearer auth disabled)
	AdminTLS     bool   // true = HTTPS on admin listener; false = plain HTTP (Caddy mode)
	AdminCert    string // admin listener cert PEM path; empty + AdminTLS = self-signed
	AdminKeyFile string // admin listener key PEM path; required when AdminCert is set

	// #624: the trust anchor for the admin listener's client-certificate
	// verification (its ClientCAs pool). Empty falls back to the serving CA
	// (EffectiveServingCA), so a freshly minted operator cert needs no
	// client-side trust override to be admitted. #657: in the default
	// single-CA deployment (no ServingCAFile/DeviceCAFile split) that same
	// CA also signs every device certificate and this server's own leaf,
	// so it trusts more than "the operator cert" alone - set this
	// explicitly to a dedicated CA where that is too wide. The literal
	// value AdminClientCASystemRoots ("system") keeps the pre-#624
	// host-root behavior explicitly, for a deployment whose operator
	// certificates come from a public CA. Env: SEP2_ADMIN_CLIENT_CA. See
	// EffectiveAdminClientCA and buildAdminTLSConfig (internal/server/server.go).
	AdminClientCA string

	// #269: operator hint that the admin listener is fronted by an
	// upstream reverse proxy that injects X-Forwarded-* / Forwarded
	// headers. When the admin listener binds to a non-loopback address
	// AND this is false, the server logs a startup WARNING explaining
	// that without an upstream proxy, AdminAuthMiddleware Path 0 will
	// admit ALL traffic as loopback-local (the proxy injects loopback
	// XFF when relaying loopback-to-loopback). Env: SEP2_ADMIN_BEHIND_PROXY.
	AdminBehindProxy bool

	// #365: explicit operator opt-in required before the admin listener may
	// bind an address reachable from outside this host. Absent, a resolved
	// non-loopback admin bind is a hard startup error and no socket opens:
	// the admin plane fails closed on its exposure posture rather than
	// warning and serving anyway. Env: SEP2_ADMIN_ALLOW_NON_LOOPBACK.
	AdminAllowNonLoopback bool

	// #270: extra Host-header values appended to the static admin
	// allowlist (defaults: localhost, 127.0.0.1, ::1, ieee2030-5.local).
	// Loaded from SEP2_ADMIN_ALLOWED_HOSTS as a CSV. Defense-in-depth
	// against DNS rebinding at the admin listener boundary; the static
	// defaults are NEVER opted out by setting this var.
	AdminAllowedHosts []string // env SEP2_ADMIN_ALLOWED_HOSTS

	// AdminLegacyDashboard serves the pre-Svelte string-constant dashboard
	// at GET / instead of the embedded admin UI. The rollback path for the
	// dashboard rewrite: an operator whose workflow the new page breaks
	// sets this and gets the old page back without a downgrade. Env:
	// SEP2_ADMIN_LEGACY_DASHBOARD.
	AdminLegacyDashboard bool

	// MetricsAddr is the bind address for the dedicated plain-HTTP Prometheus
	// metrics listener (env SEP2_METRICS_ADDR, e.g. ":9100"). Empty disables
	// the metrics listener entirely (default OFF). The listener serves ONLY
	// GET /metrics; it is NEVER mounted on the mTLS protocol listener or the
	// auth-gated admin listener, so exposition data has no client-cert or
	// Bearer gate.
	//
	// #268 loopback default (see ResolveMetricsBind): a bare ":<port>"
	// resolves to 127.0.0.1:<port> so the unauthenticated /metrics surface is
	// loopback-only by default; any explicit host (0.0.0.0, an LAN IP, [::])
	// is honored verbatim and triggers a non-loopback startup warning. A
	// containerized Prometheus that scrapes via host.docker.internal (the
	// docker bridge gateway, NOT loopback) must use the explicit routable form
	// SEP2_METRICS_ADDR=0.0.0.0:9100, which exposes /metrics on all interfaces
	// and so MUST sit behind a host firewall / trusted network.
	MetricsAddr string // env SEP2_METRICS_ADDR

	TZOffset    int32  // timezone offset from UTC in seconds
	DSTOffset   int32  // DST offset in seconds
	DSTStart    int64  // DST start (unix seconds)
	DSTEnd      int64  // DST end (unix seconds)
	TimeQuality uint8  // TimeQualityType per spec section 9.2
	EnableMDNS  bool   // enable mDNS service advertisement
	MDNSHost    string // mDNS hostname

	// NotificationAllowLoopback lets subscriptions name loopback
	// notificationURIs. Off by default because the admin listener is on
	// loopback. Env: SEP2_NOTIFICATION_ALLOW_LOOPBACK.
	NotificationAllowLoopback bool

	// PEN is this server's manufacturer Private Enterprise Number, embedded
	// in the low 32 bits of a minted FlowReservationResponse mRID (#665) so
	// a device can attribute it to this server rather than to a random
	// draw. Env: SEP2_PEN. Nil (unset) or the explicit value 0 (IANA-
	// reserved; use EffectivePEN, which treats it the same way
	// internal/dercontrol.Config already does) means the server was not
	// given one: minted mRIDs are then fully random and not conformant
	// with IEEE 2030.5 mRIDType, and the server logs one startup warning
	// rather than refusing the request.
	PEN *uint32

	// SEP2Edition is the IEEE 2030.5 edition GET /api/derms/fleets uses to
	// choose ReadingType.flowDirection semantics for its export-positive
	// mirror-reading sign mapping (#715 fix round 3, item 2: operator
	// decision on #715). Env: SEP2_EDITION, "2018" or "2023". Empty (unset)
	// means the server was not given one; use EffectiveSEP2Edition, which
	// resolves that to "2018", the edition CSIP conformance is written
	// against. Under 2018, flowDirection Forward means import (mapped
	// negative) and Reverse means export (mapped positive). Under 2023,
	// that pair flips to export/import ONLY when the posting
	// MirrorUsagePoint's roleFlags has isDER (bit 3) set; a non-DER mirror
	// under 2023 keeps the 2018 mapping. Neither edition changes abs(value)
	// being applied before the sign (#715 fix round 1, item 2): only the
	// wire's own sign is ever untrusted.
	SEP2Edition string

	// FlowReservationDeadline is how long a FlowReservationRequest waits for
	// an operator answer before the fallback decides. Env:
	// SEP2_FLOW_RESERVATION_DEADLINE_SECONDS, 1 to 3600. Zero means unset;
	// use EffectiveFlowReservationDeadline.
	FlowReservationDeadline time.Duration

	// FlowReservationRetentionGrace is how long an ended flow reservation
	// stays readable before it is removed. Env:
	// SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS, 900 to 604800. Zero means
	// unset; use EffectiveFlowReservationRetentionGrace.
	FlowReservationRetentionGrace time.Duration

	// MirrorReadingRetention is how long a MirrorMeterReading is kept after
	// the server received it. Env: SEP2_MIRROR_READING_RETENTION_SECONDS,
	// 87300 to 2592000. Zero means unset; use EffectiveMirrorReadingRetention.
	MirrorReadingRetention time.Duration

	// MirrorReadingMaxPerSeries caps the readings kept per mirror and mRID.
	// Env: SEP2_MIRROR_READING_MAX_PER_SERIES, 292 to 2592000. Zero means
	// unset; use EffectiveMirrorReadingMaxPerSeries.
	MirrorReadingMaxPerSeries int
}

// ParseFlowReservationDeadlineSeconds validates the value of
// SEP2_FLOW_RESERVATION_DEADLINE_SECONDS: empty is unset (zero), anything
// else must be whole seconds from 1 to 3600.
func ParseFlowReservationDeadlineSeconds(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS: %q is not a whole number of seconds", v)
	}
	d := time.Duration(n) * time.Second
	if n < int64(MinFlowReservationDeadline/time.Second) || n > int64(MaxFlowReservationDeadline/time.Second) {
		return 0, fmt.Errorf("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS: %d is outside 1 to 3600", n)
	}
	return d, nil
}

// EffectiveFlowReservationDeadline resolves an unset deadline to the default
// and refuses a value outside the bounds, so a Config built without the env
// parser cannot start a queue with a hold the setting would have refused.
func (c *Config) EffectiveFlowReservationDeadline() (time.Duration, error) {
	if c.FlowReservationDeadline == 0 {
		return DefaultFlowReservationDeadline, nil
	}
	if c.FlowReservationDeadline < MinFlowReservationDeadline || c.FlowReservationDeadline > MaxFlowReservationDeadline {
		return 0, fmt.Errorf("flow reservation deadline %s is outside 1s to 1h", c.FlowReservationDeadline)
	}
	return c.FlowReservationDeadline, nil
}

// ParseFlowReservationRetentionGraceSeconds validates the value of
// SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS: empty is unset (zero),
// anything else must be whole seconds from 900 to 604800.
func ParseFlowReservationRetentionGraceSeconds(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS: %q is not a whole number of seconds", v)
	}
	if n < int64(MinFlowReservationRetentionGrace/time.Second) || n > int64(MaxFlowReservationRetentionGrace/time.Second) {
		return 0, fmt.Errorf("SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS: %d is outside 900 to 604800", n)
	}
	return time.Duration(n) * time.Second, nil
}

// EffectiveFlowReservationRetentionGrace resolves an unset grace to the
// default and refuses a value outside the bounds.
func (c *Config) EffectiveFlowReservationRetentionGrace() (time.Duration, error) {
	g := c.FlowReservationRetentionGrace
	if g == 0 {
		return DefaultFlowReservationRetentionGrace, nil
	}
	if g < MinFlowReservationRetentionGrace || g > MaxFlowReservationRetentionGrace || g%time.Second != 0 {
		return 0, fmt.Errorf("flow reservation retention grace %s is not whole seconds from 15m to 168h", g)
	}
	return g, nil
}

// ParseMirrorReadingRetentionSeconds validates the value of
// SEP2_MIRROR_READING_RETENTION_SECONDS: empty is unset (zero), anything else
// must be whole seconds within the bounds.
func ParseMirrorReadingRetentionSeconds(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("SEP2_MIRROR_READING_RETENTION_SECONDS: %q is not a whole number of seconds", v)
	}
	lo, hi := int64(MinMirrorReadingRetention/time.Second), int64(MaxMirrorReadingRetention/time.Second)
	if n < lo || n > hi {
		return 0, fmt.Errorf("SEP2_MIRROR_READING_RETENTION_SECONDS: %d is outside %d to %d", n, lo, hi)
	}
	return time.Duration(n) * time.Second, nil
}

// EffectiveMirrorReadingRetention resolves an unset retention to the default
// and refuses a value outside the bounds.
func (c *Config) EffectiveMirrorReadingRetention() (time.Duration, error) {
	r := c.MirrorReadingRetention
	if r == 0 {
		return DefaultMirrorReadingRetention, nil
	}
	if r < MinMirrorReadingRetention || r > MaxMirrorReadingRetention || r%time.Second != 0 {
		return 0, fmt.Errorf("mirror reading retention %s is not whole seconds from %s to %s", r, MinMirrorReadingRetention, MaxMirrorReadingRetention)
	}
	return r, nil
}

// ParseMirrorReadingMaxPerSeries validates the value of
// SEP2_MIRROR_READING_MAX_PER_SERIES: empty is unset (zero), anything else
// must be a whole number within the bounds.
func ParseMirrorReadingMaxPerSeries(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("SEP2_MIRROR_READING_MAX_PER_SERIES: %q is not a whole number", v)
	}
	if n < MinMirrorReadingMaxPerSeries || n > MaxMirrorReadingMaxPerSeries {
		return 0, fmt.Errorf("SEP2_MIRROR_READING_MAX_PER_SERIES: %d is outside %d to %d", n, MinMirrorReadingMaxPerSeries, MaxMirrorReadingMaxPerSeries)
	}
	return n, nil
}

// EffectiveMirrorReadingMaxPerSeries resolves an unset cap to the default and
// refuses a value outside the bounds.
func (c *Config) EffectiveMirrorReadingMaxPerSeries() (int, error) {
	n := c.MirrorReadingMaxPerSeries
	if n == 0 {
		return DefaultMirrorReadingMaxPerSeries, nil
	}
	if n < MinMirrorReadingMaxPerSeries || n > MaxMirrorReadingMaxPerSeries {
		return 0, fmt.Errorf("mirror reading cap %d is outside %d to %d", n, MinMirrorReadingMaxPerSeries, MaxMirrorReadingMaxPerSeries)
	}
	return n, nil
}

// EffectivePEN normalizes PEN the way internal/dercontrol.Config already
// does for its own PEN field: the explicit value 0 is IANA-reserved and is
// treated as unset, so both fields resolve "not configured" identically.
func (c *Config) EffectivePEN() *uint32 {
	if c.PEN == nil || *c.PEN == 0 {
		return nil
	}
	return c.PEN
}

// EffectiveSEP2Edition normalizes SEP2Edition the way EffectivePEN
// normalizes PEN: an unset (empty) value resolves to the default, "2018".
func (c *Config) EffectiveSEP2Edition() string {
	if c.SEP2Edition == "" {
		return "2018"
	}
	return c.SEP2Edition
}

// EffectiveAdminListen returns the admin listener address as supplied by
// the operator (AdminListen wins; falls back to the deprecated AdminAddr).
// An empty return value means the admin listener is disabled.
//
// This returns the env value verbatim - the loopback default applied to a
// bare-port input is layered on top via ResolveAdminBind at the actual
// net.Listen site. Keeping the env value pristine here means the banner
// surface and back-compat consumers see exactly what the operator set.
func (c *Config) EffectiveAdminListen() string {
	if c.AdminListen != "" {
		return c.AdminListen
	}
	return c.AdminAddr
}

// EffectiveServingCA returns ServingCAFile if set, else CAFile. #622: this
// is the CA a caller signs THIS server's own certificates with - see
// ServingCAFile's own comment for the roles that route through it.
func (c *Config) EffectiveServingCA() string {
	if c.ServingCAFile != "" {
		return c.ServingCAFile
	}
	return c.CAFile
}

// EffectiveDeviceCA returns DeviceCAFile if set, else CAFile. #622: this is
// the CA the protocol listener's ClientCAs pool trusts devices against, and
// that signs minted device certs - see DeviceCAFile's own comment.
func (c *Config) EffectiveDeviceCA() string {
	if c.DeviceCAFile != "" {
		return c.DeviceCAFile
	}
	return c.CAFile
}

// AdminClientCASystemRoots is the SEP2_ADMIN_CLIENT_CA / AdminClientCA
// value that keeps the admin listener's client-certificate verification on
// the host root trust store - the behavior crypto/tls and crypto/x509 fall
// back to when ClientCAs is nil - instead of a file this server loads. See
// EffectiveAdminClientCA.
const AdminClientCASystemRoots = "system"

// EffectiveAdminClientCA returns AdminClientCA if set, else the serving CA
// (EffectiveServingCA) - see AdminClientCA's own field comment for the
// anchor's role, and adminClientCAPool (internal/server/server.go) for how
// each returned value, sentinel included, is resolved.
func (c *Config) EffectiveAdminClientCA() string {
	if c.AdminClientCA != "" {
		return c.AdminClientCA
	}
	return c.EffectiveServingCA()
}

// ResolveAdminBind applies the #268 loopback default to a raw admin
// listen string. The contract:
//
//	""              -> ""              (admin disabled - caller gates this)
//	":<port>"       -> "127.0.0.1:<port>"  (bare port -> loopback by default)
//	"<host>:<port>" -> unchanged       (any explicit host is honored verbatim)
//	"<port>"        -> unchanged       (malformed input passed through; net.Listen will reject)
//
// Rationale: the SEP2 protocol listener (cfg.Addr) admits any self-signed
// client cert via tls.RequireAnyClientCert + manual verify, and the admin
// auth middleware's Path 0 admits loopback requests with no proxy headers.
// Pre-#268, a bare ":<port>" admin listen bound 0.0.0.0, so any
// network neighbor (or any co-resident process on a multi-tenant host
// where the kernel routes loopback liberally) could reach the admin
// surface with no creds. Defaulting bare ports to 127.0.0.1 makes the
// network-exposure case opt-in: operators who want public bind must say
// so explicitly with "0.0.0.0:<port>" or "<ip>:<port>".
func ResolveAdminBind(listen string) string {
	if listen == "" {
		return ""
	}
	if strings.HasPrefix(listen, ":") {
		// Bare port. net.SplitHostPort accepts ":8444"; the host portion
		// comes back empty - that's the case we rewrite. Any non-empty
		// host (including 0.0.0.0, [::], 192.168.x.y, hostnames) is
		// passed through verbatim.
		host, port, err := net.SplitHostPort(listen)
		if err != nil || host != "" {
			return listen
		}
		return net.JoinHostPort("127.0.0.1", port)
	}
	return listen
}

// ResolveMetricsBind applies the #268 loopback default to a raw metrics
// listen string, with the same contract as ResolveAdminBind:
//
//	""              -> ""              (metrics disabled - caller gates this)
//	":<port>"       -> "127.0.0.1:<port>"  (bare port -> loopback by default)
//	"<host>:<port>" -> unchanged       (any explicit host is honored verbatim)
//	"<port>"        -> unchanged       (malformed input passed through; net.Listen rejects)
//
// Rationale: the metrics listener serves an UNAUTHENTICATED /metrics surface
// (no client cert, no Bearer - see Config.MetricsAddr). Pre-this-fix, a bare
// ":9100" bound 0.0.0.0/[::], so /metrics was reachable network-wide by any
// neighbor. Defaulting bare ports to loopback makes network exposure opt-in:
// an operator who wants a routable bind (e.g. for a containerized Prometheus
// scraping host.docker.internal) must say so explicitly with
// "0.0.0.0:<port>" or "<ip>:<port>", which also fires a startup warning
// (see metricsExposureWarning).
//
// Implemented as a thin alias over ResolveAdminBind so the two listeners can
// never drift in their loopback-default semantics - the rule is identical.
func ResolveMetricsBind(listen string) string {
	return ResolveAdminBind(listen)
}

// EffectiveStorePath resolves the on-disk JSON snapshot path for a named
// store under the #165 single-knob shape:
//
//  1. dedicatedPath wins if non-empty (back-compat for
//     SEP2_SUBSCRIPTION_STORE_PATH and any future per-store overrides).
//  2. Else if DataDir is non-empty, derive <DataDir>/<storeName>.json.
//  3. Else return "" - pure in-memory mode (historical default).
//
// storeName is the bare filename stem (e.g. "enddevices", "registrations",
// "fsas", "derprograms", "subscriptions"). Caller adds the .json suffix via
// this helper; callers MUST NOT hand-roll the path because the precedence
// rule is the one place we test.
func (c *Config) EffectiveStorePath(storeName, dedicatedPath string) string {
	if dedicatedPath != "" {
		return dedicatedPath
	}
	if c.DataDir == "" {
		return ""
	}
	return filepath.Join(c.DataDir, storeName+".json")
}

// EffectiveTrafficDir resolves WHERE the traffic-capture segment log goes:
// TrafficDir if set, else <DataDir>/traffic, else "" (no directory
// resolves). It says nothing about WHETHER capture runs at all - that is
// TrafficCapture's job (#628 fix round 1); a caller gates on both. Not
// implemented as a call to EffectiveStorePath, which always appends
// ".json": the capture store owns a whole directory, not one file (#611 Q4).
func (c *Config) EffectiveTrafficDir() string {
	if c.TrafficDir != "" {
		return c.TrafficDir
	}
	if c.DataDir == "" {
		return ""
	}
	return filepath.Join(c.DataDir, "traffic")
}
