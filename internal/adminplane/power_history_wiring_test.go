package adminplane

import (
	"context"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The fleet handler carries the stores' history, so the route serves what the
// recorder wrote.
func TestNewAdminFleetHandler_ThreadsPowerHistory(t *testing.T) {
	hist, err := handler.OpenPowerHistory(t.TempDir(), time.Hour, 1000)
	if err != nil {
		t.Fatal(err)
	}
	h := newAdminFleetHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore(), PowerHistory: hist})
	if h == nil || h.PowerHistory != hist {
		t.Fatalf("PowerHistory = %v, want the stores' history", h)
	}
	if h := newAdminFleetHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore()}); h.PowerHistory != nil {
		t.Errorf("PowerHistory = %v without a data directory, want nil", h.PowerHistory)
	}
}

// StartPowerHistory starts nothing without a history or without management
// pairs, and its stop is always safe to call.
func TestStartPowerHistory_NothingToRecordIsANoOp(t *testing.T) {
	hist, err := handler.OpenPowerHistory(t.TempDir(), time.Hour, 1000)
	if err != nil {
		t.Fatal(err)
	}
	StartPowerHistory(context.Background(), nil)()
	StartPowerHistory(context.Background(), &Stores{})()
	StartPowerHistory(context.Background(), &Stores{PowerHistory: hist})()
	stop := StartPowerHistory(context.Background(), &Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore(), PowerHistory: hist})
	stop()
}
