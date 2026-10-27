package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)} }

func socStatus(perCent uint16, dateTime, reading int64) sep2.DERStatus {
	return sep2.DERStatus{
		ReadingTime:         reading,
		StateOfChargeStatus: &sep2.StateOfChargeStatusType{DateTime: dateTime, Value: perCent},
	}
}

func put(t *testing.T, s store.ScopedStore[sep2.DERStatus], parent string, v sep2.DERStatus) {
	t.Helper()
	ctx := context.Background()
	err := s.Update(ctx, parent, "status", v)
	if errors.Is(err, store.ErrNotFound) {
		err = s.Create(ctx, parent, "status", v)
	}
	if err != nil {
		t.Fatalf("put: %v", err)
	}
}

func TestElementSince_RepeatedPutKeepsChangedPutResets(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	t0 := clk.now

	put(t, st, "1/1", socStatus(8000, 100, 100))
	clk.Advance(9 * time.Hour)
	// Same value, newer dateTime and readingTime: the interval continues.
	put(t, st, "1/1", socStatus(8000, 200, 200))
	if got := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !got.Equal(t0) {
		t.Fatalf("repeated PUT: since = %v, want %v", got, t0)
	}

	clk.Advance(time.Hour)
	put(t, st, "1/1", socStatus(7900, 300, 300))
	want := t0.Add(10 * time.Hour)
	if got := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !got.Equal(want) {
		t.Fatalf("changed PUT: since = %v, want %v", got, want)
	}
}

func TestElementSince_ElementsAreIndependentAndAbsentHasNone(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	t0 := clk.now

	v := socStatus(8000, 1, 1)
	v.OperationalModeStatus = &sep2.OperationalModeStatusType{Value: 2}
	put(t, st, "1/1", v)
	clk.Advance(time.Hour)

	v.OperationalModeStatus.Value = 3
	put(t, st, "1/1", v)

	got := st.ElementSince("1/1", "status")
	if !got["stateOfChargeStatus"].Equal(t0) {
		t.Errorf("unchanged soc since = %v, want %v", got["stateOfChargeStatus"], t0)
	}
	if !got["operationalModeStatus"].Equal(t0.Add(time.Hour)) {
		t.Errorf("changed mode since = %v, want %v", got["operationalModeStatus"], t0.Add(time.Hour))
	}
	for _, absent := range []string{"alarmStatus", "storageModeStatus", "genConnectStatus", "inverterStatus"} {
		if _, ok := got[absent]; ok {
			t.Errorf("absent element %s has a since", absent)
		}
	}
	if len(got) != 2 {
		t.Errorf("recorded %d elements, want 2: %v", len(got), got)
	}
}

func TestElementSince_RemovedAndReAddedElementGetsNewSince(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)

	put(t, st, "1/1", socStatus(8000, 1, 1))
	clk.Advance(time.Hour)
	put(t, st, "1/1", sep2.DERStatus{ReadingTime: 2})
	if _, ok := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; ok {
		t.Fatal("removed element still has a since")
	}
	clk.Advance(time.Hour)
	put(t, st, "1/1", socStatus(8000, 3, 3))
	want := newClock().now.Add(2 * time.Hour)
	if got := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !got.Equal(want) {
		t.Fatalf("re-added since = %v, want %v", got, want)
	}
}

func TestElementSince_DevicesDoNotShareState(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	t0 := clk.now

	put(t, st, "1/1", socStatus(8000, 1, 1))
	clk.Advance(time.Hour)
	put(t, st, "2/1", socStatus(8000, 1, 1))

	if got := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !got.Equal(t0) {
		t.Errorf("device 1 since = %v, want %v", got, t0)
	}
	if got := st.ElementSince("2/1", "status")["stateOfChargeStatus"]; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("device 2 since = %v, want %v", got, t0.Add(time.Hour))
	}

	if err := st.Delete(context.Background(), "1/1", "status"); err != nil {
		t.Fatal(err)
	}
	if n := len(st.ElementSince("1/1", "status")); n != 0 {
		t.Errorf("deleted resource keeps %d times", n)
	}
	if n := len(st.ElementSince("2/1", "status")); n != 1 {
		t.Errorf("other device lost its times: %d", n)
	}
}

func TestElementSince_RefusedWriteRecordsNothing(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	if err := st.Update(context.Background(), "1/1", "status", socStatus(8000, 1, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update on empty store: %v, want ErrNotFound", err)
	}
	if n := len(st.ElementSince("1/1", "status")); n != 0 {
		t.Fatalf("refused Update recorded %d times", n)
	}
}

func TestElementSince_ReturnsACopy(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	put(t, st, "1/1", socStatus(8000, 1, 1))
	m := st.ElementSince("1/1", "status")
	delete(m, "stateOfChargeStatus")
	if _, ok := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !ok {
		t.Fatal("caller mutation reached the store")
	}
}

func TestElementSince_Availability(t *testing.T) {
	clk := newClock()
	st := memory.NewDERAvailabilitySinceStore(memory.NewScopedStore[sep2.DERAvailability](), clk.Now)
	t0 := clk.now
	dur := uint32(7200)
	a := sep2.DERAvailability{ReadingTime: 1, AvailabilityDuration: &dur, StatWAvail: &sep2.ActivePower{Multiplier: 0, Value: 5000}}
	if err := st.Create(context.Background(), "1/1", "availability", a); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	a.ReadingTime = 2
	a.StatWAvail = &sep2.ActivePower{Multiplier: 0, Value: 4000}
	if err := st.Update(context.Background(), "1/1", "availability", a); err != nil {
		t.Fatal(err)
	}
	got := st.ElementSince("1/1", "availability")
	if !got["availabilityDuration"].Equal(t0) {
		t.Errorf("availabilityDuration since = %v, want %v", got["availabilityDuration"], t0)
	}
	if !got["statWAvail"].Equal(t0.Add(time.Hour)) {
		t.Errorf("statWAvail since = %v, want %v", got["statWAvail"], t0.Add(time.Hour))
	}
	if len(got) != 2 {
		t.Errorf("recorded %d elements, want 2", len(got))
	}
}

func TestElementSince_BulkDeletesDropTheirTimes(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	put(t, st, "dev/1", socStatus(8000, 1, 1))
	put(t, st, "dev/2", socStatus(8000, 1, 1))
	put(t, st, "other/1", socStatus(8000, 1, 1))
	put(t, st, "dev2/1", socStatus(8000, 1, 1))

	if _, err := st.DeleteParentsWithPrefix(context.Background(), "dev/"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"dev/1", "dev/2"} {
		if n := len(st.ElementSince(p, "status")); n != 0 {
			t.Errorf("%s keeps %d times after prefix delete", p, n)
		}
	}
	for _, p := range []string{"other/1", "dev2/1"} {
		if n := len(st.ElementSince(p, "status")); n != 1 {
			t.Errorf("%s lost its times: %d", p, n)
		}
	}

	if _, err := st.DeleteParent(context.Background(), "other/1"); err != nil {
		t.Fatal(err)
	}
	if n := len(st.ElementSince("other/1", "status")); n != 0 {
		t.Errorf("DeleteParent kept %d times", n)
	}
	if n := len(st.ElementSince("dev2/1", "status")); n != 1 {
		t.Errorf("DeleteParent touched another parent: %d", n)
	}
}
