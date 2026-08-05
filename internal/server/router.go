package server

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
)

// Stores holds all resource stores for the server.
//
// Phase 2: the protocol-router half of this file (BuildProtocolRouter
// and all register*Routes helpers) has been deleted. assembly.BuildProtocolRouter
// in the core module is now the sole protocol router; see
// assembly_seam.go for the thin BuildProtocolRouter adapter. The Stores
// type itself stays: the admin surface (BuildAdminRouter,
// newAdminFSAHandler, DashboardHandler) and the CSIP test harness all
// reference it directly. Deleting it is Phase 3 scope.
type Stores struct {
	EndDevices store.EndDeviceStore
	// EndDeviceIndexes allocates the opaque, server-chosen index that
	// addresses an EndDevice in resource URLs ("/edev/3/rg"). core v0.10.0
	// added this field to assembly.Stores; a nil value degrades to a
	// process-local substitute (URL indices do not survive restart) rather
	// than failing, but every field here is wired so the degraded path
	// never fires in practice. In-memory only for now, matching this
	// field's pre-bump behavior (there was no persisted index before);
	// wiring memory.NewEndDeviceIndexWithPersistence via cfg.DataDir,
	// the way Registrations does, is a follow-up, not forced by this bump.
	EndDeviceIndexes *memory.EndDeviceIndex
	// Registrations is the persistent-aware wrapper around the in-memory
	// Store[sep2.Registration]. The embedded *Store gives back-compat
	// method promotion (Get/List/Count) for call sites that don't need
	// the persistence flush.
	Registrations *memory.RegistrationStore
	// RegistrationPolicy supplies the pIN and pollRate for the Registration
	// core creates alongside every EndDevice. core v0.13.0 added this
	// field to assembly.Stores. The zero value provisions
	// nothing, and that is fail-closed rather than degraded, matching this
	// repo's behavior before the field existed: no self-registration pIN
	// resolver is wired today, so a device gets no Registration and no
	// RegistrationLink, the same as before this field was plumbed through.
	// Wiring a real PIN resolver (e.g. sourced from the admin UI's existing
	// manual PIN entry at POST /api/devices) is a follow-up feature
	// decision, not forced by copying the field.
	RegistrationPolicy  memory.RegistrationPolicy
	MirrorUsagePoints   *memory.Store[sep2.MirrorUsagePoint]
	MirrorMeterReadings *memory.ScopedStore[sep2.MirrorMeterReading]

	// DER stores
	DERs              *memory.ScopedStore[sep2.DER]
	DERCapabilities   *memory.ScopedStore[sep2.DERCapability]
	DERSettings       *memory.ScopedStore[sep2.DERSettings]
	DERStatuses       *memory.ScopedStore[sep2.DERStatus]
	DERAvailabilities *memory.ScopedStore[sep2.DERAvailability]
	// DERPrograms is the persistence-aware wrapper. It satisfies
	// store.ScopedStore[sep2.DERProgram] and its Create/Delete add the
	// disk flush.
	//
	// It no longer embeds the collection: core made the inner store an
	// unexported field, so neither the promoted ForParent nor the old
	// .ScopedStore reach-through exists. Consumers that want a scoped
	// DERProgram surface take the store.ScopedStore contract and address
	// resources by (parent, id).
	DERPrograms        *memory.DERProgramStore
	DERControls        *memory.ScopedStore[sep2.DERControl]
	DefaultDERControls *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves          *memory.Store[sep2.DERCurve]

	// FSA store
	FSAs *memory.ScopedStore[sep2.FunctionSetAssignments]

	// #163: admin FSA management plane (operator-authored templates,
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
