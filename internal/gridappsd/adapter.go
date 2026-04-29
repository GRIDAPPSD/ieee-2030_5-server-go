package gridappsd

import (
	"context"
	"fmt"
	"log"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

// DeviceConfig holds the IEEE 2030.5 configuration for a discovered device.
// Mirrors the Python DeviceConfiguration dataclass.
type DeviceConfig struct {
	ID   string // Equipment mRID
	Name string // Equipment name
	LFDI string // From certificate
	SFDI string // From certificate
	PIN  int    // Default PIN
	DERs []string // DER descriptions/names
}

// Adapter bridges GridAPPS-D CIM models to IEEE 2030.5 EndDevices.
type Adapter struct {
	cfg       GridAPPSDConfig
	cimClient *CIMClient
}

// NewAdapter creates a GridAPPS-D adapter.
func NewAdapter(cfg GridAPPSDConfig) (*Adapter, error) {
	return &Adapter{cfg: cfg}, nil
}

// Connect establishes the CIM-Graph gRPC connection.
func (a *Adapter) Connect(ctx context.Context) error {
	client, err := NewCIMClient(a.cfg.CIMGraphAddr)
	if err != nil {
		return fmt.Errorf("connect to CIM-Graph: %w", err)
	}

	health, err := client.HealthCheck(ctx)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("CIM-Graph health check: %w", err)
	}
	if !health.Healthy {
		_ = client.Close()
		return fmt.Errorf("CIM-Graph unhealthy: %s", health.DatabaseType)
	}

	log.Printf("gridappsd: connected to CIM-Graph (%s, profile: %s)",
		health.DatabaseType, health.CimProfile)

	a.cimClient = client
	return nil
}

// Close disconnects from CIM-Graph.
func (a *Adapter) Close() {
	if a.cimClient != nil {
		_ = a.cimClient.Close()
	}
}

// DiscoverDevices queries CIM-Graph for DER equipment and returns
// IEEE 2030.5 device configurations ready for EndDevice creation.
func (a *Adapter) DiscoverDevices(ctx context.Context) ([]DeviceConfig, error) {
	if a.cimClient == nil {
		return nil, fmt.Errorf("not connected — call Connect() first")
	}

	feederID := a.cfg.FeederID
	if feederID == "" {
		return nil, fmt.Errorf("feeder_id not configured")
	}

	devices, err := a.cimClient.DiscoverDERDevices(ctx, feederID)
	if err != nil {
		return nil, fmt.Errorf("discover DER devices: %w", err)
	}

	var configs []DeviceConfig
	for _, dev := range devices {
		config := DeviceConfig{
			ID:   dev.MRID,
			Name: dev.Name,
			PIN:  a.cfg.DefaultPIN,
		}

		// Collect DER names from child units
		for _, unit := range dev.Units {
			config.DERs = append(config.DERs, fmt.Sprintf("%s (%s)", unit.Name, unit.UnitType))
		}
		if len(config.DERs) == 0 {
			config.DERs = []string{dev.Name}
		}

		configs = append(configs, config)
	}

	log.Printf("gridappsd: discovered %d DER devices from feeder %s", len(configs), feederID)
	return configs, nil
}

// CreateEndDevices generates certificates and creates IEEE 2030.5 EndDevice
// objects for each discovered device.
func CreateEndDevices(devices []DeviceConfig, ca *certs.CAInfo) ([]sep2.EndDevice, error) {
	var endDevices []sep2.EndDevice

	for _, dev := range devices {
		// Generate device certificate
		certPEM, _, err := certs.GenerateDeviceCert(ca.Cert, ca.Key, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceTypeGeneric,
			HWSerialNum: dev.ID,
		})
		if err != nil {
			log.Printf("gridappsd: cert generation failed for %s: %v", dev.ID, err)
			continue
		}

		// Derive SFDI/LFDI from cert
		cert, err := certs.ParseCertificatePEM(certPEM)
		if err != nil {
			continue
		}

		enabled := true
		edev := sep2.EndDevice{
			SFDI:    sepTLS.SFDI(cert),
			LFDI:    sepTLS.LFDI(cert),
			Enabled: &enabled,
		}
		edev.ChangedTime = 0 // will be set on registration

		endDevices = append(endDevices, edev)
	}

	return endDevices, nil
}
