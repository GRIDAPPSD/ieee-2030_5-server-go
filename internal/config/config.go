package config

// Config holds server configuration.
type Config struct {
	Addr        string // listen address (e.g., ":443")
	CertFile    string // server cert PEM path
	KeyFile     string // server key PEM path
	CAFile      string // CA cert PEM path
	AdminAddr   string // admin/management API listen address (e.g., ":8080")
	TZOffset    int32  // timezone offset from UTC in seconds
	DSTOffset   int32  // DST offset in seconds
	DSTStart    int64  // DST start (unix seconds)
	DSTEnd      int64  // DST end (unix seconds)
	TimeQuality uint8  // TimeQualityType per spec section 9.2
}
