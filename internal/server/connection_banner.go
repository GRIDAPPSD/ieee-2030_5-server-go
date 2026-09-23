package server

// #206 - structured connection-details banner printed once at server
// boot. Operators run any of `make run`, `make run-ccm`, `make run-full`,
// `make run-testdevice`, `make run-sunspec` and need a single, copy-pasteable
// block that says exactly where to point a device and which trust roots are
// in play. The banner is operator-facing log output ONLY - it does not
// change server behavior and it never prints secrets (admin keys, private
// keys, etc.).
//
// The format is pinned by TestRenderConnectionBanner_* in
// connection_banner_test.go. Any format change must update the test in the
// same commit.

import (
	"fmt"
	"strings"
)

// BannerInput is everything the banner renderer needs. Kept as a flat
// struct (not a pointer to Config) so the renderer is trivially testable
// and the formatting layer stays decoupled from the config struct.
type BannerInput struct {
	// SEP2 listener
	Addr       string // raw listen address, e.g. ":8443" or "10.0.0.101:8888"
	TLSMode    string // human label: "CCM-8", "GCM", "CCM-8+GCM"
	CertFile   string // server leaf cert path
	ServerSFDI string // derived from leaf cert (12 decimal digits)
	ServerLFDI string // derived from leaf cert (40 hex chars)

	// Trust pool
	CAFile         string   // primary server CA path (SEP2_CA)
	ExtraClientCAs []string // additional trusted client CAs (SEP2_EXTRA_CLIENT_CAS)

	// Admin listener
	AdminListen   string // bind addr of the admin port; empty = admin disabled
	AdminTLSDesc  string // human label: "plain HTTP (Caddy mode)", "HTTPS, self-signed", etc.
	AdminAuthDesc string // "Bearer key set", "mTLS-only", "disabled"

	// Persistence
	DataDirDesc string // "<path>" or "in-memory" or "<path> (subscriptions: <override>)"

	// Discovery
	MDNSEnabled bool

	// Device-side smoke hints. Empty = render generic <device>.crt / <device>.key placeholders.
	DeviceCertHint string
	DeviceKeyHint  string
	DeviceCAHint   string // typically same as CAFile; allows override per profile
}

// RenderConnectionBanner returns the rendered banner text. Caller writes it
// to log.Print or directly to stderr; this function never performs I/O.
func RenderConnectionBanner(in BannerInput) string {
	host := displayHost(in.Addr)

	caHint := in.DeviceCAHint
	if caHint == "" {
		caHint = in.CAFile
	}
	certHint := in.DeviceCertHint
	if certHint == "" {
		certHint = "<device>.crt"
	}
	keyHint := in.DeviceKeyHint
	if keyHint == "" {
		keyHint = "<device>.key"
	}

	adminTLS := in.AdminTLSDesc
	if adminTLS == "" {
		adminTLS = "n/a"
	}
	adminURL := "(disabled)"
	if in.AdminListen != "" {
		// Plain-HTTP admin (Caddy mode) is the default. Only flip the
		// scheme to https when the admin listener is actually doing TLS,
		// otherwise the printed URL is unfollowable.
		scheme := "http"
		if strings.HasPrefix(adminTLS, "HTTPS") {
			scheme = "https"
		}
		adminURL = fmt.Sprintf("%s://%s/login", scheme, displayHost(in.AdminListen))
	}

	mdnsLabel := "off"
	if in.MDNSEnabled {
		mdnsLabel = "on"
	}

	var b strings.Builder
	rule := strings.Repeat("=", 60)

	fmt.Fprintln(&b, rule)
	fmt.Fprintln(&b, " SEP2 Server - connection details")
	fmt.Fprintln(&b, rule)
	fmt.Fprintf(&b, " Listen:       https://%s\n", host)
	fmt.Fprintf(&b, " TLS mode:     %s\n", in.TLSMode)
	fmt.Fprintf(&b, " Server cert:  %s  (SFDI: %s, LFDI: %s)\n", in.CertFile, in.ServerSFDI, in.ServerLFDI)
	fmt.Fprintf(&b, " Server CA:    %s\n", in.CAFile)
	fmt.Fprintln(&b, " Trusted client CAs:")
	fmt.Fprintf(&b, "   - %s\n", in.CAFile)
	for _, ca := range in.ExtraClientCAs {
		fmt.Fprintf(&b, "   - %s\n", ca)
	}
	fmt.Fprintf(&b, " Admin URL:    %s\n", adminURL)
	fmt.Fprintf(&b, " Admin TLS:    %s\n", adminTLS)
	fmt.Fprintf(&b, " Admin auth:   %s\n", in.AdminAuthDesc)
	fmt.Fprintf(&b, " Data dir:     %s\n", in.DataDirDesc)
	fmt.Fprintf(&b, " mDNS:         %s\n", mdnsLabel)
	fmt.Fprintln(&b, rule)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, " Device-side smoke:")
	fmt.Fprintf(&b, "   curl --cacert %s \\\n", caHint)
	fmt.Fprintf(&b, "        --cert %s --key %s \\\n", certHint, keyHint)
	fmt.Fprintf(&b, "        https://%s/dcap\n", host)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, rule)
	return b.String()
}

// displayHost converts a raw bind address into a human-friendly host:port
// for the banner. Bare ":<port>" binds -> "localhost:<port>" because that's
// what the operator will actually curl against; a wildcard "0.0.0.0:<port>"
// is normalized the same way. Any other host:port is preserved verbatim so
// non-loopback profiles (e.g. 10.0.0.101:8888) surface the real target.
func displayHost(addr string) string {
	if addr == "" {
		return "localhost"
	}
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "localhost:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, "[::]:") {
		return "localhost:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr
}
