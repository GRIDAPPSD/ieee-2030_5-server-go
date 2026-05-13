package config

// Config holds server configuration.
type Config struct {
	Addr            string   // listen address for IEEE 2030.5 protocol (e.g., ":443")
	CertFile        string   // server cert PEM path
	KeyFile         string   // server key PEM path
	CAFile          string   // CA cert PEM path
	ExtraClientCAs  []string // additional PEM paths appended to the ClientCAs pool (additive to CAFile)
	BootFixtureFile string   // optional YAML topology fixture loaded at startup; empty = no fixture
	AdminAddr       string   // admin HTTPS listener address (e.g., ":8443")
	AdminKey        string   // Bearer token for admin API (empty = Bearer auth disabled)
	TZOffset        int32    // timezone offset from UTC in seconds
	DSTOffset       int32    // DST offset in seconds
	DSTStart        int64    // DST start (unix seconds)
	DSTEnd          int64    // DST end (unix seconds)
	TimeQuality     uint8    // TimeQualityType per spec section 9.2
	EnableCCM       bool     // use CCM-8 cipher suite (spec-compliant) vs GCM fallback
	EnableMDNS      bool     // enable mDNS service advertisement
	MDNSHost        string   // mDNS hostname

	// SubscriptionStorePath enables IEEE-077 durable subscription persistence
	// when non-empty. Path to a JSON file the server reads at startup and
	// rewrites atomically on every subscription Create / Delete. Empty
	// (default) keeps subscriptions in memory only — historical behavior.
	SubscriptionStorePath string
}
