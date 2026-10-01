package server

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func fullyWiredFlowReservationStores() *Stores {
	return &Stores{
		EndDevices:                        memory.NewEndDeviceStore(),
		EndDeviceManagers:                 memory.NewEndDeviceManagementStore(),
		FlowReservationRequests:           memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses:          memory.NewScopedStore[sep2.FlowReservationResponse](),
		FlowReservationResponseLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
		DERControls:                       memory.NewDERControlStore(),
		DERControlLifecycles:              dercontrol.NewLifecycleStore(),
	}
}

// Every store a view reads is required: a route mounted over a missing one
// would answer 500 on every read.
func TestNewAdminFlowReservationHandler_RequiresEveryStore(t *testing.T) {
	cases := map[string]func(*Stores){
		"EndDevices":                        func(s *Stores) { s.EndDevices = nil },
		"EndDeviceManagers":                 func(s *Stores) { s.EndDeviceManagers = nil },
		"FlowReservationRequests":           func(s *Stores) { s.FlowReservationRequests = nil },
		"FlowReservationResponses":          func(s *Stores) { s.FlowReservationResponses = nil },
		"FlowReservationResponseLifecycles": func(s *Stores) { s.FlowReservationResponseLifecycles = nil },
	}
	if newAdminFlowReservationHandler(fullyWiredFlowReservationStores()) == nil {
		t.Fatal("newAdminFlowReservationHandler(fully wired) = nil, want a handler")
	}
	if newAdminFlowReservationHandler(nil) != nil {
		t.Error("newAdminFlowReservationHandler(nil) != nil")
	}
	for name, clear := range cases {
		s := fullyWiredFlowReservationStores()
		clear(s)
		if h := newAdminFlowReservationHandler(s); h != nil {
			t.Errorf("newAdminFlowReservationHandler without %s = %v, want nil", name, h)
		}
	}
}

// The DER control stores are optional, and one without the other leaves
// both handler fields nil rather than a half-wired execution read.
func TestNewAdminFlowReservationHandler_ControlStoresAreOptional(t *testing.T) {
	both := newAdminFlowReservationHandler(fullyWiredFlowReservationStores())
	if both.Controls == nil || both.Executions == nil {
		t.Errorf("with both DER control stores: Controls=%v Executions=%v, want both wired", both.Controls, both.Executions)
	}
	for name, clear := range map[string]func(*Stores){
		"DERControls":          func(s *Stores) { s.DERControls = nil },
		"DERControlLifecycles": func(s *Stores) { s.DERControlLifecycles = nil },
	} {
		s := fullyWiredFlowReservationStores()
		clear(s)
		h := newAdminFlowReservationHandler(s)
		if h == nil {
			t.Fatalf("without %s: handler = nil, want one with no executions", name)
		}
		if h.Controls != nil || h.Executions != nil {
			t.Errorf("without %s: Controls=%v Executions=%v, want both nil", name, h.Controls, h.Executions)
		}
	}
}

// Nothing persists these records yet, so the page must say so.
func TestNewAdminFlowReservationHandler_ReportsNotPersisted(t *testing.T) {
	h := newAdminFlowReservationHandler(fullyWiredFlowReservationStores())
	if h.Persisted {
		t.Error("Persisted = true over in-memory stores, want false")
	}
	if got := h.Deadline.EffectiveDeadline().Seconds(); got != 300 {
		t.Errorf("deadline = %v s, want the 300 s default", got)
	}
}

// The queue and the admin read API must run under one deadline: the queue
// takes it from NewCoreRouterConfig, the handler from its own wiring.
func TestAdminFlowReservationDeadlineIsTheQueuesDeadline(t *testing.T) {
	queue := NewCoreRouterConfig(&config.Config{}).FlowReservationDeadline
	h := newAdminFlowReservationHandler(fullyWiredFlowReservationStores())
	if got := h.Deadline.Deadline; got != queue {
		t.Errorf("admin deadline = %v, queue deadline = %v, want them equal", got, queue)
	}
	if got, want := h.Deadline.EffectiveDeadline(), (flowreservation.Config{Deadline: queue}).EffectiveDeadline(); got != want {
		t.Errorf("effective admin deadline = %v, want the queue's %v", got, want)
	}
}

type persistingStore struct{ persists bool }

func (s persistingStore) Persists() bool { return s.persists }

// Persisted follows the stores' own Persists, so it turns true when they do.
func TestPersistsFollowsTheStore(t *testing.T) {
	if persists(memory.NewScopedStore[sep2.FlowReservationRequest]()) {
		t.Error("persists(in-memory store) = true, want false")
	}
	if persists(persistingStore{false}) {
		t.Error("persists(a store reporting false) = true")
	}
	if !persists(persistingStore{true}) {
		t.Error("persists(a store reporting true) = false")
	}
	disk, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](t.TempDir()+"/frq.json", "frq")
	if err != nil {
		t.Fatal(err)
	}
	if !persists(disk) {
		t.Error("persists(PersistentScopedStore) = false, want true")
	}
}
