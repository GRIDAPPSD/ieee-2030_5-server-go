package server

// #206 - boot connection-details banner.
//
// The format test pins the rendered banner shape so future log changes are
// deliberate. The banner is operator-facing: a human reads it once at boot
// and copy-pastes the device-side curl line. If you change anything below,
// expect to update operator docs and the regression test in the same commit.
//
// #622: the single "Server CA" line became two role-scoped lines (Serving
// CA / Device CA), each with a subject and a SHA-256 fingerprint, plus a
// note when one certificate fills both roles.

import (
	"strings"
	"testing"
)

func TestRenderConnectionBanner_FullProfile(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:                 ":8443",
		TLSMode:              "CCM-8",
		CertFile:             "certs/server.crt",
		ServerSFDI:           "123456789012",
		ServerLFDI:           "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		ServingCAFile:        "certs/serving-ca.crt",
		ServingCASubject:     "CN=Serving CA",
		ServingCAFingerprint: "aa11",
		DeviceCAFile:         "certs/device-ca.crt",
		DeviceCASubject:      "CN=Device CA",
		DeviceCAFingerprint:  "bb22",
		SameCA:               false,
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
		DeviceCAHint:   "certs/serving-ca.crt",
	}

	out := RenderConnectionBanner(input)

	mustContain := []string{
		"SEP2 Server - connection details",
		"Listen:       https://localhost:8443",
		"TLS mode:     CCM-8",
		"Server cert:  certs/server.crt  (SFDI: 123456789012, LFDI: ABCDEF0123456789ABCDEF0123456789ABCDEF01)",
		"Serving CA:   certs/serving-ca.crt",
		"Device CA:    certs/device-ca.crt",
		"subject: CN=Serving CA",
		"fingerprint (sha256): aa11",
		"subject: CN=Device CA",
		"fingerprint (sha256): bb22",
		"Trusted client CAs:",
		"- certs/device-ca.crt",
		"- testdata/csip-pki/testdevice/root_ca.pem",
		"- testdata/csip-pki/sunspec/roots.pem",
		"Admin URL:    http://localhost:8444/login",
		"Admin TLS:    plain HTTP (Caddy mode)",
		"Admin auth:   Bearer key set",
		"Data dir:     /tmp/sep2-data",
		"mDNS:         on",
		"Device-side smoke:",
		"(--cacert below is the Serving CA above)",
		"--cacert certs/serving-ca.crt",
		"--cert certs/device.crt --key certs/device.key",
		"https://localhost:8443/dcap",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing expected line: %q\n---banner---\n%s", want, out)
		}
	}
	// Split roles: a same-CA note must NOT appear when SameCA is false.
	if strings.Contains(out, "same certificate") {
		t.Errorf("banner claims one certificate fills both roles when SameCA is false\n---banner---\n%s", out)
	}

	// Must NOT leak secrets.
	for _, leak := range []string{"AdminKey:", "private key", "BEGIN RSA"} {
		if strings.Contains(out, leak) {
			t.Errorf("banner unexpectedly contains potential secret marker %q", leak)
		}
	}
}

// TestRenderConnectionBanner_SameCA is #622 done-condition 3's second half:
// when one certificate fills both roles, the banner says so rather than
// silently repeating the same subject/fingerprint twice with no comment.
func TestRenderConnectionBanner_SameCA(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:                 ":443",
		TLSMode:              "GCM",
		CertFile:             "certs/server.crt",
		ServerSFDI:           "000000000000",
		ServerLFDI:           "0000000000000000000000000000000000000000",
		ServingCAFile:        "certs/ca.crt",
		ServingCASubject:     "CN=Unsplit CA",
		ServingCAFingerprint: "cc33",
		DeviceCAFile:         "certs/ca.crt",
		DeviceCASubject:      "CN=Unsplit CA",
		DeviceCAFingerprint:  "cc33",
		SameCA:               true,
		AdminAuthDesc:        "disabled",
		DataDirDesc:          "in-memory",
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "same certificate") {
		t.Errorf("banner does not say one certificate fills both roles\n---banner---\n%s", out)
	}
	if !strings.Contains(out, "fingerprint (sha256): cc33") {
		t.Errorf("banner missing the shared fingerprint\n---banner---\n%s", out)
	}
}

// TestRenderConnectionBanner_CANotLoaded pins the not-loaded case: a CA
// that failed to load prints a placeholder rather than a blank subject and
// fingerprint line, which would read as a rendering bug rather than a CA
// load failure.
func TestRenderConnectionBanner_CANotLoaded(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:          ":443",
		TLSMode:       "GCM",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "000000000000",
		ServerLFDI:    "0000000000000000000000000000000000000000",
		ServingCAFile: "certs/serving-ca.crt",
		DeviceCAFile:  "certs/device-ca.crt",
		AdminAuthDesc: "disabled",
		DataDirDesc:   "in-memory",
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "(not loaded)") {
		t.Errorf("banner does not flag the unloaded CA\n---banner---\n%s", out)
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
		ServingCAFile: "certs/ca.crt",
		DeviceCAFile:  "certs/ca.crt",
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
		ServingCAFile: "certs/ca.crt",
		DeviceCAFile:  "certs/ca.crt",
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
		ServingCAFile: "certs/ca.crt",
		DeviceCAFile:  "certs/ca.crt",
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

// TestRenderConnectionBanner_DeviceCAHintFallsBackToServingCA is #638 fix
// round 1, HIGH 1: with DeviceCAHint left EMPTY (the shape buildBannerInput
// produced before this fix round, and the only shape ever reached at boot)
// and ServingCAFile distinct from DeviceCAFile, the curl anchor must print
// the SERVING file, never the device file. Mutant: connection_banner.go's
// `caHint = in.ServingCAFile` flipped to `caHint = in.DeviceCAFile` -
// before this test, no test caught it, because the only test exercising
// this field (FullProfile, above) supplies DeviceCAHint explicitly and
// never leaves the fallback branch to run.
func TestRenderConnectionBanner_DeviceCAHintFallsBackToServingCA(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:          ":8443",
		TLSMode:       "CCM-8",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "123456789012",
		ServerLFDI:    "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		ServingCAFile: "certs/serving-only.crt",
		DeviceCAFile:  "certs/device-only.crt",
		AdminAuthDesc: "disabled",
		DataDirDesc:   "in-memory",
		// DeviceCAHint deliberately left empty: this is the fallback branch.
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "--cacert certs/serving-only.crt") {
		t.Errorf("curl anchor did not fall back to the serving CA file\n---banner---\n%s", out)
	}
	if strings.Contains(out, "--cacert certs/device-only.crt") {
		t.Errorf("curl anchor used the DEVICE CA file; a device would be handed the wrong anchor\n---banner---\n%s", out)
	}
	if !strings.Contains(out, "(--cacert below is the Serving CA above)") {
		t.Errorf("banner does not label which role the curl anchor came from\n---banner---\n%s", out)
	}
}

// TestRenderConnectionBanner_CAHintRoleLabelsDeviceOverride is the control
// for the role label added in the same fix round: when DeviceCAHint is
// explicitly set to a value equal to DeviceCAFile (not ServingCAFile), the
// label must say "Device CA", proving the label is derived from the actual
// printed value rather than hardcoded to always say "Serving CA".
func TestRenderConnectionBanner_CAHintRoleLabelsDeviceOverride(t *testing.T) {
	t.Parallel()

	input := BannerInput{
		Addr:          ":8443",
		TLSMode:       "CCM-8",
		CertFile:      "certs/server.crt",
		ServerSFDI:    "123456789012",
		ServerLFDI:    "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		ServingCAFile: "certs/serving-only.crt",
		DeviceCAFile:  "certs/device-only.crt",
		DeviceCAHint:  "certs/device-only.crt",
		AdminAuthDesc: "disabled",
		DataDirDesc:   "in-memory",
	}

	out := RenderConnectionBanner(input)
	if !strings.Contains(out, "--cacert certs/device-only.crt") {
		t.Errorf("curl anchor did not use the explicit device-CA-hint override\n---banner---\n%s", out)
	}
	if !strings.Contains(out, "(--cacert below is the Device CA above)") {
		t.Errorf("label did not track the explicit override to the device CA\n---banner---\n%s", out)
	}
	if strings.Contains(out, "(--cacert below is the Serving CA above)") {
		t.Errorf("label incorrectly says Serving CA for a device-CA override\n---banner---\n%s", out)
	}
}
