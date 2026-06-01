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

	// IEEE-097: persistence root for admin-mutated stores. Empty = pure
	// in-memory (back-compat with every test path that predates IEEE-097).
	// When set, each persistent store auto-files under <DataDir>/<name>.json
	// unless a store-specific dedicated path env var overrides it (see
	// EffectiveStorePath for the precedence rule).
	DataDir string // env SEP2_DATA_DIR

	// IEEE-097: dedicated path for the subscription store. Predates DataDir;
	// preserved for back-compat with IEEE-077-era deployments. When both are
	// set, the dedicated path wins.
	SubscriptionStorePath string // env SEP2_SUBSCRIPTION_STORE_PATH

	// IEEE-094: admin listener configuration.
	//
	// The SEP2 protocol listener is RequireAnyClientCert + manual verify per
	// CSIP V1.2 (every device presents a cert). The admin listener
	// intentionally uses a weaker posture (VerifyClientCertIfGiven, or plain
	// HTTP behind Caddy) so browser logins can flow through
	// AdminAuthMiddleware's Bearer + cookie paths without weakening the SEP2
	// wire.
	//
	// AdminListen is the canonical knob (env SEP2_ADMIN_LISTEN). For
	// back-compat with pre-IEEE-094 deployments, an empty AdminListen falls
	// back to AdminAddr (env SEP2_ADMIN_ADDR). Use EffectiveAdminListen() to
	// resolve which value the server should bind to.
	AdminListen  string // admin listener address (e.g., ":9443"); empty = falls back to AdminAddr
	AdminAddr    string // DEPRECATED alias for AdminListen; preserved for back-compat
	AdminKey     string // Bearer token for admin API (empty = Bearer auth disabled)
	AdminTLS     bool   // true = HTTPS on admin listener; false = plain HTTP (Caddy mode)
	AdminCert    string // admin listener cert PEM path; empty + AdminTLS = self-signed
	AdminKeyFile string // admin listener key PEM path; required when AdminCert is set

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

// ResolveAdminBind applies the IEEE-136 loopback default to a raw admin
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
// Pre-IEEE-136, a bare ":<port>" admin listen bound 0.0.0.0, so any
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

// EffectiveStorePath resolves the on-disk JSON snapshot path for a named
// store under the IEEE-097 single-knob shape:
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
