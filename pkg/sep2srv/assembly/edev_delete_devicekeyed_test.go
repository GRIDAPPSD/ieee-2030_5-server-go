package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestEndDeviceDelete_CascadesTheDeviceKeyedFour is the end-to-end pin for
// the first slice of GRIDAPPSD/ieee-2030_5-server-go#721: DELETE
// /edev/{id} must not leave the device's Configuration, DeviceStatus,
// PowerStatus or FunctionSetAssignments records behind under the dead key,
// the same shape TestEndDeviceDelete_CascadesFlowReservationAndLogEventRecords
// (edev_delete_cascade_test.go) already pins for #701.
//
// One subtest per record kind, table-driven since the four cases differ
// only in which store they seed and count.
func TestEndDeviceDelete_CascadesTheDeviceKeyedFour(t *testing.T) {
	t.Parallel()

	const singletonKey = "default" // pkg/sep2srv/handlers/singleton.SingletonKey

	cases := []struct {
		name  string
		seed  func(ctx context.Context, stores *assembly.Stores) error
		count func(ctx context.Context, stores *assembly.Stores) (uint32, error)
	}{
		{
			name: "Configuration",
			seed: func(ctx context.Context, stores *assembly.Stores) error {
				return stores.Configurations.Create(ctx, "e1", singletonKey, sep2.Configuration{})
			},
			count: func(ctx context.Context, stores *assembly.Stores) (uint32, error) {
				return stores.Configurations.Count(ctx, "e1")
			},
		},
		{
			name: "DeviceStatus",
			seed: func(ctx context.Context, stores *assembly.Stores) error {
				return stores.DeviceStatuses.Create(ctx, "e1", singletonKey, sep2.DeviceStatus{})
			},
			count: func(ctx context.Context, stores *assembly.Stores) (uint32, error) {
				return stores.DeviceStatuses.Count(ctx, "e1")
			},
		},
		{
			name: "PowerStatus",
			seed: func(ctx context.Context, stores *assembly.Stores) error {
				return stores.PowerStatuses.Create(ctx, "e1", singletonKey, sep2.PowerStatus{})
			},
			count: func(ctx context.Context, stores *assembly.Stores) (uint32, error) {
				return stores.PowerStatuses.Count(ctx, "e1")
			},
		},
		{
			name: "FunctionSetAssignments",
			seed: func(ctx context.Context, stores *assembly.Stores) error {
				return stores.FSAs.Create(ctx, "e1", "fsa-1", sep2.FunctionSetAssignments{})
			},
			count: func(ctx context.Context, stores *assembly.Stores) (uint32, error) {
				return stores.FSAs.Count(ctx, "e1")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stores := testStores()
			seedOwnedDevices(t, stores.EndDevices, "e1")
			handler, _ := assembly.BuildProtocolRouter(
				assembly.RouterConfig{},
				stores,
				testAuthPolicy(),
				testSFDI, testLFDI,
				nil,
			)
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			ctx := context.Background()
			if err := tc.seed(ctx, stores); err != nil {
				t.Fatalf("seed %s: %v", tc.name, err)
			}

			// Control: the record exists under "e1" before the delete, so
			// the zero count asserted below means the cascade ran rather
			// than nothing having been seeded.
			if n, err := tc.count(ctx, stores); err != nil || n != 1 {
				t.Fatalf("control: %s under e1 = %d, %v, want 1, nil", tc.name, n, err)
			}

			req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
			if err != nil {
				t.Fatalf("new DELETE request: %v", err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("DELETE /edev/e1: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("DELETE /edev/e1 status = %d, want 204", resp.StatusCode)
			}

			if n, err := tc.count(ctx, stores); err != nil || n != 0 {
				t.Errorf("%s under the dead key e1 = %d, %v, want 0, nil", tc.name, n, err)
			}
		})
	}
}

// TestEndDeviceDelete_NewOccupantOfADeadKeyStartsWithNoFunctionSetAssignments
// is the issue's own probe applied to the one device-keyed member with a
// list a client can walk: seed a new device at the reused key (same caller
// identity, so the ownership gate still admits it) and confirm it is not
// served the dead device's FunctionSetAssignments through its own link.
func TestEndDeviceDelete_NewOccupantOfADeadKeyStartsWithNoFunctionSetAssignments(t *testing.T) {
	t.Parallel()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	if err := stores.FSAs.Create(ctx, "e1", "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed function set assignment: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /edev/e1 status = %d, want 204", resp.StatusCode)
	}

	newDev := sep2.EndDevice{SFDI: "9999999999", LFDI: testLFDI}
	newDev.Href = "/edev/e1"
	if err := stores.EndDevices.Create(ctx, "e1", newDev); err != nil {
		t.Fatalf("recreate device at the reused key: %v", err)
	}

	_, fsaBody := getBytes(t, srv, "/edev/e1/fsa")
	var fsaList sep2.FunctionSetAssignmentsList
	if err := xml.Unmarshal(fsaBody, &fsaList); err != nil {
		t.Fatalf("unmarshal FunctionSetAssignmentsList: %v", err)
	}
	if len(fsaList.FunctionSetAssignments) != 0 {
		t.Errorf("the reused key e1 was served %d function set assignment(s) from the dead device, want 0",
			len(fsaList.FunctionSetAssignments))
	}
}

// TestEndDeviceDelete_ClearsTheAdminFSAAssignmentThroughTheAssembledRouter
// pins GRIDAPPSD/ieee-2030_5-server-go#721's admin-plane cascade through the
// real router assembly.go wires, not just DeviceKeyedCascadeEndDeviceStore
// directly. testStores() leaves AdminFSAs nil, so no other test in this
// package had ever exercised deviceKeyedCascadeEndDevices's AdminFSAs
// branch before this one wired it explicitly: passing nil there stayed
// green everywhere else.
func TestEndDeviceDelete_ClearsTheAdminFSAAssignmentThroughTheAssembledRouter(t *testing.T) {
	t.Parallel()

	stores := testStores()
	stores.AdminFSAs = memory.NewAdminFSAStore()
	seedOwnedDevices(t, stores.EndDevices, "e1")

	ctx := context.Background()
	if err := stores.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := stores.AdminFSAs.AssignDevice(ctx, "fsa-1", "e1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Control: the assignment is really there before the delete.
	if devs := stores.AdminFSAs.Devices(ctx, "fsa-1"); len(devs) != 1 || devs[0] != "e1" {
		t.Fatalf("control: adminFSAs.Devices(fsa-1) = %v, want [e1]", devs)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /edev/e1 status = %d, want 204", resp.StatusCode)
	}

	if devs := stores.AdminFSAs.Devices(ctx, "fsa-1"); len(devs) != 0 {
		t.Errorf("adminFSAs.Devices(fsa-1) after DELETE through the assembled router = %v, want none", devs)
	}
}

// TestEndDeviceDelete_ClearsTheAdminFSAAssignmentWhenItIsTheOnlyDeviceKeyedFamilyWired
// pins the deviceKeyedCascadeEndDevices gate itself: Configurations,
// DeviceStatuses, PowerStatuses and FSAs are all left nil here, so the
// four-term "nothing wired" check only stays false because of the
// AdminFSAs term. Dropping that term from the gate would make it decide
// nothing is wired, skip building DeviceKeyedCascadeEndDeviceStore
// entirely, and leave the admin link uncascaded even though AdminFSAs is
// present (GRIDAPPSD/ieee-2030_5-server-go#721).
func TestEndDeviceDelete_ClearsTheAdminFSAAssignmentWhenItIsTheOnlyDeviceKeyedFamilyWired(t *testing.T) {
	t.Parallel()

	stores := testStores()
	stores.Configurations = nil
	stores.DeviceStatuses = nil
	stores.PowerStatuses = nil
	stores.FSAs = nil
	stores.AdminFSAs = memory.NewAdminFSAStore()
	seedOwnedDevices(t, stores.EndDevices, "e1")

	ctx := context.Background()
	if err := stores.AdminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := stores.AdminFSAs.AssignDevice(ctx, "fsa-1", "e1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Control: the assignment is really there before the delete.
	if devs := stores.AdminFSAs.Devices(ctx, "fsa-1"); len(devs) != 1 || devs[0] != "e1" {
		t.Fatalf("control: adminFSAs.Devices(fsa-1) = %v, want [e1]", devs)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /edev/e1 status = %d, want 204", resp.StatusCode)
	}

	if devs := stores.AdminFSAs.Devices(ctx, "fsa-1"); len(devs) != 0 {
		t.Errorf("adminFSAs.Devices(fsa-1) after DELETE with AdminFSAs the only wired family = %v, want none: "+
			"the gate must still build the cascade decorator", devs)
	}
}
