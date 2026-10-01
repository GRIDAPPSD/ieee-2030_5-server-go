package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// epriCase is one reading in the shape the EPRI reference client posts: the
// value keeps its sign, flowDirection is 0 for zero, 1 (Forward) for a
// negative value and 19 (Reverse) for a positive one.
type epriCase struct {
	name    string
	edition handler.SEP2Edition
	dir     uint8
	value   int64
	want    int64
	flagged bool
}

var epriCases = []epriCase{
	{"2018 forward negative keeps its sign", handler.Edition2018, sep2.FlowDirectionForward, -500, -500, false},
	{"2018 reverse positive is export", handler.Edition2018, sep2.FlowDirectionReverse, 500, 500, false},
	{"2018 zero with direction 0 is 0 W", handler.Edition2018, 0, 0, 0, false},
	{"2018 nonzero with direction 0 is flagged", handler.Edition2018, 0, 7, 7, true},
	{"2023 forward negative is flagged", handler.Edition2023, sep2.FlowDirectionForward, -500, -500, true},
	{"2023 reverse positive on a DER is import", handler.Edition2023, sep2.FlowDirectionReverse, 500, -500, false},
	{"2023 zero with direction 0 is 0 W", handler.Edition2023, 0, 0, 0, false},
	{"2023 nonzero with direction 0 is flagged", handler.Edition2023, 0, 7, 7, true},
	{"2023 forward zero is 0 W", handler.Edition2023, sep2.FlowDirectionForward, 0, 0, false},
	{"2023 forward zero is 0 W", handler.Edition2023, sep2.FlowDirectionForward, 0, 0, false},
}

// Item 1, fleet route: each EPRI-shaped P reading on a DER mirror is served
// with the value and flag the edition's sign rule gives.
func TestHandleListFleets_EPRISignedReadings(t *testing.T) {
	for _, tc := range epriCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleetFixture(t)
			f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
			now := handlerTestNow()
			mup := sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/a"}, DeviceLFDI: fleetDeviceALFDI, RoleFlags: roleIsDER,
				MirrorMeterReading: []sep2.MirrorMeterReading{{
					MRID: "a-p", LastUpdateTime: now,
					ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomWatts), FlowDirection: u8(tc.dir), PowerOfTenMultiplier: i8(0)},
					Reading:     &sep2.Reading{Value: i64(tc.value)},
				}}}
			if err := f.mups.Create(context.Background(), "a", mup); err != nil {
				t.Fatal(err)
			}
			h := f.handler()
			h.Edition = tc.edition
			w := httptest.NewRecorder()
			handler.HandleListFleets(h)(w, httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil))
			want := fmt.Sprintf(`"p":{"value":%d,"readingTime":%d}`, tc.want, now)
			if tc.flagged {
				want = fmt.Sprintf(`"p":{"value":%d,"readingTime":%d,"directionUnknown":true}`, tc.want, now)
			}
			if !strings.Contains(w.Body.String(), want) {
				t.Fatalf("body lacks %s: %s", want, w.Body.String())
			}
		})
	}
}

// Item 1, delivery route: the same readings, held 900 s over a 900 s window.
// A mapped reading delivers value/4 Wh; a flagged one is not summed.
func TestDERControlList_DeliveryEPRISignedReadings(t *testing.T) {
	for _, tc := range epriCases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.h.Edition = tc.edition
			b := deliveryBase
			dir := tc.dir
			d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: b, value: tc.value * 4, dir: &dir})
			d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})
			_, body := d.list(t)
			want := fmt.Sprintf(`"deliveredWh":%d,"averageW":%d,"coveredSeconds":900,"readings":1,"directionUnknown":false`, tc.want, tc.want*4)
			if tc.flagged {
				want = `"deliveredWh":null,"averageW":null,"coveredSeconds":0,"readings":0,"directionUnknown":true`
			}
			if !strings.Contains(body, want) {
				t.Fatalf("body lacks %s: %s", want, body)
			}
		})
	}
}

// A second covered by only some phase legs of a qualifier is not covered,
// and its energy is not counted.
func TestDERControlList_DeliveryPartialPhaseIsNotCovered(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	avg := dq(2)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "PA", at: b + 900, value: 3600, dir: reverseDir(), qual: avg, phase: dq(128), period: &sep2.DateTimeInterval{Start: b, Duration: 900}},
		reading{mrid: "PB", at: b + 300, value: 7200, dir: reverseDir(), qual: avg, phase: dq(64), period: &sep2.DateTimeInterval{Start: b, Duration: 300}},
	)
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 300 || del.Readings != 2 {
		t.Fatalf("delivery = %+v, want only the 300 s both phases cover", *del)
	}
	// (3600 + 7200) W over 300 s.
	wantWh(t, del.DeliveredWh, 900)
}

// Item 3: dataQualifier 0 is "Not applicable (default, if not specified)"
// (2018 DataQualifierType), so it counts as absent: an instantaneous reading.
func TestDERControlList_DeliveryQualifierZeroIsInstantaneous(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), reading{mrid: "P", at: b, value: 3600, dir: reverseDir(), qual: dq(0)})
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 300 {
		t.Fatalf("covered = %d, want 300 held forward from receipt", del.CoveredSeconds)
	}
	wantWh(t, del.DeliveredWh, 300)
}
