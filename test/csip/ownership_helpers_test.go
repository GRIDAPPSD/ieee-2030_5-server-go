package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// fixtureEndDeviceID is the id of the single EndDevice every one-device
// fixture declares. Tests bind it to the certificate they present, because
// the server authorizes EndDevice access on that certificate rather than on
// the LFDI a fixture was written with.
const fixtureEndDeviceID = "0"

// seedOwnedEndDevice writes an EndDevice at id owned by owner, for a
// procedure that addresses a device no fixture declares.
func seedOwnedEndDevice(t *testing.T, devs store.EndDeviceStore, id string, owner csiptest.DeviceIdentity) {
	t.Helper()
	enabled := true
	dev := sep2.EndDevice{LFDI: owner.LFDI, SFDI: owner.SFDI, Enabled: &enabled}
	dev.Href = "/edev/" + id
	if err := devs.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("seed EndDevice %q: %v", id, err)
	}
}
