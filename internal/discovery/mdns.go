package discovery

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/hashicorp/mdns"
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
	server *mdns.Server
	config Config
}

// Register advertises the IEEE 2030.5 server via DNS-SD (mDNS).
// TXT records include: path to /dcap, HTTPS port, and txtvers.
func Register(cfg Config) (*Registration, error) {
	if cfg.Path == "" {
		cfg.Path = "/dcap"
	}
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("mdns: invalid port %d", cfg.Port)
	}
	if cfg.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "ieee2030-5-server"
		}
		cfg.Hostname = hostname
	}

	txtRecords := []string{
		fmt.Sprintf("path=%s", cfg.Path),
		fmt.Sprintf("https=%d", cfg.Port),
		"txtvers=1",
	}

	// Find the first non-loopback IPv4 address for the service
	ips := []net.IP{}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP)
			}
		}
	}
	if len(ips) == 0 {
		ips = append(ips, net.ParseIP("127.0.0.1"))
	}

	service, err := mdns.NewMDNSService(
		cfg.Hostname,         // instance name
		ServiceType,          // service type
		"",                   // domain (default = "local.")
		"",                   // host (default = hostname)
		cfg.Port,             // port
		ips,                  // IPs
		txtRecords,           // TXT records
	)
	if err != nil {
		return nil, fmt.Errorf("mdns: create service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("mdns: start server: %w", err)
	}

	log.Printf("DNS-SD: registered %s on %s:%d TXT=%v",
		ServiceType, cfg.Hostname, cfg.Port, txtRecords)

	return &Registration{server: server, config: cfg}, nil
}

// Close deregisters the mDNS service and stops the server.
func (r *Registration) Close() {
	if r.server != nil {
		log.Printf("DNS-SD: deregistering %s", ServiceType)
		r.server.Shutdown()
		r.server = nil
	}
}

// IsActive returns whether the registration is currently active.
func (r *Registration) IsActive() bool {
	return r.server != nil
}
