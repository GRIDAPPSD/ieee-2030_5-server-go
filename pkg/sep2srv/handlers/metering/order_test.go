package metering_test

import (
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
)

func TestCompareMeterReading_MRIDDescending(t *testing.T) {
	t.Parallel()
	in := []sep2.MeterReading{
		{IdentifiedObject: sep2.IdentifiedObject{MRID: "A1"}},
		{IdentifiedObject: sep2.IdentifiedObject{MRID: "C3"}},
		{IdentifiedObject: sep2.IdentifiedObject{MRID: "B2"}},
	}
	slices.SortFunc(in, metering.CompareMeterReading)
	got := []string{in[0].MRID, in[1].MRID, in[2].MRID}
	if want := []string{"C3", "B2", "A1"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestCompareReadingSet_StartDescendingThenMRIDDescending(t *testing.T) {
	t.Parallel()
	set := func(mrid string, start int64) sep2.ReadingSet {
		return sep2.ReadingSet{
			IdentifiedObject: sep2.IdentifiedObject{MRID: mrid},
			TimePeriod:       sep2.DateTimeInterval{Start: start},
		}
	}
	in := []sep2.ReadingSet{set("A", 100), set("B", 300), set("A2", 300), set("C", 200)}
	slices.SortFunc(in, metering.CompareReadingSet)
	var got []string
	for _, s := range in {
		got = append(got, s.MRID)
	}
	if want := []string{"B", "A2", "C", "A"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v (start 300 first, mRID descending within it)", got, want)
	}
}

func TestCompareReading_LocalIDAscendingAbsentLast(t *testing.T) {
	t.Parallel()
	id := func(v uint16) *sep2.HexBinary16 { h := sep2.HexBinary16(v); return &h }
	rd := func(href string, local *sep2.HexBinary16) sep2.Reading {
		r := sep2.Reading{LocalID: local}
		r.Href = href
		return r
	}
	in := []sep2.Reading{rd("/z", nil), rd("/b", id(2)), rd("/a", nil), rd("/c", id(1))}
	slices.SortFunc(in, metering.CompareReading)
	var got []string
	for _, r := range in {
		got = append(got, r.Href)
	}
	if want := []string{"/c", "/b", "/a", "/z"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
