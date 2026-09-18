package assembly

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ReaderStores is the read-only half of [Stores]: one reader-typed field for
// every resource collection Stores exposes for writing, taken from the same
// underlying stores. A consumer holding a ReaderStores cannot compile a call
// that mutates the store; see [NewReaderStores].
//
// Not mirrored here, and why: EndDeviceIndexes is a URL addressing
// allocator, not resource data, and no read consumer resolves one; nothing
// but the router's own wiring needs it. RegistrationPolicy is a provisioning
// config value (a PIN resolver), not a store. AdminFSAs and Subscriptions
// are bespoke admin and notification planes with no existing reader/writer
// interface split, and no acceptance criterion for this handle names either
// as a read consumer's need; narrowing them is future work for whichever
// consumer first needs to read one.
type ReaderStores struct {
	EndDevices        store.EndDeviceReader
	EndDeviceManagers store.EndDeviceManagementReader

	Registrations store.ResourceReader[sep2.Registration]

	MirrorUsagePoints   store.ResourceReader[sep2.MirrorUsagePoint]
	MirrorMeterReadings store.ScopedReader[sep2.MirrorMeterReading]

	DERs               store.ScopedReader[sep2.DER]
	DERCapabilities    store.ScopedReader[sep2.DERCapability]
	DERSettings        store.ScopedReader[sep2.DERSettings]
	DERStatuses        store.ScopedReader[sep2.DERStatus]
	DERAvailabilities  store.ScopedReader[sep2.DERAvailability]
	DERPrograms        store.ScopedReader[sep2.DERProgram]
	DERControls        store.ScopedReader[sep2.DERControl]
	DefaultDERControls store.ScopedReader[sep2.DefaultDERControl]
	DERCurves          store.ResourceReader[sep2.DERCurve]

	FSAs store.ScopedReader[sep2.FunctionSetAssignments]

	UsagePoints   store.ResourceReader[sep2.UsagePoint]
	MeterReadings store.ScopedReader[sep2.MeterReading]
	Readings      store.ScopedReader[sep2.Reading]
	ReadingTypes  store.ResourceReader[sep2.ReadingType]

	Configurations           store.ScopedReader[sep2.Configuration]
	DeviceStatuses           store.ScopedReader[sep2.DeviceStatus]
	LogEvents                store.ScopedReader[sep2.LogEvent]
	PowerStatuses            store.ScopedReader[sep2.PowerStatus]
	MessagingPrograms        store.ResourceReader[sep2.MessagingProgram]
	TextMessages             store.ScopedReader[sep2.TextMessage]
	FlowReservationRequests  store.ScopedReader[sep2.FlowReservationRequest]
	FlowReservationResponses store.ScopedReader[sep2.FlowReservationResponse]
	ResponseSets             store.ResourceReader[sep2.ResponseSet]
	Responses                store.ScopedReader[sep2.Response]
}

// NewReaderStores narrows s to its read-only half: every field is assigned
// from the matching field on s, to a reader interface rather than the
// write-capable one s declares. Nothing is allocated and nothing is
// materialized; a write made through s is visible through the result,
// because both are views onto the same underlying stores, not two stores.
//
// This is the follow-up sep2server.Server.Stores's own doc comment
// promised: a second accessor returning reader interfaces, needing no
// change here. Every field below is a plain interface narrowing, proven to
// type-check by pkg/sep2server.TestReadOnlyNarrowingIsAvailable before this
// type existed.
func NewReaderStores(s *Stores) *ReaderStores {
	return &ReaderStores{
		EndDevices:        s.EndDevices,
		EndDeviceManagers: s.EndDeviceManagers,

		Registrations: s.Registrations,

		MirrorUsagePoints:   s.MirrorUsagePoints,
		MirrorMeterReadings: s.MirrorMeterReadings,

		DERs:               s.DERs,
		DERCapabilities:    s.DERCapabilities,
		DERSettings:        s.DERSettings,
		DERStatuses:        s.DERStatuses,
		DERAvailabilities:  s.DERAvailabilities,
		DERPrograms:        s.DERPrograms,
		DERControls:        s.DERControls,
		DefaultDERControls: s.DefaultDERControls,
		DERCurves:          s.DERCurves,

		FSAs: s.FSAs,

		UsagePoints:   s.UsagePoints,
		MeterReadings: s.MeterReadings,
		Readings:      s.Readings,
		ReadingTypes:  s.ReadingTypes,

		Configurations:           s.Configurations,
		DeviceStatuses:           s.DeviceStatuses,
		LogEvents:                s.LogEvents,
		PowerStatuses:            s.PowerStatuses,
		MessagingPrograms:        s.MessagingPrograms,
		TextMessages:             s.TextMessages,
		FlowReservationRequests:  s.FlowReservationRequests,
		FlowReservationResponses: s.FlowReservationResponses,
		ResponseSets:             s.ResponseSets,
		Responses:                s.Responses,
	}
}
