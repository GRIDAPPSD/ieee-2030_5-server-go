package discovery_test

import (
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/discovery"
)

func TestRegister(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "localhost",
		Port:     443,
		Path:     "/dcap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IsActive() {
		t.Error("registration should be active")
	}
}

func TestRegisterDefaultPath(t *testing.T) {
	reg, err := discovery.Register(discovery.Config{
		Hostname: "localhost",
		Port:     443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IsActive() {
		t.Error("registration should be active")
	}
}

func TestRegisterInvalidPort(t *testing.T) {
	_, err := discovery.Register(discovery.Config{
		Hostname: "localhost",
		Port:     0,
	})
	if err == nil {
		t.Error("should fail with invalid port")
	}
}

func TestClose(t *testing.T) {
	reg, _ := discovery.Register(discovery.Config{
		Hostname: "localhost",
		Port:     443,
	})

	reg.Close()
	if reg.IsActive() {
		t.Error("should be inactive after Close")
	}
}

func TestCloseIdempotent(t *testing.T) {
	reg, _ := discovery.Register(discovery.Config{
		Hostname: "localhost",
		Port:     443,
	})

	reg.Close()
	reg.Close() // should not panic
}
