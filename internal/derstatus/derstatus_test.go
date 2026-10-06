package derstatus

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func ptr[T any](v T) *T { return &v }

func TestConnectOf_FieldByField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status sep2.DERStatus
		want   *Connect
	}{
		{
			name: "2018 storage device reports storConnectStatus",
			status: sep2.DERStatus{
				StorConnectStatus: &sep2.ConnectStatusType{DateTime: 500, Value: 0x0B},
			},
			want: &Connect{
				Source: SourceStorConnectStatus, Raw: 0x0B, Since: 500,
				Connected: true, Available: true, Operating: false, Test: true,
			},
		},
		{
			name: "2018 generator reports genConnectStatus with fault and reserved bits",
			status: sep2.DERStatus{
				GenConnectStatus: &sep2.ConnectStatusType{DateTime: 600, Value: 0xF0},
			},
			want: &Connect{
				Source: SourceGenConnectStatus, Raw: 0xF0, Since: 600,
				Connected: false, Fault: true, ReservedBits: 0xE0,
			},
		},
		{
			name: "2018 value 0 is a real report of Disconnected",
			status: sep2.DERStatus{
				GenConnectStatus: &sep2.ConnectStatusType{DateTime: 1, Value: 0},
			},
			want: &Connect{Source: SourceGenConnectStatus, Raw: 0, Since: 1},
		},
		{
			name: "2023 device reports connectStatus with energized",
			status: sep2.DERStatus{
				ConnectStatus: &sep2.ConnectStatusType2{DateTime: 700, Value: 0x03},
			},
			want: &Connect{
				Source: SourceConnectStatus, Raw: 0x03, Since: 700,
				Connected: true, Energized: ptr(true),
			},
		},
		{
			name: "2023 connected but not energized, bits above 1 are reserved",
			status: sep2.DERStatus{
				ConnectStatus: &sep2.ConnectStatusType2{DateTime: 800, Value: 0x05},
			},
			want: &Connect{
				Source: SourceConnectStatus, Raw: 0x05, Since: 800,
				Connected: true, Energized: ptr(false), ReservedBits: 0x04,
			},
		},
		{
			name: "connectStatus wins over a deprecated genConnectStatus",
			status: sep2.DERStatus{
				ConnectStatus:    &sep2.ConnectStatusType2{DateTime: 900, Value: 0x00},
				GenConnectStatus: &sep2.ConnectStatusType{DateTime: 900, Value: 0x01},
			},
			want: &Connect{Source: SourceConnectStatus, Raw: 0, Since: 900, Energized: ptr(false)},
		},
		{
			name: "genConnectStatus wins over storConnectStatus",
			status: sep2.DERStatus{
				GenConnectStatus:  &sep2.ConnectStatusType{DateTime: 10, Value: 0x01},
				StorConnectStatus: &sep2.ConnectStatusType{DateTime: 20, Value: 0x00},
			},
			want: &Connect{Source: SourceGenConnectStatus, Raw: 1, Since: 10, Connected: true},
		},
		{
			name:   "no connect field is not reported",
			status: sep2.DERStatus{ReadingTime: 5},
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ConnectOf(tc.status)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("ConnectOf = %+v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("ConnectOf = nil, want %+v", *tc.want)
			case tc.want == nil:
				return
			}
			if got.Source != tc.want.Source || got.Raw != tc.want.Raw || got.Since != tc.want.Since {
				t.Errorf("source/raw/since = %q/%#x/%d, want %q/%#x/%d",
					got.Source, got.Raw, got.Since, tc.want.Source, tc.want.Raw, tc.want.Since)
			}
			if got.Connected != tc.want.Connected || got.Available != tc.want.Available ||
				got.Operating != tc.want.Operating || got.Test != tc.want.Test || got.Fault != tc.want.Fault {
				t.Errorf("bits = %+v, want %+v", *got, *tc.want)
			}
			if got.ReservedBits != tc.want.ReservedBits {
				t.Errorf("ReservedBits = %#x, want %#x", got.ReservedBits, tc.want.ReservedBits)
			}
			if (got.Energized == nil) != (tc.want.Energized == nil) ||
				(got.Energized != nil && *got.Energized != *tc.want.Energized) {
				t.Errorf("Energized = %v, want %v", got.Energized, tc.want.Energized)
			}
		})
	}
}

func TestInverterOf(t *testing.T) {
	t.Parallel()
	if got := InverterOf(sep2.DERStatus{}); got != nil {
		t.Fatalf("InverterOf with no inverterStatus = %+v, want nil", *got)
	}
	got := InverterOf(sep2.DERStatus{InverterStatus: &sep2.InverterStatusType{DateTime: 42, Value: 8}})
	if got == nil || got.Code != 8 || got.Since != 42 {
		t.Fatalf("InverterOf = %+v, want code 8 since 42", got)
	}
}

func TestAssess_StaleBoundary(t *testing.T) {
	t.Parallel()
	const now = int64(1_000_000)
	pollRates := []struct {
		name string
		rate uint32
		want int64
	}{
		{"default 900 s", 900, 1860},
		{"explicit 300 s", 300, 660},
		{"zero takes the 900 s default", 0, 1860},
	}
	for _, pr := range pollRates {
		t.Run(pr.name, func(t *testing.T) {
			t.Parallel()
			limit := StaleAfterSeconds(pr.rate)
			if limit != pr.want {
				t.Fatalf("StaleAfterSeconds(%d) = %d, want %d", pr.rate, limit, pr.want)
			}
			for _, c := range []struct {
				name  string
				age   int64
				stale bool
			}{
				{"limit minus 1 s is fresh", limit - 1, false},
				{"exactly the limit is fresh", limit, false},
				{"limit plus 1 s is stale", limit + 1, true},
			} {
				f := Assess(now-c.age, now, pr.rate)
				if f.Stale != c.stale || f.AgeSeconds != c.age {
					t.Errorf("%s: Stale=%v Age=%d, want %v and %d", c.name, f.Stale, f.AgeSeconds, c.stale, c.age)
				}
				if f.NoReadingTime || f.ClockAhead {
					t.Errorf("%s: unexpected flags %+v", c.name, f)
				}
			}
		})
	}
}

func TestAssess_NoReadingTime(t *testing.T) {
	t.Parallel()
	f := Assess(0, 1_000_000, 900)
	if !f.NoReadingTime {
		t.Error("NoReadingTime = false for readingTime 0")
	}
	if f.AgeSeconds != 0 {
		t.Errorf("AgeSeconds = %d, want 0 (no age is claimed)", f.AgeSeconds)
	}
	if !f.Stale {
		t.Error("Stale = false: a status that cannot be aged must not read as fresh")
	}
	if f.ClockAhead {
		t.Error("ClockAhead = true for readingTime 0")
	}
}

func TestAssess_ClockAhead(t *testing.T) {
	t.Parallel()
	const now = int64(1_000_000)
	cases := []struct {
		name  string
		ahead int64
		want  bool
	}{
		{"1 s ahead is skew, not clock-ahead", 1, false},
		{"exactly one pollRate ahead is not flagged", 900, false},
		{"one pollRate plus 1 s ahead is flagged", 901, true},
	}
	for _, tc := range cases {
		f := Assess(now+tc.ahead, now, 900)
		if f.ClockAhead != tc.want {
			t.Errorf("%s: ClockAhead = %v, want %v", tc.name, f.ClockAhead, tc.want)
		}
		if f.AgeSeconds != 0 {
			t.Errorf("%s: AgeSeconds = %d, want 0 floor for a future reading", tc.name, f.AgeSeconds)
		}
		if f.Stale {
			t.Errorf("%s: Stale = true for a future reading", tc.name)
		}
	}
}

func TestDecode_CombinesAllParts(t *testing.T) {
	t.Parallel()
	s := sep2.DERStatus{
		ReadingTime:    990,
		ConnectStatus:  &sep2.ConnectStatusType2{DateTime: 100, Value: 1},
		InverterStatus: &sep2.InverterStatusType{DateTime: 200, Value: 4},
	}
	d := Decode(s, 1000, 900)
	if d.Connect == nil || !d.Connect.Connected || d.Connect.Since != 100 {
		t.Errorf("Connect = %+v", d.Connect)
	}
	if d.Inverter == nil || d.Inverter.Code != 4 || d.Inverter.Since != 200 {
		t.Errorf("Inverter = %+v", d.Inverter)
	}
	if d.ReadingTime != 990 || d.AgeSeconds != 10 || d.Stale {
		t.Errorf("freshness = %+v, want readingTime 990 age 10 fresh", d.Freshness)
	}
}
