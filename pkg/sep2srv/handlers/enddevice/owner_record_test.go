package enddevice_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// lfdiAnswer is an EndDeviceReader whose GetByLFDI answers a fixed record
// and error, for the index-drift and outage cases the memory store cannot
// produce.
type lfdiAnswer struct {
	store.EndDeviceReader
	dev sep2.EndDevice
	err error
}

func (a lfdiAnswer) GetByLFDI(context.Context, string) (sep2.EndDevice, error) { return a.dev, a.err }

// TestRecordFor pins the one definition of "this LFDI has an EndDevice
// record": present only when a record is found whose stored LFDI equals the
// one asked for. Missing and mismatched are distinct states so each caller
// keeps its own diagnostic, and a store error is returned, never a state.
func TestRecordFor(t *testing.T) {
	const lfdi = "1111111111111111111111111111111111111111"
	ctx := context.Background()

	devices := memory.NewEndDeviceStore()
	if _, state, err := coreedev.RecordFor(ctx, devices, lfdi); err != nil || state != coreedev.RecordMissing {
		t.Errorf("empty store: state = %v, err = %v, want RecordMissing", state, err)
	}

	dev := sep2.EndDevice{LFDI: lfdi, SFDI: "123456789012"}
	dev.Href = "/edev/7"
	if err := devices.Create(ctx, "7", dev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, state, err := coreedev.RecordFor(ctx, devices, lfdi)
	if err != nil || state != coreedev.RecordPresent {
		t.Fatalf("seeded store: state = %v, err = %v, want RecordPresent", state, err)
	}
	if got.Href != "/edev/7" || got.LFDI != lfdi {
		t.Errorf("record = %+v, want the stored one", got)
	}

	drift := lfdiAnswer{dev: sep2.EndDevice{LFDI: "3333333333333333333333333333333333333333"}}
	if got, state, err := coreedev.RecordFor(ctx, drift, lfdi); err != nil || state != coreedev.RecordMismatched {
		t.Errorf("drifted index: state = %v, err = %v, want RecordMismatched", state, err)
	} else if got.LFDI != drift.dev.LFDI {
		t.Errorf("mismatched record = %+v, want the record the index returned, for the caller's diagnostic", got)
	}

	outage := errors.New("device store unavailable")
	if _, _, err := coreedev.RecordFor(ctx, lfdiAnswer{err: outage}, lfdi); !errors.Is(err, outage) {
		t.Errorf("outage: err = %v, want it wrapped", err)
	}
}
