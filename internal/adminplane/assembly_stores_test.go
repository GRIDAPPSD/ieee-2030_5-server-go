package adminplane

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestStoresFromAssemblyHoldsTheSameInstances checks the admin plane reads
// and writes the protocol router's own stores, not copies of them.
func TestStoresFromAssemblyHoldsTheSameInstances(t *testing.T) {
	fsas := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	controls := memory.NewDERControlStore()
	in := &assembly.Stores{
		EndDevices:                        memory.NewEndDeviceStore(),
		EndDeviceManagers:                 memory.NewEndDeviceManagementStore(),
		DERPrograms:                       memory.NewDERProgramStore(),
		DERControls:                       controls,
		DERControlLifecycles:              memory.NewScopedStore[dercontrol.LifecycleRecord](),
		FSAs:                              fsas,
		AdminFSAs:                         memory.NewAdminFSAStore(),
		FlowReservationRequests:           memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses:          memory.NewScopedStore[sep2.FlowReservationResponse](),
		FlowReservationResponseLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
	}
	out, err := StoresFromAssembly(in)
	if err != nil {
		t.Fatalf("StoresFromAssembly: %v", err)
	}
	same := []struct {
		name string
		got  any
		want any
	}{
		{"EndDevices", out.EndDevices, in.EndDevices},
		{"EndDeviceManagers", out.EndDeviceManagers, in.EndDeviceManagers},
		{"DERPrograms", out.DERPrograms, in.DERPrograms},
		{"DERControls", out.DERControls, controls},
		{"DERControlLifecycles", out.DERControlLifecycles, in.DERControlLifecycles},
		{"FSAs", out.FSAs, fsas},
		{"AdminFSAs", out.AdminFSAs, in.AdminFSAs},
		{"FlowReservationRequests", out.FlowReservationRequests, in.FlowReservationRequests},
		{"FlowReservationResponses", out.FlowReservationResponses, in.FlowReservationResponses},
		{"FlowReservationResponseLifecycles", out.FlowReservationResponseLifecycles, in.FlowReservationResponseLifecycles},
	}
	for _, s := range same {
		if s.got != s.want {
			t.Errorf("%s is not the assembly's instance", s.name)
		}
	}
}

func TestStoresFromAssemblyOnConcreteTypeMismatch(t *testing.T) {
	notConcrete := memory.WithDependents(store.ScopedStore[sep2.FunctionSetAssignments](memory.NewScopedStore[sep2.FunctionSetAssignments]()), memory.NewScopedStore[dercontrol.LifecycleRecord]())
	if _, err := StoresFromAssembly(&assembly.Stores{AdminFSAs: memory.NewAdminFSAStore(), FSAs: notConcrete}); err == nil {
		t.Error("FSAs of another type with AdminFSAs set: no error, want one")
	}
	if _, err := StoresFromAssembly(&assembly.Stores{AdminFSAs: memory.NewAdminFSAStore()}); err == nil {
		t.Error("no FSAs with AdminFSAs set: no error, want one")
	}

	out, err := StoresFromAssembly(&assembly.Stores{DERControls: memory.NewScopedStore[sep2.DERControl]()})
	if err != nil {
		t.Fatalf("StoresFromAssembly: %v", err)
	}
	if out.DERControls != nil {
		t.Error("a DERControls that is not *memory.DERControlStore was kept")
	}
	if newAdminDERControlHandler(out) != nil {
		t.Error("DER control handler built without a DER control store")
	}
}
