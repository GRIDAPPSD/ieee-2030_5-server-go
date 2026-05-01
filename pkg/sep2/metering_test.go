package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestUsagePointRoundTrip(t *testing.T) {
	upt := sep2.UsagePoint{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/upt/1"},
		},
		MRID:                "AAAA0100000000000000000004D1",
		Description:         "Meter Usage Point",
		ServiceCategoryKind: 0,
		Status:              1,
		MeterReadingListLink: &sep2.ListLink{Href: "/upt/1/mr", All: 3},
	}

	data, err := xml.Marshal(&upt)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "UsagePoint") {
		t.Error("missing UsagePoint element")
	}
	if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
		t.Error("missing namespace")
	}

	var parsed sep2.UsagePoint
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MRID != upt.MRID {
		t.Errorf("MRID = %q", parsed.MRID)
	}
}

func TestMeterReadingMarshal(t *testing.T) {
	mr := sep2.MeterReading{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/upt/1/mr/1"},
		},
		MRID:        "BBBB01",
		Description: "Instantaneous Demand",
		ReadingTypeLink: &sep2.Link{Href: "/rt/1"},
		ReadingLink:     &sep2.Link{Href: "/upt/1/mr/1/r"},
	}

	data, err := xml.Marshal(&mr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MeterReading") {
		t.Error("missing MeterReading element")
	}
}

func TestReadingCopy(t *testing.T) {
	val := int64(42000)
	tp := sep2.DateTimeInterval{Start: 1604963587, Duration: 300}
	original := sep2.Reading{
		Value:      &val,
		TimePeriod: &tp,
	}

	copied := original.Copy()
	*copied.Value = 999
	copied.TimePeriod.Start = 0

	if *original.Value != 42000 {
		t.Error("original Value mutated")
	}
	if original.TimePeriod.Start != 1604963587 {
		t.Error("original TimePeriod mutated")
	}
}

func TestReadingTypeMarshal(t *testing.T) {
	uom := sep2.UomWatts
	rt := sep2.ReadingType{
		Resource: sep2.Resource{Href: "/rt/1"},
		Uom:      &uom,
	}

	data, err := xml.Marshal(&rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<uom>38</uom>") {
		t.Error("missing uom element with watts value")
	}
}

func TestMirrorUsagePointRoundTrip(t *testing.T) {
	mup := sep2.MirrorUsagePoint{
		Resource:    sep2.Resource{Href: "/mup/1"},
		MRID:        "MUP001",
		Description: "Inverter Mirror",
		DeviceLFDI:  "AABBCCDD00112233445566778899AABBCCDDEEFF",
		ServiceCategoryKind: 0,
		Status:              1,
	}

	data, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatal(err)
	}

	var parsed sep2.MirrorUsagePoint
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.DeviceLFDI != mup.DeviceLFDI {
		t.Errorf("DeviceLFDI = %q", parsed.DeviceLFDI)
	}
}

func TestMirrorMeterReadingCopy(t *testing.T) {
	val := int64(5000)
	uom := sep2.UomWatts
	original := sep2.MirrorMeterReading{
		MRID:        "MMR01",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	}

	copied := original.Copy()
	*copied.Reading.Value = 999
	*copied.ReadingType.Uom = 0

	if *original.Reading.Value != 5000 {
		t.Error("original Reading.Value mutated")
	}
	if *original.ReadingType.Uom != sep2.UomWatts {
		t.Error("original ReadingType.Uom mutated")
	}
}
