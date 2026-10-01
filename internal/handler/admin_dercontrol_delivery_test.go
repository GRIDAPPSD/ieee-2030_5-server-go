package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// deliveryBase is far enough in the past that every window below has ended.
const deliveryBase = int64(1_700_000_000)

type deliveryHarness struct {
	*dcHarness
	mups *memory.Store[sep2.MirrorUsagePoint]
	mmrs *memory.ScopedStore[sep2.MirrorMeterReading]
}

func newDeliveryHarness(t *testing.T) *deliveryHarness {
	t.Helper()
	d := &deliveryHarness{
		dcHarness: newDCHarness(t, ptrU32(dcPEN)),
		mups:      memory.NewStore[sep2.MirrorUsagePoint](),
		mmrs:      memory.NewScopedStore[sep2.MirrorMeterReading](),
	}
	d.h.MirrorUsagePoints = d.mups
	d.h.MirrorMeterReadings = d.mmrs
	return d
}

// seedControl stores an admin-issued control under device 0's program 0
// directly, so its interval can lie in the past.
func (d *deliveryHarness) seedControl(t *testing.T, id string, start int64, duration uint32, lc dercontrol.LifecycleRecord) string {
	t.Helper()
	ctx := context.Background()
	mrid := fmt.Sprintf("%031d%s", 0, strings.ToUpper(id))
	ctrl := sep2.DERControl{
		RandomizableEvent: sep2.RandomizableEvent{Event: sep2.Event{
			MRID:                 mrid,
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/0/fsa/0/derp/0/derc/" + id}},
			CreationTime:         start - 60,
			Interval:             &sep2.DateTimeInterval{Start: start, Duration: duration},
		}},
		DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1000}},
	}
	if err := d.controls.Create(ctx, "0/0/0", id, ctrl); err != nil {
		t.Fatal(err)
	}
	if err := d.lifecycles.Create(ctx, "0/0/0", id, lc); err != nil {
		t.Fatal(err)
	}
	return mrid
}

// seedMirror stores a mirror for lfdi with out-of-band export readings of
// watts, held for postRate seconds each, received at each of times.
func (d *deliveryHarness) seedMirror(t *testing.T, id, lfdi string, postRate uint32, watts int64, dir *uint8, times ...int64) {
	t.Helper()
	ctx := context.Background()
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, DeviceLFDI: lfdi, PostRate: &postRate}
	mup.Href = "/mup/" + id
	if err := d.mups.Create(ctx, id, mup); err != nil {
		t.Fatal(err)
	}
	uom := sep2.UomWatts
	for i, at := range times {
		r := sep2.MirrorMeterReading{
			MRID:           "W" + id,
			LastUpdateTime: at,
			Reading:        &sep2.Reading{Value: &watts},
			ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: dir},
		}
		if err := d.mmrs.Create(ctx, id, fmt.Sprint(i), r); err != nil {
			t.Fatal(err)
		}
	}
}

func (d *deliveryHarness) list(t *testing.T) (map[string]handler.DERControlListItem, string) {
	t.Helper()
	w := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got handler.DERControlList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]handler.DERControlListItem{}
	for _, c := range got.Controls {
		out[c.MRID] = c
	}
	return out, w.Body.String()
}

func reverseDir() *uint8 { v := sep2.FlowDirectionReverse; return &v }

func wantWh(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Fatalf("deliveredWh = %v, want %v", got, want)
	}
}

// Criterion: a superseded or cancelled control stops at that time. Readings
// hold through every window, so only the window end bounds the figure.
func TestDERControlList_DeliveryStopsAtSupersedeAndCancel(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	// A mirror with a lowercase deviceLFDI still matches device 0.
	d.seedMirror(t, "1", strings.ToLower(dcLFDI), 1000, 3600, reverseDir(), b, b+1000, b+2000, b+3000)
	ended := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	superseded := d.seedControl(t, "s", b+1000, 1000, dercontrol.LifecycleRecord{SupersededAt: ptrI64(b + 1400), SupersededBy: "X"})
	cancelled := d.seedControl(t, "c", b+2000, 1000, dercontrol.LifecycleRecord{CancelledAt: ptrI64(b + 2250), CancelReason: "test"})

	got, body := d.list(t)
	for _, tc := range []struct {
		mrid     string
		start    int64
		end      int64
		readings int
	}{
		{ended, b, b + 1000, 1},
		{superseded, b + 1000, b + 1400, 1},
		{cancelled, b + 2000, b + 2250, 1},
	} {
		del := got[tc.mrid].Delivery
		if del == nil {
			t.Fatalf("control %s has no delivery: %s", tc.mrid, body)
		}
		if del.WindowStart != tc.start || del.WindowEnd != tc.end || del.CoveredSeconds != tc.end-tc.start || del.Readings != tc.readings || del.DeviceLFDI != dcLFDI || del.DirectionUnknown {
			t.Errorf("control %s delivery = %+v, want window [%d,%d) fully covered by %d reading", tc.mrid, *del, tc.start, tc.end, tc.readings)
		}
		wantWh(t, del.DeliveredWh, float64(tc.end-tc.start))
		if del.NewestReadingTime == nil || *del.NewestReadingTime != tc.start {
			t.Errorf("control %s newestReadingTime = %v, want %d", tc.mrid, del.NewestReadingTime, tc.start)
		}
	}
}

// Trap: readings are in memory only, so a control with none reads "no
// readings" (null), never 0, on the wire.
func TestDERControlList_DeliveryWithNoReadingsIsNull(t *testing.T) {
	d := newDeliveryHarness(t)
	// Another device's readings never count.
	d.seedMirror(t, "1", dcOtherLFDI, 1000, 3600, reverseDir(), deliveryBase)
	mrid := d.seedControl(t, "e", deliveryBase, 600, dercontrol.LifecycleRecord{})

	got, body := d.list(t)
	del := got[mrid].Delivery
	if del == nil || del.DeliveredWh != nil || del.CoveredSeconds != 0 || del.Readings != 0 || del.NewestReadingTime != nil {
		t.Fatalf("delivery = %+v, want a null figure: %s", del, body)
	}
	if del.WindowStart != deliveryBase || del.WindowEnd != deliveryBase+600 {
		t.Fatalf("window = [%d,%d), want [%d,%d)", del.WindowStart, del.WindowEnd, deliveryBase, deliveryBase+600)
	}
	want := fmt.Sprintf(`"delivery":{"windowStart":%d,"windowEnd":%d,"deliveredWh":null,"coveredSeconds":0,"readings":0,"directionUnknown":false,"deviceLFDI":"%s","newestReadingTime":null}`, deliveryBase, deliveryBase+600, dcLFDI)
	if !strings.Contains(body, want) {
		t.Fatalf("body lacks %s: %s", want, body)
	}
}

// Criterion: a reading with no direction is flagged on the wire and not summed.
func TestDERControlList_DeliveryFlagsDirectionlessReading(t *testing.T) {
	d := newDeliveryHarness(t)
	d.seedMirror(t, "1", dcLFDI, 100, 3600, reverseDir(), deliveryBase)
	d.seedMirror(t, "2", dcLFDI, 1000, 99999, nil, deliveryBase+200)
	mrid := d.seedControl(t, "e", deliveryBase, 1000, dercontrol.LifecycleRecord{})

	got, body := d.list(t)
	del := got[mrid].Delivery
	if del == nil || !del.DirectionUnknown || del.CoveredSeconds != 100 || del.Readings != 1 {
		t.Fatalf("delivery = %+v, want directionUnknown and 100 s from the directed reading: %s", del, body)
	}
	wantWh(t, del.DeliveredWh, 100)
}

type failingMUPs struct {
	store.ResourceReader[sep2.MirrorUsagePoint]
}

func (failingMUPs) List(context.Context, store.ListOptions) (store.ListResult[sep2.MirrorUsagePoint], error) {
	return store.ListResult[sep2.MirrorUsagePoint]{}, errors.New("disk read failed")
}

type failingMMRs struct {
	store.ScopedReader[sep2.MirrorMeterReading]
}

func (failingMMRs) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.MirrorMeterReading], error) {
	return store.ListResult[sep2.MirrorMeterReading]{}, errors.New("disk read failed")
}

// A store error is a 500, never a zero or a null figure.
func TestDERControlList_DeliveryStoreErrorIs500(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(d *deliveryHarness)
	}{
		{"mirror usage points", func(d *deliveryHarness) { d.h.MirrorUsagePoints = failingMUPs{} }},
		{"mirror meter readings", func(d *deliveryHarness) { d.h.MirrorMeterReadings = failingMMRs{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.seedMirror(t, "1", dcLFDI, 100, 3600, reverseDir(), deliveryBase)
			d.seedControl(t, "e", deliveryBase, 1000, dercontrol.LifecycleRecord{})
			tc.set(d)
			assertRefusal(t, d.do(t, http.MethodGet, "/api/der/controls?device=0", ""), http.StatusInternalServerError, "internal error")
		})
	}
}

// The route writes nothing: every store it reads holds the same records
// after a list as before.
func TestDERControlList_DeliveryWritesNothing(t *testing.T) {
	d := newDeliveryHarness(t)
	d.seedMirror(t, "1", dcLFDI, 100, 3600, reverseDir(), deliveryBase, deliveryBase+50)
	d.seedControl(t, "e", deliveryBase, 1000, dercontrol.LifecycleRecord{SupersededAt: ptrI64(deliveryBase + 500)})

	snapshot := func() []any {
		ctx := context.Background()
		mups, err := d.mups.List(ctx, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		mmrs, err := d.mmrs.List(ctx, "1", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		ctrls, err := d.controls.List(ctx, "0/0/0", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		lc, err := d.lifecycles.Get(ctx, "0/0/0", "e")
		if err != nil {
			t.Fatal(err)
		}
		return []any{mups.Items, mmrs.Items, ctrls.Items, lc}
	}
	before := snapshot()
	d.list(t)
	d.list(t)
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("stores changed across a list:\nbefore %+v\nafter  %+v", before, after)
	}
}

func ptrI64(v int64) *int64 { return &v }
