// Package embed_smoke_test verifies that the public packages
// (pkg/server, pkg/certs, pkg/identity) are importable as a
// downstream consumer (the gridappsd-2030_5-go bridge) would
// import them.
//
// This is a compile-only test. If the listed identifiers are not
// reachable, the package fails to build and the test suite breaks.
package embed_smoke_test

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/identity"
	pkgserver "github.com/GRIDAPPSD/ieee-2030_5-go/pkg/server"
)

// TestBridgeImportsAvailable references each public symbol the bridge
// is expected to consume. If any symbol is missing or its signature
// changes incompatibly, this test stops compiling.
func TestBridgeImportsAvailable(t *testing.T) {
	// pkg/identity: pure functions over *x509.Certificate.
	var c *x509.Certificate
	_ = func() string { return identity.LFDI(c) }
	_ = func() string { return identity.SFDI(c) }
	_ = func() [32]byte { return identity.Fingerprint(c) }
	_ = identity.ValidateSFDI
	_ = identity.FormatSFDI

	// pkg/certs: cert generation + load helpers.
	_ = certs.GenerateCA
	_ = certs.GenerateServerCert
	_ = certs.GenerateDeviceCert
	_ = certs.GenerateAdminCert
	_ = certs.LoadCA
	_ = certs.ParseCertificatePEM
	_ = certs.ParseKeyPEM
	_ = certs.CAOptions{}
	_ = certs.ServerCertOptions{}
	_ = certs.AdminCertOptions{}
	_ = certs.DeviceCertOptions{
		DeviceType: certs.DeviceTypeGeneric,
		ValidYears: 1,
		CommonName: "smoke",
	}

	// pkg/server: lifecycle.
	_ = func() (*pkgserver.Server, error) { return pkgserver.New(pkgserver.Config{}) }
	var srv *pkgserver.Server
	_ = func() error { return srv.Start(context.Background()) }
	_ = func() error { return srv.Shutdown(context.Background()) }
}
