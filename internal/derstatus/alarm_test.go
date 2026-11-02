package derstatus_test

import (
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derstatus"
)

func TestAlarmOf(t *testing.T) {
	if derstatus.AlarmOf(sep2.DERStatus{}) != nil {
		t.Error("no alarmStatus must decode to nil")
	}
	cases := []struct {
		name     string
		raw      uint32
		wantBits []int
		wantRes  uint32
	}{
		{"zero is an alarmStatus with nothing set", 0, []int{}, 0},
		{"bit 0", 1 << 0, []int{0}, 0},
		{"bit 10, the last defined", 1 << 10, []int{10}, 0},
		{"bit 11 is reserved", 1 << 11, []int{}, 1 << 11},
		{"bit 31 is reserved", 1 << 31, []int{}, 1 << 31},
		{"mixed", 1<<2 | 1<<7 | 1<<20, []int{2, 7}, 1 << 20},
	}
	for _, c := range cases {
		hb := sep2.HexBinary32(c.raw)
		a := derstatus.AlarmOf(sep2.DERStatus{AlarmStatus: &hb})
		if a == nil {
			t.Fatalf("%s: nil", c.name)
		}
		if a.Raw != c.raw || !reflect.DeepEqual(a.Bits, c.wantBits) || a.ReservedBits != c.wantRes {
			t.Errorf("%s: got raw=%#x bits=%v reserved=%#x, want bits=%v reserved=%#x", c.name, a.Raw, a.Bits, a.ReservedBits, c.wantBits, c.wantRes)
		}
	}
}
