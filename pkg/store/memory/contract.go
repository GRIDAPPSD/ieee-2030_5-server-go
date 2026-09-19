package memory

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Compile-time proof that the in-memory implementations satisfy the store
// contract. Without these, the interfaces in pkg/store would be aspirational:
// nothing would fail to build if an implementation drifted off them.
//
// A BUILD failure is the point, not a test failure. Every store handle on
// assembly.Stores and every handler constructor is declared as
// one of these interfaces, so a method added to or changed on the contract has
// to be answered here before anything downstream compiles. A test could report
// the same fact, but only after a build that already succeeded, and only if
// someone ran it.
//
// The assertions are instantiated with real sep2 resource types rather than a
// stand-in, so they also prove the generic constraints hold for the types the
// server actually stores: an sep2 type whose Copy signature drifts off
// [store.Copier] fails here rather than at whichever call site stores it.
//
// One assertion per resource type is deliberate rather than redundant. Store[T]
// and ScopedStore[T] are generic, so a single instantiation proves the method
// set for one T and says nothing about whether the OTHER types the router wires
// can be stored at all. The lists below are the set assembly.Stores declares,
// and a new field there belongs in them.

// Flat collections: the store.ResourceStore fields on assembly.Stores.
var (
	_ store.ResourceReader[sep2.EndDevice] = (*Store[sep2.EndDevice])(nil)
	_ store.ResourceStore[sep2.EndDevice]  = (*Store[sep2.EndDevice])(nil)

	_ store.ResourceReader[sep2.MirrorUsagePoint] = (*Store[sep2.MirrorUsagePoint])(nil)
	_ store.ResourceStore[sep2.MirrorUsagePoint]  = (*Store[sep2.MirrorUsagePoint])(nil)

	_ store.ResourceStore[sep2.Registration]     = (*Store[sep2.Registration])(nil)
	_ store.ResourceStore[sep2.DERCurve]         = (*Store[sep2.DERCurve])(nil)
	_ store.ResourceStore[sep2.UsagePoint]       = (*Store[sep2.UsagePoint])(nil)
	_ store.ResourceStore[sep2.ReadingType]      = (*Store[sep2.ReadingType])(nil)
	_ store.ResourceStore[sep2.MessagingProgram] = (*Store[sep2.MessagingProgram])(nil)
	_ store.ResourceStore[sep2.ResponseSet]      = (*Store[sep2.ResponseSet])(nil)
)

// Parent-scoped collections: the store.ScopedStore fields on assembly.Stores.
var (
	_ store.ScopedReader[sep2.DERStatus] = (*ScopedStore[sep2.DERStatus])(nil)
	_ store.ScopedStore[sep2.DERStatus]  = (*ScopedStore[sep2.DERStatus])(nil)

	_ store.ScopedReader[sep2.MirrorMeterReading] = (*ScopedStore[sep2.MirrorMeterReading])(nil)
	_ store.ScopedStore[sep2.MirrorMeterReading]  = (*ScopedStore[sep2.MirrorMeterReading])(nil)

	_ store.ScopedStore[sep2.DER]                     = (*ScopedStore[sep2.DER])(nil)
	_ store.ScopedStore[sep2.DERCapability]           = (*ScopedStore[sep2.DERCapability])(nil)
	_ store.ScopedStore[sep2.DERSettings]             = (*ScopedStore[sep2.DERSettings])(nil)
	_ store.ScopedStore[sep2.DERAvailability]         = (*ScopedStore[sep2.DERAvailability])(nil)
	_ store.ScopedStore[sep2.DERControl]              = (*ScopedStore[sep2.DERControl])(nil)
	_ store.ScopedStore[sep2.DefaultDERControl]       = (*ScopedStore[sep2.DefaultDERControl])(nil)
	_ store.ScopedStore[sep2.FunctionSetAssignments]  = (*ScopedStore[sep2.FunctionSetAssignments])(nil)
	_ store.ScopedStore[sep2.MeterReading]            = (*ScopedStore[sep2.MeterReading])(nil)
	_ store.ScopedStore[sep2.Reading]                 = (*ScopedStore[sep2.Reading])(nil)
	_ store.ScopedStore[sep2.Configuration]           = (*ScopedStore[sep2.Configuration])(nil)
	_ store.ScopedStore[sep2.DeviceStatus]            = (*ScopedStore[sep2.DeviceStatus])(nil)
	_ store.ScopedStore[sep2.LogEvent]                = (*ScopedStore[sep2.LogEvent])(nil)
	_ store.ScopedStore[sep2.PowerStatus]             = (*ScopedStore[sep2.PowerStatus])(nil)
	_ store.ScopedStore[sep2.TextMessage]             = (*ScopedStore[sep2.TextMessage])(nil)
	_ store.ScopedStore[sep2.FlowReservationRequest]  = (*ScopedStore[sep2.FlowReservationRequest])(nil)
	_ store.ScopedStore[sep2.FlowReservationResponse] = (*ScopedStore[sep2.FlowReservationResponse])(nil)
	_ store.ScopedStore[sep2.Response]                = (*ScopedStore[sep2.Response])(nil)
)

// Wrappers and decorators. Each delegates to another implementation rather than
// being one, so each needs its own assertion: satisfying the contract is not
// inherited from what a type wraps, and neither persistence wrapper relies
// on embedding to promote the methods that prove it.
var (
	_ store.ResourceStore[sep2.Registration] = (*RegistrationStore)(nil)
	_ store.ScopedStore[sep2.DERProgram]     = (*DERProgramStore)(nil)

	// The EndDevice implementations. The two decorators also assert this next
	// to their own declarations; repeated here so this file reads as a complete
	// inventory of what claims to implement the contract.
	_ store.EndDeviceStore = (*EndDeviceStore)(nil)
	_ store.EndDeviceStore = (*RegisteredEndDeviceStore)(nil)
	_ store.EndDeviceStore = (*LogEventLinkedEndDeviceStore)(nil)

	// The read-only half, proven against the same three so a narrowing that
	// only needs EndDeviceReader is not aspirational either.
	_ store.EndDeviceReader = (*EndDeviceStore)(nil)
	_ store.EndDeviceReader = (*RegisteredEndDeviceStore)(nil)
	_ store.EndDeviceReader = (*LogEventLinkedEndDeviceStore)(nil)
)
