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

// NewReaderStores narrows s to its read-only half. Every field is WRAPPED,
// not assigned: [store.AsReader], [store.AsScopedReader],
// [store.AsEndDeviceReader] and [store.AsEndDeviceManagementReader] each
// return a private type declaring only the reader methods, following the
// [store.UnderReader] idiom ("narrowing scope must not widen privilege")
// for privilege rather than scope. A plain field-to-field assignment would
// leave the write handle's concrete value, and its write methods, reachable
// through a type assertion or reflection; wrapping does not.
//
// Nothing is allocated beyond the wrappers themselves and nothing is
// materialized: a write made through s is visible through the result,
// because both are views onto the same underlying stores, not two stores.
//
// EndDevices gets [ownedEndDevices], the same decorator chain
// BuildProtocolRouter gives the write-side route handlers, so the read
// handle's device records carry the same RegistrationLink and
// LogEventListLink derivations the serve path advertises, and an absent
// handle refuses descriptively rather than panicking. A single function
// backs both callers, so the write and read chains cannot type the same
// three decorators out separately and drift apart.
//
// Every other field, EndDeviceManagers included, gets requireScoped,
// requireResource or requireEndDeviceManagers directly: unlike the router,
// which gates a family's siblings only when the family's own anchor field
// is present (because an unmounted family's routes never read them),
// ReaderStores has no route mounting to rely on and every field is always
// reachable through direct struct access, so every field is guarded on its
// own. EndDeviceManagers cannot use the router's own raw-read pattern
// (ownership.go checks store.IsAbsent once and skips every later call
// instead of substituting) because ReaderStores exposes the field directly
// with no call site of its own to gate; requireEndDeviceManagers
// substitutes a refusing store instead, so a half-wired manager store
// answers descriptively on ManagerOf rather than panicking, matching every
// sibling field.
func NewReaderStores(s *Stores) *ReaderStores {
	edevs := ownedEndDevices(s)

	return &ReaderStores{
		EndDevices:        store.AsEndDeviceReader(edevs),
		EndDeviceManagers: store.AsEndDeviceManagementReader(requireEndDeviceManagers(s.EndDeviceManagers)),

		Registrations: store.AsReader(requireResource(s.Registrations, "Registrations")),

		MirrorUsagePoints:   store.AsReader(requireResource(s.MirrorUsagePoints, "MirrorUsagePoints")),
		MirrorMeterReadings: store.AsScopedReader(requireScoped(s.MirrorMeterReadings, "MirrorMeterReadings")),

		DERs:               store.AsScopedReader(requireScoped(s.DERs, "DERs")),
		DERCapabilities:    store.AsScopedReader(requireScoped(s.DERCapabilities, "DERCapabilities")),
		DERSettings:        store.AsScopedReader(requireScoped(s.DERSettings, "DERSettings")),
		DERStatuses:        store.AsScopedReader(requireScoped(s.DERStatuses, "DERStatuses")),
		DERAvailabilities:  store.AsScopedReader(requireScoped(s.DERAvailabilities, "DERAvailabilities")),
		DERPrograms:        store.AsScopedReader(requireScoped(s.DERPrograms, "DERPrograms")),
		DERControls:        store.AsScopedReader(requireScoped(s.DERControls, "DERControls")),
		DefaultDERControls: store.AsScopedReader(requireScoped(s.DefaultDERControls, "DefaultDERControls")),
		DERCurves:          store.AsReader(requireResource(s.DERCurves, "DERCurves")),

		FSAs: store.AsScopedReader(requireScoped(s.FSAs, "FSAs")),

		UsagePoints:   store.AsReader(requireResource(s.UsagePoints, "UsagePoints")),
		MeterReadings: store.AsScopedReader(requireScoped(s.MeterReadings, "MeterReadings")),
		Readings:      store.AsScopedReader(requireScoped(s.Readings, "Readings")),
		ReadingTypes:  store.AsReader(requireResource(s.ReadingTypes, "ReadingTypes")),

		Configurations:           store.AsScopedReader(requireScoped(s.Configurations, "Configurations")),
		DeviceStatuses:           store.AsScopedReader(requireScoped(s.DeviceStatuses, "DeviceStatuses")),
		LogEvents:                store.AsScopedReader(requireScoped(s.LogEvents, "LogEvents")),
		PowerStatuses:            store.AsScopedReader(requireScoped(s.PowerStatuses, "PowerStatuses")),
		MessagingPrograms:        store.AsReader(requireResource(s.MessagingPrograms, "MessagingPrograms")),
		TextMessages:             store.AsScopedReader(requireScoped(s.TextMessages, "TextMessages")),
		FlowReservationRequests:  store.AsScopedReader(requireScoped(s.FlowReservationRequests, "FlowReservationRequests")),
		FlowReservationResponses: store.AsScopedReader(requireScoped(s.FlowReservationResponses, "FlowReservationResponses")),
		ResponseSets:             store.AsReader(requireResource(s.ResponseSets, "ResponseSets")),
		Responses:                store.AsScopedReader(requireScoped(s.Responses, "Responses")),
	}
}
