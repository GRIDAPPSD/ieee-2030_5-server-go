package server

import (
	"log"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Not run with t.Parallel(): each redirects the shared stdlib log.Writer().

func derControlWiringStores() *Stores {
	return &Stores{
		DERPrograms:          memory.NewDERProgramStore(),
		DERControls:          memory.NewDERControlStore(),
		DERControlLifecycles: dercontrol.NewLifecycleStore(),
		EndDevices:           memory.NewEndDeviceStore(),
	}
}

func TestNewAdminDERControlHandler_MissingLockStoresLogAndLeaveFieldsNil(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newAdminDERControlHandler(derControlWiringStores())
	if h == nil {
		t.Fatal("newAdminDERControlHandler = nil, want a handler: a cancel still runs without the lock")
	}
	if h.Fleets != nil || h.Ledger != nil {
		t.Errorf("Fleets = %v, Ledger = %v, want both nil", h.Fleets, h.Ledger)
	}
	got := buf.String()
	if !strings.Contains(got, "no EndDevice management store") || !strings.Contains(got, "no commitment ledger") {
		t.Errorf("log output = %q, want one line for the management store and one for the ledger", got)
	}
	if n := strings.Count(got, "a cancel runs without the fleet lock"); n != 2 {
		t.Errorf("log output = %q, want 2 lines naming the unlocked cancel, got %d", got, n)
	}
}

func TestNewAdminDERControlHandler_LockStoresWiredIsSilent(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	s := derControlWiringStores()
	s.EndDeviceManagers = memory.NewEndDeviceManagementStore()
	s.CommitmentLedger = NewCommitmentLedger(s)
	h := newAdminDERControlHandler(s)
	if h == nil {
		t.Fatal("newAdminDERControlHandler = nil, want a handler")
	}
	if h.Fleets == nil || h.Ledger == nil {
		t.Errorf("Fleets = %v, Ledger = %v, want both wired", h.Fleets, h.Ledger)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none", buf.String())
	}
}
