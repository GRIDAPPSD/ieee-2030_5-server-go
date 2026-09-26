package sep2tls

// TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 is the IEEE 2030.5-2018 clause 6.7 /
// CSIP mandatory cipher suite, RFC 7251 ID 0xC0AE. Go's crypto/tls does not
// implement CCM mode (golang/go#27484), so no *tls.Config in this package can
// carry it: every exported constructor that builds a TLS configuration
// returns a *gotls.Config (the vendored fork in pkg/sep2tls/gotls) offering
// this suite alone. See NewCCMServerConfig, NewCCMServerConfigWithExtraCAs,
// and NewCCMServerConfigFromPEM in ccmserver.go, and NewCCMClientConfig and
// NewCCMClientConfigFromPEM in ccmclient.go.
const TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 uint16 = 0xC0AE
