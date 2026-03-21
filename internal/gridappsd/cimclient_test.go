package gridappsd_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/craig8/ieee-2030_5-go/internal/gridappsd"
	pb "github.com/craig8/ieee-2030_5-go/internal/gridappsd/pb"
)

// mockCIMGraphServer implements the CIMGraphService for testing.
type mockCIMGraphServer struct {
	pb.UnimplementedCIMGraphServiceServer
}

func (m *mockCIMGraphServer) ListFeeders(ctx context.Context, req *pb.ListFeedersRequest) (*pb.ListFeedersResponse, error) {
	return &pb.ListFeedersResponse{
		Feeders: []*pb.Feeder{
			{Mrid: "_AAA-BBB-CCC", Name: "IEEE 13 Node", Region: "Region 1", Subregion: "Sub 1", Substation: "Station A"},
			{Mrid: "_DDD-EEE-FFF", Name: "IEEE 123 Node", Region: "Region 1", Subregion: "Sub 2", Substation: "Station B"},
			{Mrid: "_GGG-HHH-III", Name: "Test Feeder", Region: "Region 2", Subregion: "Sub 3", Substation: "Station C"},
		},
	}, nil
}

func (m *mockCIMGraphServer) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return &pb.HealthCheckResponse{
		Healthy:      true,
		DatabaseType: "mock",
		CimProfile:   "test",
		Version:      "0.4.3",
	}, nil
}

func (m *mockCIMGraphServer) GetFeederModel(ctx context.Context, req *pb.GetFeederModelRequest) (*pb.FeederModelResponse, error) {
	return &pb.FeederModelResponse{
		FeederMrid:     req.FeederMrid,
		FeederName:     "Test Feeder",
		EquipmentCount: 3,
		EquipmentTypes: []string{"PowerElectronicsConnection", "PhotovoltaicUnit"},
	}, nil
}

func (m *mockCIMGraphServer) GetPowerElectronicsConnections(ctx context.Context, req *pb.GetPECRequest) (*pb.PECListResponse, error) {
	return &pb.PECListResponse{
		Connections: []*pb.PowerElectronicsConnection{
			{
				Mrid:   "pec-001",
				Name:   "Solar Inverter 1",
				RatedS: 10000,
				RatedU: 240,
				P:      8500,
				Units: []*pb.PowerElectronicsUnit{
					{Mrid: "pv-001", Name: "PV Array 1", UnitType: "PhotovoltaicUnit", MaxP: 10000},
				},
			},
			{
				Mrid:   "pec-002",
				Name:   "Battery Storage 1",
				RatedS: 5000,
				RatedU: 240,
				Units: []*pb.PowerElectronicsUnit{
					{Mrid: "bat-001", Name: "Battery 1", UnitType: "BatteryUnit", MaxP: 5000, MinP: -5000},
				},
			},
		},
	}, nil
}

func startMockServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := grpc.NewServer()
	pb.RegisterCIMGraphServiceServer(srv, &mockCIMGraphServer{})

	go srv.Serve(lis)
	t.Cleanup(func() { srv.Stop() })

	return lis.Addr().String()
}

func TestCIMClientHealthCheck(t *testing.T) {
	addr := startMockServer(t)
	client, err := gridappsd.NewCIMClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	resp, err := client.HealthCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Healthy {
		t.Error("should be healthy")
	}
	if resp.DatabaseType != "mock" {
		t.Errorf("database = %q", resp.DatabaseType)
	}
}

func TestCIMClientDiscoverDERDevices(t *testing.T) {
	addr := startMockServer(t)
	client, err := gridappsd.NewCIMClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	devices, err := client.DiscoverDERDevices(context.Background(), "test-feeder-123")
	if err != nil {
		t.Fatal(err)
	}

	if len(devices) != 2 {
		t.Fatalf("device count = %d, want 2", len(devices))
	}

	// Check first device (PV inverter)
	if devices[0].Name != "Solar Inverter 1" {
		t.Errorf("device[0].Name = %q", devices[0].Name)
	}
	if devices[0].RatedS != 10000 {
		t.Errorf("device[0].RatedS = %f", devices[0].RatedS)
	}
	if len(devices[0].Units) != 1 {
		t.Fatalf("device[0] units = %d", len(devices[0].Units))
	}
	if devices[0].Units[0].UnitType != "PhotovoltaicUnit" {
		t.Errorf("unit type = %q", devices[0].Units[0].UnitType)
	}

	// Check second device (battery)
	if devices[1].Name != "Battery Storage 1" {
		t.Errorf("device[1].Name = %q", devices[1].Name)
	}
	if devices[1].Units[0].UnitType != "BatteryUnit" {
		t.Errorf("unit type = %q", devices[1].Units[0].UnitType)
	}
	if devices[1].Units[0].MinP != -5000 {
		t.Errorf("battery MinP = %f (should be negative for discharge)", devices[1].Units[0].MinP)
	}
}

func TestAdapterDiscoverDevices(t *testing.T) {
	addr := startMockServer(t)

	cfg := gridappsd.DefaultGridAPPSDConfig()
	cfg.CIMGraphAddr = addr
	cfg.FeederID = "test-feeder-123"

	adapter, err := gridappsd.NewAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	if err := adapter.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	devices, err := adapter.DiscoverDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(devices) != 2 {
		t.Fatalf("discovered %d devices, want 2", len(devices))
	}

	if devices[0].ID != "pec-001" {
		t.Errorf("device[0].ID = %q", devices[0].ID)
	}
	if devices[0].PIN != 111115 {
		t.Errorf("device[0].PIN = %d", devices[0].PIN)
	}
	if len(devices[0].DERs) == 0 {
		t.Error("device[0] should have DER names")
	}
}

func TestAdapterNotConnected(t *testing.T) {
	cfg := gridappsd.DefaultGridAPPSDConfig()
	cfg.FeederID = "test"

	adapter, _ := gridappsd.NewAdapter(cfg)
	_, err := adapter.DiscoverDevices(context.Background())
	if err == nil {
		t.Error("should fail when not connected")
	}
}

func TestAdapterNoFeederID(t *testing.T) {
	addr := startMockServer(t)
	cfg := gridappsd.DefaultGridAPPSDConfig()
	cfg.CIMGraphAddr = addr
	// No FeederID set

	adapter, _ := gridappsd.NewAdapter(cfg)
	adapter.Connect(context.Background())

	_, err := adapter.DiscoverDevices(context.Background())
	if err == nil {
		t.Error("should fail without feeder_id")
	}
}

func TestCIMClientListFeeders(t *testing.T) {
	addr := startMockServer(t)
	client, err := gridappsd.NewCIMClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	feeders, err := client.ListFeeders(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(feeders) != 3 {
		t.Fatalf("feeder count = %d, want 3", len(feeders))
	}

	if feeders[0].Name != "IEEE 13 Node" {
		t.Errorf("feeders[0].Name = %q", feeders[0].Name)
	}
	if feeders[0].MRID != "_AAA-BBB-CCC" {
		t.Errorf("feeders[0].MRID = %q", feeders[0].MRID)
	}
	if feeders[0].Region != "Region 1" {
		t.Errorf("feeders[0].Region = %q", feeders[0].Region)
	}
	if feeders[0].Substation != "Station A" {
		t.Errorf("feeders[0].Substation = %q", feeders[0].Substation)
	}

	// Verify all feeders have data
	for i, f := range feeders {
		if f.MRID == "" || f.Name == "" {
			t.Errorf("feeder[%d] missing data: %+v", i, f)
		}
	}
}

// Test with direct gRPC to verify proto compatibility
func TestProtoDirectCall(t *testing.T) {
	addr := startMockServer(t)

	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := pb.NewCIMGraphServiceClient(conn)
	resp, err := client.GetFeederModel(context.Background(), &pb.GetFeederModelRequest{
		FeederMrid:   "test-123",
		DatabaseType: "blazegraph",
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.EquipmentCount != 3 {
		t.Errorf("equipment count = %d", resp.EquipmentCount)
	}
	if resp.FeederName != "Test Feeder" {
		t.Errorf("feeder name = %q", resp.FeederName)
	}
}
