package der_test

import (
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
)

func control(mrid string, creation int64, start *int64) sep2.DERControl {
	var c sep2.DERControl
	c.MRID = mrid
	c.CreationTime = creation
	if start != nil {
		c.Interval = &sep2.DateTimeInterval{Start: *start, Duration: 60}
	}
	return c
}

// A control with no interval has no start time to be placed by, so it goes
// after every control that has one and is ordered among its kind by the
// remaining keys.
func TestCompareDERControl_MissingIntervalSortsLast(t *testing.T) {
	t.Parallel()
	early, late := int64(100), int64(900)
	items := []sep2.DERControl{
		control("0A", 50, nil),
		control("0B", 10, &late),
		control("0C", 60, nil),
		control("0D", 10, &early),
	}
	slices.SortStableFunc(items, coreder.CompareDERControl)
	var got []string
	for _, c := range items {
		got = append(got, c.MRID)
	}
	if want := []string{"0D", "0B", "0C", "0A"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// mRID is the last key of three lists, so two members differing only by it
// must still compare unequal, or their order would depend on the store.
func TestCompare_LastKeyIsMRIDDescending(t *testing.T) {
	t.Parallel()
	if c := coreder.CompareDERProgram(sep2.DERProgram{MRID: "01"}, sep2.DERProgram{MRID: "02"}); c <= 0 {
		t.Errorf("DERProgram 01 vs 02 = %d, want > 0", c)
	}
	start := int64(5)
	if c := coreder.CompareDERControl(control("01", 1, &start), control("02", 1, &start)); c <= 0 {
		t.Errorf("DERControl 01 vs 02 = %d, want > 0", c)
	}
	if c := coreder.CompareDERCurve(sep2.DERCurve{MRID: "01"}, sep2.DERCurve{MRID: "02"}); c <= 0 {
		t.Errorf("DERCurve 01 vs 02 = %d, want > 0", c)
	}
}
