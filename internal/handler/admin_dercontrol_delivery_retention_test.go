package handler_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
)

// G/W/T 4 (#806): a 24 h control with readings every 300 s, swept at its end
// under the shortest retention the setting allows, is still fully covered.
// The first reading is received 299 s before the control starts, so it is the
// one that covers the first second and is the oldest the sweep must keep.
func TestDERControlList_DeliveryCoveredAfterRetentionSweep(t *testing.T) {
	for _, maxAge := range []time.Duration{config.MinMirrorReadingRetention, config.DefaultMirrorReadingRetention} {
		t.Run(maxAge.String(), func(t *testing.T) {
			d := newDeliveryHarness(t)
			ctx := context.Background()
			const (
				day  = int64(dercontrol.DefaultMaxDuration / time.Second)
				rate = 300
			)
			start := deliveryBase
			first := start - (rate - 1)

			postRate := uint32(rate)
			mup := sep2.MirrorUsagePoint{MRID: "MUP1", DeviceLFDI: dcLFDI, PostRate: &postRate, RoleFlags: roleIsDER}
			mup.Href = "/mup/1"
			if err := d.mups.Create(ctx, "1", mup); err != nil {
				t.Fatal(err)
			}
			uom := sep2.UomWatts
			watts := int64(3600)
			n := 0
			for at := first; at < start+day; at += rate {
				id := fmt.Sprintf("%020d", time.Unix(at, 0).UnixNano())
				r := sep2.MirrorMeterReading{
					MRID:           "W1",
					LastUpdateTime: at,
					Reading:        &sep2.Reading{Value: &watts},
					ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: reverseDir()},
				}
				r.Href = "/mup/1/mr/" + id
				if err := d.mmrs.Create(ctx, "1", id, r); err != nil {
					t.Fatal(err)
				}
				n++
			}
			mrid := d.seedControl(t, "e", start, uint32(day), dercontrol.LifecycleRecord{})

			ret := &mirrorretention.Retention{
				Readings:     d.mmrs,
				Mirrors:      d.mups,
				MaxAge:       maxAge,
				MaxPerSeries: config.DefaultMirrorReadingMaxPerSeries,
				Log:          slog.New(slog.DiscardHandler),
			}
			if removed, err := ret.Sweep(ctx, time.Unix(start+day, 0)); err != nil || removed != 0 {
				t.Fatalf("Sweep = %d, %v, want nothing removed", removed, err)
			}

			got, body := d.list(t)
			del := got[mrid].Delivery
			if del == nil || del.WindowStart != start || del.WindowEnd != start+day || del.CoveredSeconds != day || del.Readings != n {
				t.Fatalf("delivery = %+v, want [%d,%d) fully covered by %d readings: %s", del, start, start+day, n, body)
			}
			wantWh(t, del.DeliveredWh, float64(day))
		})
	}
}
