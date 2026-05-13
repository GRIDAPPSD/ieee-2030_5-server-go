package config

import "path/filepath"

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

// EffectiveAdminListen returns the admin listener address, falling back to
// the deprecated AdminAddr when AdminListen is empty. An empty return value
// means the admin listener is disabled.
func (c *Config) EffectiveAdminListen() string {
	if c.AdminListen != "" {
		return c.AdminListen
	}
	return c.AdminAddr
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
