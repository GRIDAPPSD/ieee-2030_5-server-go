package adminplane

import (
	"fmt"
	"log"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Admin credential lifetimes, shared by Run and the public facade. The idle
// window is what an operator notices; the absolute cap is what a stolen
// cookie runs into, and it is never extended by use.
const (
	AdminTicketTTL              = 30 * time.Second
	AdminSessionIdleTimeout     = 30 * time.Minute
	AdminSessionAbsoluteTimeout = 8 * time.Hour
)

// StoresFromAssembly holds the same store instances the protocol router
// serves as the admin plane's Stores, so both planes read and write one set.
// The config-derived fields (edition, PEN, deadlines, notifier) are left for
// the caller.
//
// A few admin fields are concrete where assembly.Stores holds an interface.
// FSAs must be a *memory.ScopedStore when AdminFSAs is set, or the FSA
// assignment routes would write through a nil store, so that is an error. A
// DERControls that is not a *memory.DERControlStore leaves the DER control
// routes unmounted, as an unset one does, and is logged.
func StoresFromAssembly(s *assembly.Stores) (*Stores, error) {
	if s == nil {
		return nil, nil
	}
	fsas, ok := s.FSAs.(*memory.ScopedStore[sep2.FunctionSetAssignments])
	if s.AdminFSAs != nil && !ok {
		return nil, fmt.Errorf("admin plane: FSAs is %T, want *memory.ScopedStore[sep2.FunctionSetAssignments] when AdminFSAs is set", s.FSAs)
	}
	derControls, ok := s.DERControls.(*memory.DERControlStore)
	if !ok && !store.IsAbsent(s.DERControls) {
		log.Printf("admin plane: DERControls is %T, not *memory.DERControlStore: no DER control admin routes mounted", s.DERControls)
	}
	out := &Stores{
		EndDevices:           s.EndDevices,
		EndDeviceManagers:    s.EndDeviceManagers,
		EndDeviceIndexes:     s.EndDeviceIndexes,
		Registrations:        s.Registrations,
		RegistrationPolicy:   s.RegistrationPolicy,
		MirrorUsagePoints:    s.MirrorUsagePoints,
		MirrorMeterReadings:  s.MirrorMeterReadings,
		DERs:                 s.DERs,
		DERCapabilities:      concrete[*memory.ScopedStore[sep2.DERCapability]](s.DERCapabilities),
		DERSettings:          concrete[*memory.ScopedStore[sep2.DERSettings]](s.DERSettings),
		DERStatuses:          s.DERStatuses,
		DERAvailabilities:    s.DERAvailabilities,
		DERPrograms:          s.DERPrograms,
		DERControls:          derControls,
		DERControlLifecycles: s.DERControlLifecycles,
		DefaultDERControls:   concrete[*memory.ScopedStore[sep2.DefaultDERControl]](s.DefaultDERControls),
		DERCurves:            concrete[*memory.Store[sep2.DERCurve]](s.DERCurves),
		FSAs:                 fsas,
		AdminFSAs:            s.AdminFSAs,
		Subscriptions:        s.Subscriptions,
		UsagePoints:          concrete[*memory.Store[sep2.UsagePoint]](s.UsagePoints),
		MeterReadings:        concrete[*memory.ScopedStore[sep2.MeterReading]](s.MeterReadings),
		Readings:             concrete[*memory.ScopedStore[sep2.Reading]](s.Readings),
		ReadingTypes:         concrete[*memory.Store[sep2.ReadingType]](s.ReadingTypes),
		Configurations:       concrete[*memory.ScopedStore[sep2.Configuration]](s.Configurations),
		DeviceStatuses:       concrete[*memory.ScopedStore[sep2.DeviceStatus]](s.DeviceStatuses),
		LogEvents:            concrete[*memory.ScopedStore[sep2.LogEvent]](s.LogEvents),
		PowerStatuses:        concrete[*memory.ScopedStore[sep2.PowerStatus]](s.PowerStatuses),
		MessagingPrograms:    concrete[*memory.Store[sep2.MessagingProgram]](s.MessagingPrograms),
		TextMessages:         concrete[*memory.ScopedStore[sep2.TextMessage]](s.TextMessages),

		FlowReservationRequests:           s.FlowReservationRequests,
		FlowReservationResponses:          s.FlowReservationResponses,
		ResponseSets:                      concrete[*memory.Store[sep2.ResponseSet]](s.ResponseSets),
		Responses:                         s.Responses,
		FlowReservationResponseLifecycles: s.FlowReservationResponseLifecycles,
		CommitmentLedger:                  s.CommitmentLedger,
		FlowReservationQueue:              s.FlowReservationQueue,
		DERControlIssuer:                  s.DERControlIssuer,
	}
	return out, nil
}

// concrete returns v as T, or T's zero value when v holds another type. It
// is used only for fields the admin plane never reads.
func concrete[T any](v any) T {
	t, _ := v.(T)
	return t
}
