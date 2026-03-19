package discovery

import (
	"fmt"
	"log"
)

// ServiceType is the DNS-SD service type for IEEE 2030.5.
const ServiceType = "_smartenergy._tcp"

// Config holds mDNS registration parameters.
type Config struct {
	Hostname string // server hostname
	Port     int    // HTTPS port
	Path     string // path to dcap resource (default "/dcap")
}

// Registration represents an active mDNS service registration.
type Registration struct {
	config Config
	active bool
}

// Register advertises the IEEE 2030.5 server via DNS-SD.
// TXT records include: path, https port, and extensibility level.
//
// Current implementation logs the registration. Full mDNS support
// requires github.com/hashicorp/mdns (deferred to avoid adding
// dependencies until needed).
func Register(cfg Config) (*Registration, error) {
	if cfg.Path == "" {
		cfg.Path = "/dcap"
	}
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("mdns: invalid port %d", cfg.Port)
	}

	txtRecords := []string{
		fmt.Sprintf("path=%s", cfg.Path),
		fmt.Sprintf("https=%d", cfg.Port),
		"txtvers=1",
	}

	log.Printf("DNS-SD: registering %s on %s:%d TXT=%v",
		ServiceType, cfg.Hostname, cfg.Port, txtRecords)

	return &Registration{config: cfg, active: true}, nil
}

// Close deregisters the mDNS service.
func (r *Registration) Close() {
	if r.active {
		log.Printf("DNS-SD: deregistering %s", ServiceType)
		r.active = false
	}
}

// IsActive returns whether the registration is currently active.
func (r *Registration) IsActive() bool {
	return r.active
}
