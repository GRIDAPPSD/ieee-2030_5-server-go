package sep2server

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// NewStores builds a fully populated, pure in-memory store set.
//
// Every field is non-nil. That matters: core treats a nil store field as
// "skip those routes", so a partially populated set does not fail loudly, it
// serves a quietly smaller protocol surface. A consumer that wants persistence
// or pre-seeded contents builds its own set and passes it as [Config.Stores];
// this is the baseline that makes the simple case a one-liner.
//
// RegistrationPolicy is deliberately left at its zero value. That is
// fail-closed rather than degraded: with no PIN resolver wired, a device gets
// no Registration and no RegistrationLink at all, rather than one carrying a
// PIN this package invented. Registration PINs must not be derivable from
// device identity, so there is no defensible default to ship.
func NewStores() *assembly.Stores {
	// The response store removes its lifecycle records with it, so an EndDevice
	// delete leaves no cancel mark behind (#761).
	responseLifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
	s := &assembly.Stores{
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
		DERControls:              memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		DERControlLifecycles:     memory.NewScopedStore[dercontrol.LifecycleRecord](),
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
		FlowReservationResponses: memory.WithDependents(memory.NewScopedStore[sep2.FlowReservationResponse](), responseLifecycles),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),

		FlowReservationResponseLifecycles: responseLifecycles,
	}
	s.CommitmentLedger = sources.NewLedger(
		s.EndDevices, s.EndDeviceManagers,
		s.FlowReservationResponses, s.FlowReservationResponseLifecycles,
		s.DERControls, s.DERControlLifecycles,
	)
	return s
}
