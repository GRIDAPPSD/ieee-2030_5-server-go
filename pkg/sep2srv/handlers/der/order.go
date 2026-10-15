package der

import (
	"cmp"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// The list orderings of IEEE 2030.5-2018 Table 50, one comparator per list,
// for [store.Ordered] and [store.OrderedScoped]. Each is a total order, so a
// list is served the same way whatever order the store holds it in.
//
// mRID is compared as a byte string. That is its numeric order for the
// fixed-width upper-case hex the server mints; a loaded mRID of another width
// or case is ordered by its bytes.

// CompareDERProgram orders DERPrograms by primacy ascending, then mRID
// descending.
func CompareDERProgram(a, b sep2.DERProgram) int {
	if c := cmp.Compare(a.Primacy, b.Primacy); c != 0 {
		return c
	}
	return strings.Compare(b.MRID, a.MRID)
}

// CompareDERControl orders DERControls by interval.start ascending, then
// creationTime descending, then mRID descending. A control with no interval
// sorts after every control that has one, rather than at a start time it does
// not carry.
func CompareDERControl(a, b sep2.DERControl) int {
	switch {
	case a.Interval == nil && b.Interval != nil:
		return 1
	case a.Interval != nil && b.Interval == nil:
		return -1
	case a.Interval != nil:
		if c := cmp.Compare(a.Interval.Start, b.Interval.Start); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(b.CreationTime, a.CreationTime); c != 0 {
		return c
	}
	return strings.Compare(b.MRID, a.MRID)
}

// CompareDERCurve orders DERCurves by creationTime descending, then mRID
// descending.
func CompareDERCurve(a, b sep2.DERCurve) int {
	if c := cmp.Compare(b.CreationTime, a.CreationTime); c != 0 {
		return c
	}
	return strings.Compare(b.MRID, a.MRID)
}

// CompareDER orders DERs by href ascending.
func CompareDER(a, b sep2.DER) int {
	return strings.Compare(a.Href, b.Href)
}
