package sep2adminplane

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
)

// TestAdminStoresOwnsThePEN checks the PEN the plane mints under cannot be
// changed through the caller's pointer after New has checked it.
func TestAdminStoresOwnsThePEN(t *testing.T) {
	pen := uint32(40732)
	stores, err := adminStores(Config{Stores: sep2server.NewStores(), PEN: &pen}, handler.Edition2018, 0, 0)
	if err != nil {
		t.Fatalf("adminStores: %v", err)
	}
	pen = 0
	if stores.PEN == nil || *stores.PEN != 40732 {
		t.Fatalf("PEN after the caller zeroed its value = %v, want 40732", stores.PEN)
	}

	zero := uint32(0)
	stores, err = adminStores(Config{Stores: sep2server.NewStores(), PEN: &zero}, handler.Edition2018, 0, 0)
	if err != nil {
		t.Fatalf("adminStores: %v", err)
	}
	if stores.PEN != nil {
		t.Fatalf("PEN 0 kept as %d, want nil", *stores.PEN)
	}
}
