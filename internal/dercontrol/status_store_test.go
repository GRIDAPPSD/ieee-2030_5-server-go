package dercontrol

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The two policies differ on exactly three cases: an item with no record,
// an item with no interval, and a list member with no id. Each case is
// pinned for both, so neither user can drift into the other's behavior.
func TestDerivedStatusStore_Policies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stored := sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: 5}

	build := func(t *testing.T, serverAuthored bool) (*DerivedStatusStore[sep2.DERControl], *memory.ScopedStore[sep2.DERControl]) {
		t.Helper()
		items := memory.NewScopedStore[sep2.DERControl]()
		add := func(id, href string, interval *sep2.DateTimeInterval) {
			c := sep2.DERControl{}
			c.Href = href
			c.MRID = id
			c.CreationTime = 5
			c.Interval = interval
			es := stored
			c.EventStatus = &es
			if err := items.Create(ctx, "P", id, c); err != nil {
				t.Fatal(err)
			}
		}
		add("norecord", "/x/derc/norecord", &sep2.DateTimeInterval{Start: 10, Duration: 10})
		add("nointerval", "/x/derc/nointerval", nil)
		return NewDerivedStatusStore(items, memory.NewScopedStore[LifecycleRecord](), StatusPolicy[sep2.DERControl]{
			Event:          func(c *sep2.DERControl) *sep2.Event { return &c.Event },
			ID:             func(_ string, c sep2.DERControl) (string, bool) { return derhref.ControlID(c.Href) },
			ServerAuthored: serverAuthored,
		}), items
	}

	for _, tc := range []struct {
		name           string
		serverAuthored bool
		id             string
		want           sep2.EventStatus
	}{
		{"not server-authored, no record: as stored", false, "norecord", stored},
		{"not server-authored, no interval: as stored", false, "nointerval", stored},
		{"server-authored, no record: derived Active past its start", true, "norecord", sep2.EventStatus{CurrentStatus: sep2.EventStatusActive, DateTime: 10}},
		{"server-authored, no interval: derived from start 0", true, "nointerval", sep2.EventStatus{CurrentStatus: sep2.EventStatusActive, DateTime: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := build(t, tc.serverAuthored)
			got, err := s.Get(ctx, "P", tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if got.EventStatus == nil || got.EventStatus.CurrentStatus != tc.want.CurrentStatus || got.EventStatus.DateTime != tc.want.DateTime {
				t.Errorf("EventStatus = %+v, want %+v", got.EventStatus, tc.want)
			}
		})
	}

	for _, serverAuthored := range []bool{false, true} {
		s, items := build(t, serverAuthored)
		c := sep2.DERControl{}
		c.Href = "/x/no-id-here"
		c.Interval = &sep2.DateTimeInterval{Start: 10, Duration: 10}
		if err := items.Create(ctx, "P", "unkeyable", c); err != nil {
			t.Fatal(err)
		}
		_, err := s.List(ctx, "P", store.ListOptions{Unbounded: true})
		if serverAuthored && err == nil {
			t.Error("server-authored: List with an unkeyable member = nil error, want a refusal")
		}
		if !serverAuthored && err != nil {
			t.Errorf("not server-authored: List with an unkeyable member = %v, want it served as stored", err)
		}
	}
}
