package server

import (
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// Stores holds all resource stores for the server.
//
// Phase 2 (IEEESRV-002): the protocol-router half of this file
// (BuildProtocolRouter and all register*Routes helpers) has been deleted.
// assembly.BuildProtocolRouter in the core module is now the sole protocol
// router; see assembly_seam.go for the thin BuildProtocolRouter adapter.
// The Stores type itself stays: the admin surface (BuildAdminRouter,
// newAdminFSAHandler, DashboardHandler) and the CSIP test harness all
// reference it directly. Deleting it is Phase 3 (IEEESRV-003) scope.
type Stores struct {
	EndDevices store.EndDeviceStore
	// Registrations is the persistent-aware wrapper around the in-memory
	// Store[sep2.Registration]. The embedded *Store gives back-compat
	// method promotion (Get/List/Count) for call sites that don't need
	// the persistence flush.
	Registrations       *memory.RegistrationStore
	MirrorUsagePoints   *memory.Store[sep2.MirrorUsagePoint]
	MirrorMeterReadings *memory.ScopedStore[sep2.MirrorMeterReading]

	// DER stores
	DERs              *memory.ScopedStore[sep2.DER]
	DERCapabilities   *memory.ScopedStore[sep2.DERCapability]
	DERSettings       *memory.ScopedStore[sep2.DERSettings]
	DERStatuses       *memory.ScopedStore[sep2.DERStatus]
	DERAvailabilities *memory.ScopedStore[sep2.DERAvailability]
	// DERPrograms is the persistent-aware wrapper. It embeds
	// *ScopedStore[sep2.DERProgram] so existing handlers that call
	// .ForParent(...) keep working unchanged; the shadowed
	// Create/Delete add the disk flush.
	DERPrograms        *memory.DERProgramStore
	DERControls        *memory.ScopedStore[sep2.DERControl]
	DefaultDERControls *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves          *memory.Store[sep2.DERCurve]

	// FSA store
	FSAs *memory.ScopedStore[sep2.FunctionSetAssignments]

	// IEEE-096: admin FSA management plane (operator-authored templates,
	// program links, device assignments). Distinct from FSAs above which is
	// the spec-facing scoped surface.
	AdminFSAs *memory.AdminFSAStore

	// Subscription store
	Subscriptions *memory.SubscriptionStore

	// Server-side metering
	UsagePoints   *memory.Store[sep2.UsagePoint]
	MeterReadings *memory.ScopedStore[sep2.MeterReading]
	Readings      *memory.ScopedStore[sep2.Reading]
	ReadingTypes  *memory.Store[sep2.ReadingType]

	// New function sets
	Configurations           *memory.ScopedStore[sep2.Configuration]
	DeviceStatuses           *memory.ScopedStore[sep2.DeviceStatus]
	LogEvents                *memory.ScopedStore[sep2.LogEvent]
	PowerStatuses            *memory.ScopedStore[sep2.PowerStatus]
	MessagingPrograms        *memory.Store[sep2.MessagingProgram]
	TextMessages             *memory.ScopedStore[sep2.TextMessage]
	FlowReservationRequests  *memory.ScopedStore[sep2.FlowReservationRequest]
	FlowReservationResponses *memory.ScopedStore[sep2.FlowReservationResponse]
	ResponseSets             *memory.Store[sep2.ResponseSet]
	Responses                *memory.ScopedStore[sep2.Response]
}
