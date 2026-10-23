package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
)

// TestMup_ManagerNeedsTheManagedDevicesRecord drives POST /mup through the
// assembled router: a manager naming a paired LFDI with no EndDevice record
// is refused and nothing is stored; once the record exists the same request
// stores a mirror for the managed device.
func TestMup_ManagerNeedsTheManagedDevicesRecord(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	if err := fleet.managers.Assign(context.Background(), managerLFDI, recordlessLFDI); err != nil {
		t.Fatalf("assign pair to a device with no record: %v", err)
	}
	srv := gateServer(t, fleet.stores, gateTestPolicy())

	body := func(mrid, device string) string {
		raw, err := xml.Marshal(&sep2.MirrorUsagePoint{MRID: mrid, DeviceLFDI: device})
		if err != nil {
			t.Fatalf("marshal MirrorUsagePoint: %v", err)
		}
		return string(raw)
	}

	status, raw := gateRequest(t, srv, http.MethodPost, "/mup", managerLFDI, body("NOREC", recordlessLFDI))
	if status != http.StatusForbidden {
		t.Fatalf("manager POST /mup for a device with no record: status %d, want 403; body=%s", status, raw)
	}
	if n, _ := fleet.stores.MirrorUsagePoints.Count(context.Background()); n != 0 {
		t.Fatalf("stored mirrors after the refusal = %d, want 0", n)
	}

	status, raw = gateRequest(t, srv, http.MethodPost, "/mup", managerLFDI, body("WITHREC", victimLFDI))
	if status != http.StatusCreated {
		t.Fatalf("manager POST /mup for a device with a record: status %d, want 201; body=%s", status, raw)
	}
	stored, err := fleet.stores.MirrorUsagePoints.Get(context.Background(), metering.MirrorStoreID(victimLFDI, "WITHREC"))
	if err != nil {
		t.Fatalf("stored mirror: %v", err)
	}
	if stored.DeviceLFDI != victimLFDI {
		t.Errorf("stored DeviceLFDI = %q, want the managed device %q", stored.DeviceLFDI, victimLFDI)
	}
}
