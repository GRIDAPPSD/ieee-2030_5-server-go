package metering

import (
	"cmp"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// The list orderings of IEEE 2030.5-2018 Table 39 (section 10.4.2), one
// comparator per list, for [store.OrderedScoped]. Each is a total order, so a
// list is served the same way whatever order the store holds it in. mRID is
// compared as a byte string, as in handlers/der/order.go.

// CompareMeterReading orders MeterReadings by mRID descending.
func CompareMeterReading(a, b sep2.MeterReading) int {
	return strings.Compare(b.MRID, a.MRID)
}

// CompareReadingSet orders ReadingSets by timePeriod.start descending, then
// mRID descending.
func CompareReadingSet(a, b sep2.ReadingSet) int {
	if c := cmp.Compare(b.TimePeriod.Start, a.TimePeriod.Start); c != 0 {
		return c
	}
	return strings.Compare(b.MRID, a.MRID)
}

// CompareReading orders Readings by localID ascending. consumptionBlock and
// touTier, the table's next two keys, are not fields of sep2.Reading, so they
// cannot differ between two served Readings. A Reading with no localID sorts
// after every Reading that has one, and href ascending settles any remaining
// tie so the order stays total.
func CompareReading(a, b sep2.Reading) int {
	switch {
	case a.LocalID == nil && b.LocalID != nil:
		return 1
	case a.LocalID != nil && b.LocalID == nil:
		return -1
	case a.LocalID != nil:
		if c := cmp.Compare(*a.LocalID, *b.LocalID); c != 0 {
			return c
		}
	}
	return strings.Compare(a.Href, b.Href)
}
