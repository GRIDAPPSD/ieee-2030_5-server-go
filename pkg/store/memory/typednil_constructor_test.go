package memory_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The binding constructors must reject a TYPED nil, not merely a nil interface
// literal.
//
// Both constructors exist to turn a mis-wiring into a loud failure at assembly
// time rather than a silent 500 at request time, and both guarded their
// interface parameters with a plain "== nil". In Go an interface holding a nil
// concrete pointer is not equal to nil, so the one shape a real consumer
// actually produces, the zero value of the concrete type it would otherwise
// have constructed, walked straight past the guard. The diagnostic these
// functions exist to give was withheld from exactly the caller who needed it,
// and the mis-wired store surfaced instead as a nil dereference on the first
// request.
//
// This is the same fault closed at the mount gates with [store.IsAbsent].
// It survived here because these are constructor arguments
// rather than Stores fields, and nothing swept the constructors.
//
// The assertions pin BOTH halves: store.IsAbsent must agree the handle is
// absent, and the constructor must panic on it. Asserting only the panic would
// pass for a guard that rejects every argument.

func TestNewRegisteredEndDeviceStore_RejectsATypedNilEndDeviceStore(t *testing.T) {
	t.Parallel()

	var devs *memory.EndDeviceStore
	if !store.IsAbsent(store.EndDeviceStore(devs)) {
		t.Fatal("store.IsAbsent must read a typed nil EndDeviceStore as absent")
	}

	defer func() {
		if recover() == nil {
			t.Error("a typed nil devs must panic at construction, not be accepted as a wired store")
		}
	}()
	memory.NewRegisteredEndDeviceStore(devs, memory.NewRegistrationStore(), memory.RegistrationPolicy{})
}

func TestNewRegisteredEndDeviceStore_RejectsATypedNilRegistrationStore(t *testing.T) {
	t.Parallel()

	var regs *memory.RegistrationStore
	if !store.IsAbsent(store.ResourceStore[sep2.Registration](regs)) {
		t.Fatal("store.IsAbsent must read a typed nil RegistrationStore as absent")
	}

	defer func() {
		if recover() == nil {
			t.Error("a typed nil regs must panic at construction, not be accepted as a wired store")
		}
	}()
	memory.NewRegisteredEndDeviceStore(memory.NewEndDeviceStore(), regs, memory.RegistrationPolicy{})
}

func TestNewLogEventLinkedEndDeviceStore_RejectsATypedNilStore(t *testing.T) {
	t.Parallel()

	var devs *memory.EndDeviceStore
	if !store.IsAbsent(store.EndDeviceStore(devs)) {
		t.Fatal("store.IsAbsent must read a typed nil EndDeviceStore as absent")
	}

	defer func() {
		if recover() == nil {
			t.Error("a typed nil decorated store must panic at construction, not be accepted as a wired store")
		}
	}()
	memory.NewLogEventLinkedEndDeviceStore(devs)
}

func TestNewFlowReservationLinkedEndDeviceStore_RejectsATypedNilStore(t *testing.T) {
	t.Parallel()

	var devs *memory.EndDeviceStore
	if !store.IsAbsent(store.EndDeviceStore(devs)) {
		t.Fatal("store.IsAbsent must read a typed nil EndDeviceStore as absent")
	}

	defer func() {
		if recover() == nil {
			t.Error("a typed nil decorated store must panic at construction, not be accepted as a wired store")
		}
	}()
	memory.NewFlowReservationLinkedEndDeviceStore(devs)
}

func TestNewFlowReservationUnservedEndDeviceStore_RejectsATypedNilStore(t *testing.T) {
	t.Parallel()

	var devs *memory.EndDeviceStore
	if !store.IsAbsent(store.EndDeviceStore(devs)) {
		t.Fatal("store.IsAbsent must read a typed nil EndDeviceStore as absent")
	}

	defer func() {
		if recover() == nil {
			t.Error("a typed nil decorated store must panic at construction, not be accepted as a wired store")
		}
	}()
	memory.NewFlowReservationUnservedEndDeviceStore(devs)
}
