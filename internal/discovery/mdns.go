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

// AdminServiceType is the DNS-SD service type used to publish the admin
// listener. We piggyback on _http._tcp so the registration carries an A
// record bound to AdminHostname; that's the load-bearing artifact (LAN
// peers can resolve `ieee2030-5.local` once the publisher is up). The SRV
// record advertised under this service type is a useful side effect for
// any client that wants to enumerate admin endpoints, but the canonical
// surface is the hostname.
const AdminServiceType = "_http._tcp"

// AdminHostname is the LAN-discovery hostname for the admin surface
// (IEEE-133). It is intentionally not configurable: a stable hostname is
// the whole point. Hyphenated form because `2030.5.local` is invalid DNS
// (a label may not begin with a digit-dot-digit sequence mid-label).
const AdminHostname = "ieee2030-5.local"

// AdminInstanceName is the DNS-SD instance name advertised under
// AdminServiceType. It is unique within a LAN segment (collisions across
// two co-located IEEE 2030.5 servers are not handled — that's a deferred
// problem, not a today problem).
const AdminInstanceName = "ieee2030-5-admin"

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
		cfg.Hostname, // instance name
		ServiceType,  // service type
		"",           // domain (default = "local.")
		"",           // host (default = hostname)
		cfg.Port,     // port
		ips,          // IPs
		txtRecords,   // TXT records
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
		if err := r.server.Shutdown(); err != nil {
			log.Printf("DNS-SD: shutdown error: %v", err)
		}
		r.server = nil
	}
}

// IsActive returns whether the registration is currently active.
func (r *Registration) IsActive() bool {
	return r.server != nil
}

// AdminConfig holds parameters for an admin-listener mDNS advertisement.
type AdminConfig struct {
	// Listen is the admin listener bind address (e.g. ":8444",
	// "0.0.0.0:8444", "127.0.0.1:8444"). The port is resolved from this
	// string; the IP determines whether the advertisement makes sense at
	// all (loopback binds skip — see RegisterAdmin).
	Listen string
}

// RegisterAdmin advertises the admin listener as `ieee2030-5.local` on
// the LAN, gated on the caller already having decided to enable mDNS
// (config.EnableMDNS). The hostname is fixed (IEEE-133); LAN discovery
// needs a stable name, not a configurable one.
//
// The advertisement is skipped — without an error — when the admin
// listener is bound to a loopback interface, because the published
// address would be unreachable off-box. The caller treats a (nil, nil)
// return as "skipped, continue normally".
func RegisterAdmin(cfg AdminConfig) (*Registration, error) {
	host, portStr, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("mdns admin: parse listen %q: %w", cfg.Listen, err)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, fmt.Errorf("mdns admin: parse port %q: %w", portStr, err)
	}

	ip := net.ParseIP(host)
	switch {
	case host == "":
		// Unspecified bind ("" or ":<port>"): pick an outbound interface IP.
		ip, err = firstNonLoopbackIPv4()
		if err != nil {
			return nil, fmt.Errorf("mdns admin: %w", err)
		}
	case ip == nil:
		return nil, fmt.Errorf("mdns admin: listen host %q is not an IP", host)
	case ip.IsUnspecified():
		ip, err = firstNonLoopbackIPv4()
		if err != nil {
			return nil, fmt.Errorf("mdns admin: %w", err)
		}
	case ip.IsLoopback():
		log.Printf("DNS-SD admin: listen %s is loopback; skipping %s advertisement (would be unreachable off-box)",
			cfg.Listen, AdminHostname)
		return nil, nil
	}

	service, err := mdns.NewMDNSService(
		AdminInstanceName,     // instance name
		AdminServiceType,      // service type (_http._tcp)
		"",                    // domain (default = "local.")
		AdminHostname+".",     // host (FQDN; mdns lib requires the trailing dot)
		port,                  // port
		[]net.IP{ip},          // IPs (used for the A/AAAA record bound to AdminHostname)
		[]string{"txtvers=1"}, // TXT records
	)
	if err != nil {
		return nil, fmt.Errorf("mdns admin: create service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("mdns admin: start server: %w", err)
	}

	log.Printf("DNS-SD admin: registered %s as %s on %s:%d",
		AdminServiceType, AdminHostname, ip, port)

	return &Registration{server: server, config: Config{
		Hostname: AdminHostname,
		Port:     port,
		Path:     "/",
	}}, nil
}

// firstNonLoopbackIPv4 returns the first non-loopback IPv4 address on
// any up interface. Used when the admin listener binds an unspecified
// address — the published mDNS A record needs a routable IP, not 0.0.0.0.
func firstNonLoopbackIPv4() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("interface addrs: %w", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4, nil
		}
	}
	return nil, fmt.Errorf("no non-loopback IPv4 address found")
}
