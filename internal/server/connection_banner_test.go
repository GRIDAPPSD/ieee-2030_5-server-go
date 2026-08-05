package server

// #206 — boot connection-details banner.
//
// The format test pins the rendered banner shape so future log changes are
// deliberate. The banner is operator-facing: a human reads it once at boot
// and copy-pastes the device-side curl line. If you change anything below,
// expect to update operator docs and the regression test in the same commit.

import (
	"strings"
	"testing"
)

func TestRenderConnectionBanner_FullProfile(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:       ":8443",
		TLSMode:    "CCM-8",
		CertFile:   "certs/server.crt",
		ServerSFDI: "123456789012",
		ServerLFDI: "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		CAFile:     "certs/ca.crt",
		ExtraClientCAs: []string{
			"testdata/csip-pki/testdevice/root_ca.pem",
			"testdata/csip-pki/sunspec/roots.pem",
		},
		AdminListen:    ":8444",
		AdminTLSDesc:   "plain HTTP (Caddy mode)",
		AdminAuthDesc:  "Bearer key set",
		DataDirDesc:    "/tmp/sep2-data",
		MDNSEnabled:    true,
		DeviceCertHint: "certs/device.crt",
		DeviceKeyHint:  "certs/device.key",
		DeviceCAHint:   "certs/ca.crt",
	}

	out := RenderConnectionBanner(input)

	mustContain := []string{
		"SEP2 Server — connection details",
		"Listen:       https://localhost:8443",
		"TLS mode:     CCM-8",
		"Server cert:  certs/server.crt  (SFDI: 123456789012, LFDI: ABCDEF0123456789ABCDEF0123456789ABCDEF01)",
		"Server CA:    certs/ca.crt",
		"Trusted client CAs:",
		"- certs/ca.crt",
		"- testdata/csip-pki/testdevice/root_ca.pem",
		"- testdata/csip-pki/sunspec/roots.pem",
		"Admin URL:    http://localhost:8444/login",
		"Admin TLS:    plain HTTP (Caddy mode)",
		"Admin auth:   Bearer key set",
		"Data dir:     /tmp/sep2-data",
		"mDNS:         on",
		"Device-side smoke:",
		"--cacert certs/ca.crt",
		"--cert certs/device.crt --key certs/device.key",
		"https://localhost:8443/dcap",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing expected line: %q\n---banner---\n%s", want, out)
		}
	}

	// Must NOT leak secrets.
	for _, leak := range []string{"AdminKey:", "private key", "BEGIN RSA"} {
		if strings.Contains(out, leak) {
			t.Errorf("banner unexpectedly contains potential secret marker %q", leak)
		}
	}
}

func TestRenderConnectionBanner_DefaultsAndMinimal(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:          ":443",
		TLSMode:       "GCM",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "000000000000",
		ServerLFDI:    "0000000000000000000000000000000000000000",
		CAFile:        "certs/ca.crt",
		AdminListen:   "",
		AdminTLSDesc:  "",
		AdminAuthDesc: "disabled",
		DataDirDesc:   "in-memory",
		MDNSEnabled:   false,
	}

	out := RenderConnectionBanner(input)

	for _, want := range []string{
		"Listen:       https://localhost:443",
		"TLS mode:     GCM",
		"Admin URL:    (disabled)",
		"Admin auth:   disabled",
		"Data dir:     in-memory",
		"mDNS:         off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing expected line: %q\n---banner---\n%s", want, out)
		}
	}
}

func TestRenderConnectionBanner_AdminHTTPSScheme(t *testing.T) {
	t.Parallel()

	// When the admin listener is doing real HTTPS (operator-supplied cert
	// or self-signed fallback), the printed URL must use https:// so the
	// operator can paste it into a browser unchanged.
	input := BannerInput{
		Addr:          ":8443",
		TLSMode:       "CCM-8",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "123456789012",
		ServerLFDI:    "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		CAFile:        "certs/ca.crt",
		AdminListen:   ":9443",
		AdminTLSDesc:  "HTTPS, self-signed",
		AdminAuthDesc: "Bearer key set",
		DataDirDesc:   "in-memory",
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "Admin URL:    https://localhost:9443/login") {
		t.Errorf("HTTPS admin listener should render https:// URL\n---banner---\n%s", out)
	}
}

func TestRenderConnectionBanner_AddrHostPreserved(t *testing.T) {
	t.Parallel()

	// Non-loopback bind address (Enphase profile) must surface the actual
	// host so the operator's copy-paste curl works against the right IP.
	input := BannerInput{
		Addr:          "10.0.0.101:8888",
		TLSMode:       "CCM-8",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "123456789012",
		ServerLFDI:    "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		CAFile:        "certs/ca.crt",
		AdminAuthDesc: "disabled",
		DataDirDesc:   "in-memory",
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "Listen:       https://10.0.0.101:8888") {
		t.Errorf("banner did not preserve non-loopback host\n---banner---\n%s", out)
	}
	if !strings.Contains(out, "https://10.0.0.101:8888/dcap") {
		t.Errorf("device-side curl did not preserve non-loopback host\n---banner---\n%s", out)
	}
}
