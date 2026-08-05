package config

import (
	"net"
	"path/filepath"
	"strings"
)

// Config holds server configuration.
type Config struct {
	Addr            string   // listen address for IEEE 2030.5 protocol (e.g., ":443")
	CertFile        string   // server cert PEM path
	KeyFile         string   // server key PEM path
	CAFile          string   // CA cert PEM path
	ExtraClientCAs  []string // additional PEM paths appended to the ClientCAs pool (additive to CAFile)
	BootFixtureFile string   // optional YAML topology fixture loaded at startup; empty = no fixture

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

	// #269: operator hint that the admin listener is fronted by an
	// upstream reverse proxy that injects X-Forwarded-* / Forwarded
	// headers. When the admin listener binds to a non-loopback address
	// AND this is false, the server logs a startup WARNING explaining
	// that without an upstream proxy, AdminAuthMiddleware Path 0 will
	// admit ALL traffic as loopback-local (the proxy injects loopback
	// XFF when relaying loopback-to-loopback). Env: SEP2_ADMIN_BEHIND_PROXY.
	AdminBehindProxy bool

	// #270: extra Host-header values appended to the static admin
	// allowlist (defaults: localhost, 127.0.0.1, ::1, ieee2030-5.local).
	// Loaded from SEP2_ADMIN_ALLOWED_HOSTS as a CSV. Defense-in-depth
	// against DNS rebinding at the admin listener boundary; the static
	// defaults are NEVER opted out by setting this var.
	AdminAllowedHosts []string // env SEP2_ADMIN_ALLOWED_HOSTS

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
	EnableCCM   bool   // use CCM-8 cipher suite (spec-compliant) vs GCM fallback
	EnableMDNS  bool   // enable mDNS service advertisement
	MDNSHost    string // mDNS hostname
}

// EffectiveAdminListen returns the admin listener address as supplied by
// the operator (AdminListen wins; falls back to the deprecated AdminAddr).
// An empty return value means the admin listener is disabled.
//
// This returns the env value verbatim — the loopback default applied to a
// bare-port input is layered on top via ResolveAdminBind at the actual
// net.Listen site. Keeping the env value pristine here means the banner
// surface and back-compat consumers see exactly what the operator set.
func (c *Config) EffectiveAdminListen() string {
	if c.AdminListen != "" {
		return c.AdminListen
	}
	return c.AdminAddr
}

// ResolveAdminBind applies the #268 loopback default to a raw admin
// listen string. The contract:
//
//	""              → ""              (admin disabled — caller gates this)
//	":<port>"       → "127.0.0.1:<port>"  (bare port → loopback by default)
//	"<host>:<port>" → unchanged       (any explicit host is honored verbatim)
//	"<port>"        → unchanged       (malformed input passed through; net.Listen will reject)
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
		// comes back empty — that's the case we rewrite. Any non-empty
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
//	""              → ""              (metrics disabled — caller gates this)
//	":<port>"       → "127.0.0.1:<port>"  (bare port → loopback by default)
//	"<host>:<port>" → unchanged       (any explicit host is honored verbatim)
//	"<port>"        → unchanged       (malformed input passed through; net.Listen rejects)
//
// Rationale: the metrics listener serves an UNAUTHENTICATED /metrics surface
// (no client cert, no Bearer — see Config.MetricsAddr). Pre-this-fix, a bare
// ":9100" bound 0.0.0.0/[::], so /metrics was reachable network-wide by any
// neighbor. Defaulting bare ports to loopback makes network exposure opt-in:
// an operator who wants a routable bind (e.g. for a containerized Prometheus
// scraping host.docker.internal) must say so explicitly with
// "0.0.0.0:<port>" or "<ip>:<port>", which also fires a startup warning
// (see metricsExposureWarning).
//
// Implemented as a thin alias over ResolveAdminBind so the two listeners can
// never drift in their loopback-default semantics — the rule is identical.
func ResolveMetricsBind(listen string) string {
	return ResolveAdminBind(listen)
}

// EffectiveStorePath resolves the on-disk JSON snapshot path for a named
// store under the #165 single-knob shape:
//
//  1. dedicatedPath wins if non-empty (back-compat for
//     SEP2_SUBSCRIPTION_STORE_PATH and any future per-store overrides).
//  2. Else if DataDir is non-empty, derive <DataDir>/<storeName>.json.
//  3. Else return "" — pure in-memory mode (historical default).
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
