package server

import (
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
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
	// EndDeviceManagers holds the (manager, managed) LFDI pairs the ownership
	// gate consults. Run wires an empty store until the admin plane can
	// provision pairs, so aggregators reach only their own EndDevice.
	EndDeviceManagers store.EndDeviceManagementStore
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
	DERPrograms *memory.DERProgramStore
	// DERControls is the persistence-aware wrapper (GRIDAPPSD/ieee-2030_5-server-go#565).
	// It satisfies store.ScopedStore[sep2.DERControl]; its Create/Update/Delete
	// add the disk flush, and it carries the mRID-to-scope index
	// internal/dercontrol.Issuer's undo logic and #566's admin API need.
	DERControls *memory.DERControlStore
	// DERControlLifecycles is the persistence-aware companion store for
	// internal/dercontrol.LifecycleRecord, keyed identically to DERControls
	// (GRIDAPPSD/ieee-2030_5-server-go#565). #564's serve-time status
	// decorator (pkg/sep2srv/handlers/der, wired in
	// pkg/sep2srv/assembly/assembly.go behind store.IsAbsent) reads it; the
	// #566 admin DER control routes write it through
	// internal/dercontrol.Issuer. Nil leaves those routes unmounted.
	DERControlLifecycles *dercontrol.LifecycleStore
	DefaultDERControls   *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves            *memory.Store[sep2.DERCurve]

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
	FlowReservationRequests  store.ScopedStore[sep2.FlowReservationRequest]
	FlowReservationResponses store.ScopedStore[sep2.FlowReservationResponse]
	ResponseSets             *memory.Store[sep2.ResponseSet]
	Responses                *memory.ScopedStore[sep2.Response]

	// FlowReservationResponseLifecycles holds each response's cancel mark
	// (#714), keyed exactly as the response and persisted like it (#761).
	FlowReservationResponseLifecycles store.ScopedStore[dercontrol.LifecycleRecord]

	// FlowReservationAnswers records who created and who cancelled each
	// response (#670), keyed exactly as the response. Nil records nothing,
	// and every response reads as unrecorded.
	FlowReservationAnswers store.ScopedStore[flowreservation.AnswerRecord]

	// CommitmentLedger is the process's one commitment ledger (#714).
	CommitmentLedger *commitment.Ledger

	// FlowReservationQueue is the process's one flow reservation queue (#763),
	// shared by the protocol routes and startup recovery, and closed by Run.
	// Nil leaves the assembly to build a queue of its own, which nothing
	// closes or recovers.
	FlowReservationQueue *flowreservation.Queue

	// FlowReservationDeadline is the hold the queue runs under. The admin read
	// API computes each request's deadline from the same value (#763). Zero
	// takes the queue's default.
	FlowReservationDeadline time.Duration

	// FlowReservationRetentionGrace is how long an ended flow reservation
	// stays readable before retention removes it (#672). Zero takes the
	// package default.
	FlowReservationRetentionGrace time.Duration

	// MirrorReadingRetention and MirrorReadingMaxPerMirror bound the
	// out-of-band mirror readings (#806). Run resolves both from Config; a
	// zero value makes the retention sweep refuse, logged at boot.
	MirrorReadingRetention    time.Duration
	MirrorReadingMaxPerMirror int

	// DERControlIssuer is the process's one DER control issuer (#763), shared
	// by the admin DER control handler and the grant cancel writers. Nil makes
	// each of them build its own.
	DERControlIssuer *dercontrol.Issuer

	// Sep2Edition is the config-declared IEEE 2030.5 edition (env
	// SEP2_EDITION, internal/config.Config.EffectiveSEP2Edition), threaded
	// to the fleet-read admin handler's export-positive sign mapping (#715
	// fix round 3, item 2). The zero value, handler.Edition2018, is what an
	// unconfigured server already did before this field existed. Held on
	// Stores rather than as a BuildAdminRouter parameter: it is config about
	// how to interpret the resources in these stores, the same role
	// RegistrationPolicy already carries on this struct, not a routing or
	// listener knob like the router's other explicit parameters.
	Sep2Edition handler.SEP2Edition

	// PEN is the server's IANA Private Enterprise Number, the low 32 bits of
	// every admin-issued DERControl mRID (config.Config.EffectivePEN). Nil
	// leaves the DER control create route answering 503.
	PEN *uint32

	// AdminNotifier fans out the subscription notifications an admin write
	// causes (the #566 DER control routes). Nil sends none. Held here for
	// the same reason as Sep2Edition: BuildAdminRouter's other parameters
	// are routing and listener knobs.
	AdminNotifier handler.ResourceNotifier
}

// NewCommitmentLedger builds the commitment ledger over s's own stores: the
// response lifecycle store it reads is the one the response status routes
// read, so a cancel mark frees the window and serves Cancelled together.
func NewCommitmentLedger(s *Stores) *commitment.Ledger {
	return sources.NewLedger(
		s.EndDevices, s.EndDeviceManagers,
		s.FlowReservationResponses, s.FlowReservationResponseLifecycles,
		s.DERControls, s.DERControlLifecycles,
	)
}
