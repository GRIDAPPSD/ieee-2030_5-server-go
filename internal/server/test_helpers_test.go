package server_test

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func newTestStores() *server.Stores {
	return &server.Stores{
		EndDevices:               memory.NewEndDeviceStore(),
		EndDeviceManagers:        memory.NewEndDeviceManagementStore(),
		EndDeviceIndexes:         memory.NewEndDeviceIndex(),
		Registrations:            memory.NewRegistrationStore(),
		MirrorUsagePoints:        memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings:      memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:                     memory.NewScopedStore[sep2.DER](),
		DERCapabilities:          memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:              memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:              memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:        memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:              memory.NewDERProgramStore(),
		DERControls:              memory.NewDERControlStore(),
		DERControlLifecycles:     dercontrol.NewLifecycleStore(),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		FSAs:                     memory.NewScopedStore[sep2.FunctionSetAssignments](),
		AdminFSAs:                memory.NewAdminFSAStore(),
		Subscriptions:            memory.NewSubscriptionStore(),
		UsagePoints:              memory.NewStore[sep2.UsagePoint](),
		MeterReadings:            memory.NewScopedStore[sep2.MeterReading](),
		Readings:                 memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:             memory.NewStore[sep2.ReadingType](),
		Configurations:           memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:           memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:                memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:            memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:        memory.NewStore[sep2.MessagingProgram](),
		TextMessages:             memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),

		FlowReservationResponseLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
		CommitmentLedger:                  commitment.NewLedger(noGrants{}, noControls{}),
	}
}

// noGrants and noControls are empty ledger sources: these tests only need a
// non-nil ledger to prove it is carried into the assembly.
type noGrants struct{}

func (noGrants) GrantsInFleet(context.Context, string) ([]commitment.Grant, error) { return nil, nil }
func (noGrants) Grant(context.Context, string) (commitment.Grant, error) {
	return commitment.Grant{}, store.ErrNotFound
}

type noControls struct{}

func (noControls) ControlsInFleet(context.Context, string) ([]commitment.Control, error) {
	return nil, nil
}
func (noControls) ExecutionsOf(context.Context, string) ([]commitment.Control, error) {
	return nil, nil
}
