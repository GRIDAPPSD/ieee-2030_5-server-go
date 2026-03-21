package gridappsd

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/craig8/ieee-2030_5-go/internal/gridappsd/pb"
)

// CIMClient wraps the CIM-Graph gRPC service for querying CIM models.
type CIMClient struct {
	conn   *grpc.ClientConn
	client pb.CIMGraphServiceClient
}

// NewCIMClient connects to the CIM-Graph gRPC server.
func NewCIMClient(addr string) (*CIMClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to CIM-Graph at %s: %w", addr, err)
	}

	return &CIMClient{
		conn:   conn,
		client: pb.NewCIMGraphServiceClient(conn),
	}, nil
}

// Close disconnects from the gRPC server.
func (c *CIMClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// HealthCheck verifies the CIM-Graph service is alive.
func (c *CIMClient) HealthCheck(ctx context.Context) (*pb.HealthCheckResponse, error) {
	return c.client.HealthCheck(ctx, &pb.HealthCheckRequest{})
}

// DERDevice represents a discovered DER from the CIM model.
type DERDevice struct {
	MRID    string
	Name    string
	RatedS  float64 // VA
	RatedU  float64 // V
	P       float64 // W
	Q       float64 // VAr
	Units   []DERUnit
}

// DERUnit represents a child unit of a PowerElectronicsConnection.
type DERUnit struct {
	MRID     string
	Name     string
	UnitType string // "PhotovoltaicUnit", "BatteryUnit", etc.
	MaxP     float64
	MinP     float64
}

// DiscoverDERDevices loads a feeder model and returns all DER equipment.
// This is the primary method for IEEE 2030.5 device discovery from GridAPPS-D.
func (c *CIMClient) DiscoverDERDevices(ctx context.Context, feederMRID string) ([]DERDevice, error) {
	// First load the feeder model
	_, err := c.client.GetFeederModel(ctx, &pb.GetFeederModelRequest{
		FeederMrid: feederMRID,
	})
	if err != nil {
		return nil, fmt.Errorf("load feeder model: %w", err)
	}

	// Query PowerElectronicsConnections
	resp, err := c.client.GetPowerElectronicsConnections(ctx, &pb.GetPECRequest{
		FeederMrid: feederMRID,
	})
	if err != nil {
		return nil, fmt.Errorf("get PECs: %w", err)
	}

	var devices []DERDevice
	for _, pec := range resp.Connections {
		dev := DERDevice{
			MRID:   pec.Mrid,
			Name:   pec.Name,
			RatedS: pec.RatedS,
			RatedU: pec.RatedU,
			P:      pec.P,
			Q:      pec.Q,
		}
		for _, unit := range pec.Units {
			dev.Units = append(dev.Units, DERUnit{
				MRID:     unit.Mrid,
				Name:     unit.Name,
				UnitType: unit.UnitType,
				MaxP:     unit.MaxP,
				MinP:     unit.MinP,
			})
		}
		devices = append(devices, dev)
	}

	return devices, nil
}

// GetEquipment returns details for a specific equipment by mRID.
func (c *CIMClient) GetEquipment(ctx context.Context, mrid string) (*pb.EquipmentResponse, error) {
	return c.client.GetEquipment(ctx, &pb.GetEquipmentRequest{Mrid: mrid})
}

// ExecuteSPARQL runs a raw SPARQL query.
func (c *CIMClient) ExecuteSPARQL(ctx context.Context, query string) (string, error) {
	resp, err := c.client.ExecuteSPARQL(ctx, &pb.SPARQLRequest{Query: query})
	if err != nil {
		return "", err
	}
	return resp.ResultJson, nil
}
