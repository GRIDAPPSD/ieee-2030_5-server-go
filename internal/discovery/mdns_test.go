package discovery_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/discovery"
)

func TestRegister(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "test-server",
		Port:     443,
		Path:     "/dcap",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer reg.Close()

	if !reg.IsActive() {
		t.Error("registration should be active")
	}
}

func TestRegisterDefaultPath(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "test-default",
		Port:     8443,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer reg.Close()

	if !reg.IsActive() {
		t.Error("registration should be active")
	}
}

func TestRegisterInvalidPort(t *testing.T) {
	_, err := discovery.Register(discovery.Config{
		Hostname: "test",
		Port:     0,
	})
	if err == nil {
		t.Error("should fail with invalid port")
	}
}

func TestClose(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "test-close",
		Port:     443,
	})
	if err != nil {
		t.Fatal(err)
	}

	reg.Close()
	if reg.IsActive() {
		t.Error("should be inactive after Close")
	}
}

func TestCloseIdempotent(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "test-idempotent",
		Port:     443,
	})
	if err != nil {
		t.Fatal(err)
	}

	reg.Close()
	reg.Close() // should not panic
}

// #246: RegisterAdmin must skip — without erroring — when the admin
// listener is bound to a loopback interface. The published A record
// would be unreachable off-box, so advertising it is worse than not.
func TestRegisterAdminSkipsLoopbackIPv4(t *testing.T) {
	reg, err := discovery.RegisterAdmin(discovery.AdminConfig{Listen: "127.0.0.1:8444"})
	if err != nil {
		t.Fatalf("loopback bind should not error, got %v", err)
	}
	if reg != nil {
		reg.Close()
		t.Error("loopback bind should return nil registration (skipped)")
	}
}

func TestRegisterAdminSkipsLoopbackIPv6(t *testing.T) {
	reg, err := discovery.RegisterAdmin(discovery.AdminConfig{Listen: "[::1]:8444"})
	if err != nil {
		t.Fatalf("loopback IPv6 bind should not error, got %v", err)
	}
	if reg != nil {
		reg.Close()
		t.Error("loopback IPv6 bind should return nil registration (skipped)")
	}
}

func TestRegisterAdminRejectsBadListen(t *testing.T) {
	_, err := discovery.RegisterAdmin(discovery.AdminConfig{Listen: "not-an-address"})
	if err == nil {
		t.Error("malformed listen string should error")
	}
}

func TestRegisterAdminRejectsHostNotIP(t *testing.T) {
	_, err := discovery.RegisterAdmin(discovery.AdminConfig{Listen: "example.com:8444"})
	if err == nil {
		t.Error("non-IP host should error")
	}
}

func TestAdminHostnameIsHyphenated(t *testing.T) {
	// #246: hostname is `ieee2030-5.local`, NOT `2030.5.local`. The
	// dot-prefixed form would put a digit-dot-digit sequence inside a
	// label, which is invalid DNS.
	if discovery.AdminHostname != "ieee2030-5.local" {
		t.Errorf("AdminHostname = %q, want ieee2030-5.local", discovery.AdminHostname)
	}
}
