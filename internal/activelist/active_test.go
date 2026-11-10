package activelist_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/activelist"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func ev(start int64, dur uint32, creation int64, status *sep2.EventStatus) *sep2.Event {
	e := &sep2.Event{CreationTime: creation, Interval: &sep2.DateTimeInterval{Start: start, Duration: dur}}
	e.EventStatus = status
	return e
}

// TestEventActive_Boundaries pins the instants at which an event enters and
// leaves the active set.
func TestEventActive_Boundaries(t *testing.T) {
	t.Parallel()
	const now = 10_000
	active := &sep2.EventStatus{CurrentStatus: sep2.EventStatusActive}
	cases := []struct {
		name string
		ev   *sep2.Event
		want bool
	}{
		{"starting now", ev(now, 600, now-10, nil), true},
		{"starts next second", ev(now+1, 600, now-10, nil), false},
		{"ending now", ev(now-600, 600, now-700, active), false},
		{"last second", ev(now-599, 600, now-700, active), true},
		{"no interval", &sep2.Event{EventStatus: active}, false},
		{"stamped scheduled", ev(now-5, 600, now-10, &sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled}), false},
		{"stamped cancelled", ev(now-5, 600, now-10, &sep2.EventStatus{CurrentStatus: sep2.EventStatusCancelled}), false},
		{"stamped superseded", ev(now-5, 600, now-10, &sep2.EventStatus{CurrentStatus: sep2.EventStatusSuperseded}), false},
		{"stamped active", ev(now-5, 600, now-10, active), true},
		{"stamped active, future start", ev(now+100, 600, now-10, active), true},
	}
	for _, tc := range cases {
		if got := activelist.EventActive(tc.ev, now); got != tc.want {
			t.Errorf("%s: EventActive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestActiveStore_FiltersBeforePaging proves All and Count describe the
// active members and a page is cut from them.
func TestActiveStore_FiltersBeforePaging(t *testing.T) {
	t.Parallel()
	inner := memory.NewScopedStore[sep2.DERControl]()
	now := sep2time.Now().Unix()
	seed := func(id string, start int64, dur uint32) {
		c := sep2.DERControl{DERControlBase: &sep2.DERControlBase{}}
		c.Href = "/edev/e/fsa/f/derp/p/derc/" + id
		c.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
		c.CreationTime = start - 10
		c.MRID = id
		if err := inner.Create(context.Background(), "e/f/p", id, c); err != nil {
			t.Fatal(err)
		}
	}
	seed("a", now-100, 3600)
	seed("old", now-9000, 10)
	// Sorts before every active id, so a page cut before the filter would
	// spend its one slot on it and return nothing.
	seed("0expired", now-9000, 10)
	seed("b", now-50, 3600)
	seed("future", now+500, 60)

	s := activelist.NewActiveStore[sep2.DERControl](inner, func(c *sep2.DERControl) *sep2.Event { return &c.Event })
	ctx := context.Background()

	if n, err := s.Count(ctx, "e/f/p"); err != nil || n != 2 {
		t.Fatalf("Count = %d, %v; want 2", n, err)
	}
	page, err := s.List(ctx, "e/f/p", store.ListOptions{Start: 1, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.All != 2 || page.Results != 1 || len(page.Items) != 1 {
		t.Fatalf("page all=%d results=%d items=%d, want 2, 1, 1", page.All, page.Results, len(page.Items))
	}
	first, err := s.List(ctx, "e/f/p", store.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.All != 2 || first.Results != 1 || len(first.Items) != 1 || first.Items[0].MRID != "a" {
		t.Fatalf("first page all=%d results=%d items=%+v, want 2, 1, the control a", first.All, first.Results, first.Items)
	}
	whole, err := s.List(ctx, "e/f/p", store.ListOptions{Unbounded: true})
	if err != nil || whole.All != 2 || whole.Results != 2 {
		t.Fatalf("unbounded all=%d results=%d err=%v, want 2, 2", whole.All, whole.Results, err)
	}
	if _, err := s.List(ctx, "e/f/p", store.ListOptions{Unbounded: true, Limit: 3}); err == nil {
		t.Error("Unbounded with Limit accepted, want store.ErrInvalidListOptions")
	}
}
