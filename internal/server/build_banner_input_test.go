package server

// #638 fix round 1, HIGH 2: buildBannerInput had zero test coverage before
// this file. Done-condition 3 (the banner names both CA roles with subject
// and fingerprint, and flags a shared certificate) was pinned only in the
// renderer, where a test supplies SameCA and both subjects itself; nothing
// proved the PROJECTION from *config.Config and *handler.AdminCertService
// computed those values correctly. Every test below drives buildBannerInput
// directly with real generated certificates, never a mock.

import (
	"encoding/hex"
	"testing"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// buildBannerTestCA generates a real CA certificate and returns both the
// parsed certificate and its key, for wiring into an AdminCertService.
func buildBannerTestCA(t *testing.T, commonName string) (*certs.CAInfo, []byte) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: commonName, ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(%s): %v", commonName, err)
	}
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(%s): %v", commonName, err)
	}
	key, err := certs.ParseKeyPEM(keyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(%s): %v", commonName, err)
	}
	return &certs.CAInfo{Cert: cert, Key: key}, certPEM
}

// TestBuildBannerInput_SplitRoles is #638 fix round 1, HIGH 2: with two
// distinct CAs loaded, every CA-related BannerInput field must trace back
// to the RIGHT role's certificate, not a swapped or shared one. Mutant this
// closes: `SameCA: false` hardcoded at the buildBannerInput literal, which
// the coverage lane found survives the whole suite.
func TestBuildBannerInput_SplitRoles(t *testing.T) {
	servingCA, servingPEM := buildBannerTestCA(t, "638 Serving CA")
	deviceCA, _ := buildBannerTestCA(t, "638 Device CA")
	svc := handler.NewAdminCertServiceWithCAs(servingCA.Cert, servingCA.Key, servingPEM, deviceCA.Cert, deviceCA.Key)

	cfg := &config.Config{
		Addr:          ":8443",
		CertFile:      "certs/server.crt",
		ServingCAFile: "certs/serving-ca.crt",
		DeviceCAFile:  "certs/device-ca.crt",
		AdminListen:   ":8444",
		AdminKey:      "some-key",
		DataDir:       "/data",
	}

	got := buildBannerInput(cfg, svc, "CCM-8", "123456789012", "ABCDEF0123456789ABCDEF0123456789ABCDEF01", "plain HTTP (Caddy mode)")

	if got.ServingCAFile != cfg.EffectiveServingCA() {
		t.Errorf("ServingCAFile = %q, want %q", got.ServingCAFile, cfg.EffectiveServingCA())
	}
	if got.DeviceCAFile != cfg.EffectiveDeviceCA() {
		t.Errorf("DeviceCAFile = %q, want %q", got.DeviceCAFile, cfg.EffectiveDeviceCA())
	}
	if got.SameCA {
		t.Error("SameCA = true, want false for two distinct CA files")
	}

	wantServingSubject := servingCA.Cert.Subject.String()
	wantDeviceSubject := deviceCA.Cert.Subject.String()
	if got.ServingCASubject != wantServingSubject {
		t.Errorf("ServingCASubject = %q, want %q", got.ServingCASubject, wantServingSubject)
	}
	if got.DeviceCASubject != wantDeviceSubject {
		t.Errorf("DeviceCASubject = %q, want %q", got.DeviceCASubject, wantDeviceSubject)
	}
	if got.ServingCASubject == got.DeviceCASubject {
		t.Fatal("ServingCASubject equals DeviceCASubject; the two roles are not actually distinct in this fixture")
	}

	servingFP := sepTLS.Fingerprint(servingCA.Cert)
	deviceFP := sepTLS.Fingerprint(deviceCA.Cert)
	if got.ServingCAFingerprint != hex.EncodeToString(servingFP[:]) {
		t.Errorf("ServingCAFingerprint = %q, want %q", got.ServingCAFingerprint, hex.EncodeToString(servingFP[:]))
	}
	if got.DeviceCAFingerprint != hex.EncodeToString(deviceFP[:]) {
		t.Errorf("DeviceCAFingerprint = %q, want %q", got.DeviceCAFingerprint, hex.EncodeToString(deviceFP[:]))
	}
	if got.ServingCAFingerprint == got.DeviceCAFingerprint {
		t.Fatal("ServingCAFingerprint equals DeviceCAFingerprint; fixture does not actually distinguish the roles")
	}

	// #638 fix round 1, HIGH 1: buildBannerInput sets DeviceCAHint
	// explicitly to the serving CA file, rather than leaving it for the
	// renderer's fallback to fill in silently.
	if got.DeviceCAHint != cfg.EffectiveServingCA() {
		t.Errorf("DeviceCAHint = %q, want %q (the serving CA)", got.DeviceCAHint, cfg.EffectiveServingCA())
	}

	if got.CertFile != cfg.CertFile {
		t.Errorf("CertFile = %q, want %q", got.CertFile, cfg.CertFile)
	}
	if got.AdminAuthDesc != "Bearer key set" {
		t.Errorf("AdminAuthDesc = %q, want %q", got.AdminAuthDesc, "Bearer key set")
	}
	if got.DataDirDesc != "/data" {
		t.Errorf("DataDirDesc = %q, want %q", got.DataDirDesc, "/data")
	}
}

// TestBuildBannerInput_SameCAUnsplitDeployment is the control for the
// SplitRoles test above: a deployment that sets neither ServingCAFile nor
// DeviceCAFile (both fall back to CAFile) must compute SameCA true. Proves
// the SameCA assertion in SplitRoles can fail the other way too - it is not
// vacuously false because buildBannerInput would always produce false
// without a real check.
func TestBuildBannerInput_SameCAUnsplitDeployment(t *testing.T) {
	sharedCA, sharedPEM := buildBannerTestCA(t, "638 Shared CA")
	svc := handler.NewAdminCertServiceWithCAs(sharedCA.Cert, sharedCA.Key, sharedPEM, sharedCA.Cert, sharedCA.Key)

	cfg := &config.Config{
		Addr:     ":443",
		CertFile: "certs/server.crt",
		CAFile:   "certs/ca.crt",
		// ServingCAFile, DeviceCAFile left empty: both fall back to CAFile.
	}

	got := buildBannerInput(cfg, svc, "GCM", "000000000000", "0000000000000000000000000000000000000000", "")

	if !got.SameCA {
		t.Error("SameCA = false, want true when neither ServingCAFile nor DeviceCAFile is set")
	}
	if got.ServingCAFile != "certs/ca.crt" || got.DeviceCAFile != "certs/ca.crt" {
		t.Errorf("ServingCAFile=%q DeviceCAFile=%q, want both %q", got.ServingCAFile, got.DeviceCAFile, "certs/ca.crt")
	}
	if got.DeviceCAHint != "certs/ca.crt" {
		t.Errorf("DeviceCAHint = %q, want %q", got.DeviceCAHint, "certs/ca.crt")
	}
}

// TestBuildBannerInput_NilService pins the nil-svc case (admin cert API
// entirely off): both roles must render as not-loaded, never panic on a
// nil accessor call.
func TestBuildBannerInput_NilService(t *testing.T) {
	cfg := &config.Config{
		Addr:          ":443",
		CertFile:      "certs/server.crt",
		ServingCAFile: "certs/serving-ca.crt",
		DeviceCAFile:  "certs/device-ca.crt",
	}

	got := buildBannerInput(cfg, nil, "GCM", "000000000000", "0000000000000000000000000000000000000000", "")

	if got.ServingCASubject != "" || got.ServingCAFingerprint != "" {
		t.Errorf("serving subject/fingerprint not empty with nil svc: %q / %q", got.ServingCASubject, got.ServingCAFingerprint)
	}
	if got.DeviceCASubject != "" || got.DeviceCAFingerprint != "" {
		t.Errorf("device subject/fingerprint not empty with nil svc: %q / %q", got.DeviceCASubject, got.DeviceCAFingerprint)
	}
}

// TestBuildBannerInput_CertWithoutKeyStillReportsLoaded is #638 fix round 1,
// MEDIUM 1, driven end to end through buildBannerInput: a service holding a
// certificate with a NIL key (the shape certs.LoadCAPair now hands
// cmd/sep2server/main.go for a keys-removed deployment) must still show
// subject and fingerprint, not "(not loaded)". Before this fix round,
// AdminCertService could not even be constructed with this shape from
// main.go's LoadCA, so this exact production path was unreachable; it is
// the live path now.
func TestBuildBannerInput_CertWithoutKeyStillReportsLoaded(t *testing.T) {
	servingCA, servingPEM := buildBannerTestCA(t, "638 Keyless Serving CA")
	svc := handler.NewAdminCertServiceWithCAs(servingCA.Cert, nil, servingPEM, nil, nil)

	cfg := &config.Config{
		Addr:          ":443",
		CertFile:      "certs/server.crt",
		ServingCAFile: "certs/serving-ca.crt",
		DeviceCAFile:  "certs/device-ca.crt",
	}

	got := buildBannerInput(cfg, svc, "GCM", "000000000000", "0000000000000000000000000000000000000000", "")

	wantSubject := servingCA.Cert.Subject.String()
	if got.ServingCASubject != wantSubject {
		t.Errorf("ServingCASubject = %q, want %q (loaded despite nil key)", got.ServingCASubject, wantSubject)
	}
	if got.ServingCAFingerprint == "" {
		t.Error("ServingCAFingerprint is empty, want a real fingerprint for a loaded certificate with no key")
	}
	// Device role: no certificate loaded at all, so it stays not-loaded.
	if got.DeviceCASubject != "" || got.DeviceCAFingerprint != "" {
		t.Errorf("device subject/fingerprint not empty with no device certificate: %q / %q", got.DeviceCASubject, got.DeviceCAFingerprint)
	}
}
