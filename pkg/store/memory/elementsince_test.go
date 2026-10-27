package memory_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
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

	if _, err := st.(interface {
		DeleteParentsWithPrefix(context.Context, string) (uint32, error)
	}).DeleteParentsWithPrefix(context.Background(), "dev/"); err != nil {
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

	if _, err := st.(interface {
		DeleteParent(context.Context, string) (uint32, error)
	}).DeleteParent(context.Background(), "other/1"); err != nil {
		t.Fatal(err)
	}
	if n := len(st.ElementSince("other/1", "status")); n != 0 {
		t.Errorf("DeleteParent kept %d times", n)
	}
	if n := len(st.ElementSince("dev2/1", "status")); n != 1 {
		t.Errorf("DeleteParent touched another parent: %d", n)
	}
}

// plainScoped hides every optional capability of the store it wraps.
type plainScoped[T store.Copier[T]] struct{ store.ScopedStore[T] }

func TestElementSince_ExposesCascadesOnlyWhenInnerHasThem(t *testing.T) {
	type parent interface {
		DeleteParent(context.Context, string) (uint32, error)
	}
	type prefix interface {
		DeleteParentsWithPrefix(context.Context, string) (uint32, error)
	}
	full := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), nil)
	if _, ok := full.(parent); !ok {
		t.Error("inner has DeleteParent, wrapper hides it")
	}
	if _, ok := full.(prefix); !ok {
		t.Error("inner has DeleteParentsWithPrefix, wrapper hides it")
	}
	bare := memory.NewDERStatusSinceStore(plainScoped[sep2.DERStatus]{memory.NewScopedStore[sep2.DERStatus]()}, nil)
	if _, ok := bare.(parent); ok {
		t.Error("inner lacks DeleteParent, wrapper claims it")
	}
	if _, ok := bare.(prefix); ok {
		t.Error("inner lacks DeleteParentsWithPrefix, wrapper claims it")
	}
	if _, ok := bare.(store.ElementSinceReader); !ok {
		t.Error("wrapper lost ElementSince")
	}
}

// Every pointer field of the resource is a schema element; a new one that the
// fingerprint forgets would never get a since.
func TestElementSince_FingerprintCoversEveryElement(t *testing.T) {
	cases := []struct {
		name string
		got  func() (int, int)
	}{
		{"DERStatus", func() (int, int) {
			var v sep2.DERStatus
			return fillPointers(&v), len(memory.DERStatusElementsForTest(v))
		}},
		{"DERAvailability", func() (int, int) {
			var v sep2.DERAvailability
			return fillPointers(&v), len(memory.DERAvailabilityElementsForTest(v))
		}},
	}
	for _, c := range cases {
		want, got := c.got()
		if want == 0 || got != want {
			t.Errorf("%s: %d pointer elements, fingerprint covers %d", c.name, want, got)
		}
	}
}

func fillPointers(v any) int {
	rv := reflect.ValueOf(v).Elem()
	n := 0
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if f.Kind() == reflect.Pointer {
			f.Set(reflect.New(f.Type().Elem()))
			n++
		}
	}
	return n
}

func TestElementSince_ConcurrentWriters(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clk.Now)
	put(t, st, "1/1", socStatus(1, 1, 1))
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if err := st.Update(context.Background(), "1/1", "status", socStatus(uint16(w*1000+i), 1, 1)); err != nil {
					t.Errorf("update: %v", err)
					return
				}
				st.ElementSince("1/1", "status")
			}
		}(w)
	}
	wg.Wait()
	if _, ok := st.ElementSince("1/1", "status")["stateOfChargeStatus"]; !ok {
		t.Fatal("element lost under concurrent writers")
	}
}

func TestElementSince_DeleteNotFoundClearsStaleTime(t *testing.T) {
	clk := newClock()
	inner := memory.NewScopedStore[sep2.DERStatus]()
	st := memory.NewDERStatusSinceStore(inner, clk.Now)
	put(t, st, "1/1", socStatus(8000, 1, 1))
	// Removed behind the decorator's back, so its record is stale.
	if err := inner.Delete(context.Background(), "1/1", "status"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(context.Background(), "1/1", "status"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete = %v, want ErrNotFound", err)
	}
	if n := len(st.ElementSince("1/1", "status")); n != 0 {
		t.Fatalf("stale time kept: %d", n)
	}
}

// failingBulk deletes the resources under the parent, then reports an error.
type failingBulk struct {
	*memory.ScopedStore[sep2.DERStatus]
}

func (f failingBulk) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	n, _ := f.ScopedStore.DeleteParent(ctx, parentID)
	return n, errors.New("late failure")
}

func TestElementSince_BulkDeleteErrorStillClearsDeleted(t *testing.T) {
	clk := newClock()
	st := memory.NewDERStatusSinceStore(failingBulk{memory.NewScopedStore[sep2.DERStatus]()}, clk.Now)
	put(t, st, "1/1", socStatus(8000, 1, 1))
	_, err := st.(interface {
		DeleteParent(context.Context, string) (uint32, error)
	}).DeleteParent(context.Background(), "1/1")
	if err == nil {
		t.Fatal("want the inner error")
	}
	if n := len(st.ElementSince("1/1", "status")); n != 0 {
		t.Fatalf("deleted resource keeps %d times", n)
	}
}
