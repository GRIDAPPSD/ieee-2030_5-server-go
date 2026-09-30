package flowreservation

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestDeriveEventStatus_CancelledRecordReadsCancelledAtItsTime(t *testing.T) {
	t.Parallel()
	at := int64(1500)
	got := deriveEventStatus(1000, 900, 5000, dercontrol.LifecycleRecord{CancelledAt: &at})
	if got.CurrentStatus != sep2.EventStatusCancelled || got.DateTime != at {
		t.Fatalf("EventStatus = %+v, want Cancelled at %d", got, at)
	}
}

func TestResponseID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, edev, href, want string
		ok                     bool
	}{
		{"the shape build writes", "E1", responseHref("E1", "R7"), "R7", true},
		{"another device's response", "E2", "/edev/E1/frp/R7", "", false},
		{"no id", "E1", "/edev/E1/frp/", "", false},
		{"nested path", "E1", "/edev/E1/frp/R7/x", "", false},
		{"request href", "E1", "/edev/E1/frq/R7", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ResponseID(tc.edev, tc.href)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ResponseID(%q, %q) = (%q, %v), want (%q, %v)", tc.edev, tc.href, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// storedResponse is a grant as build stores it: Scheduled at creation,
// with a start far in the future so a re-derivation without a record
// could not change it either.
func storedResponse(edev, id string) sep2.FlowReservationResponse {
	frp := sep2.FlowReservationResponse{}
	frp.Href = responseHref(edev, id)
	frp.MRID = "MRID-" + id
	frp.CreationTime = 100
	frp.Interval = &sep2.DateTimeInterval{Start: 1 << 40, Duration: 600}
	es := sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: 100}
	frp.EventStatus = &es
	return frp
}

func TestDerivedStatusResponseStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
	lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
	for _, id := range []string{"R1", "R2"} {
		if err := responses.Create(ctx, "E1", id, storedResponse("E1", id)); err != nil {
			t.Fatal(err)
		}
	}
	cancelledAt := int64(4242)
	if err := lifecycles.Create(ctx, "E1", "R1", dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
		t.Fatal(err)
	}
	decorated := NewDerivedStatusResponseStore(responses, lifecycles)

	wantCancelled := func(t *testing.T, frp sep2.FlowReservationResponse) {
		t.Helper()
		if frp.EventStatus == nil || frp.EventStatus.CurrentStatus != sep2.EventStatusCancelled || frp.EventStatus.DateTime != cancelledAt {
			t.Errorf("%s EventStatus = %+v, want Cancelled at %d", frp.MRID, frp.EventStatus, cancelledAt)
		}
	}
	wantAsStored := func(t *testing.T, frp sep2.FlowReservationResponse) {
		t.Helper()
		if frp.EventStatus == nil || frp.EventStatus.CurrentStatus != sep2.EventStatusScheduled || frp.EventStatus.DateTime != 100 {
			t.Errorf("%s EventStatus = %+v, want the stored Scheduled at 100", frp.MRID, frp.EventStatus)
		}
	}

	t.Run("Get serves a cancelled response as Cancelled with its time", func(t *testing.T) {
		got, err := decorated.Get(ctx, "E1", "R1")
		if err != nil {
			t.Fatal(err)
		}
		wantCancelled(t, got)
	})
	t.Run("Get serves a response with no record exactly as stored", func(t *testing.T) {
		got, err := decorated.Get(ctx, "E1", "R2")
		if err != nil {
			t.Fatal(err)
		}
		wantAsStored(t, got)
	})
	t.Run("List derives each member from its own record", func(t *testing.T) {
		page, err := decorated.List(ctx, "E1", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("List returned %d items, want 2", len(page.Items))
		}
		for _, frp := range page.Items {
			switch frp.MRID {
			case "MRID-R1":
				wantCancelled(t, frp)
			case "MRID-R2":
				wantAsStored(t, frp)
			default:
				t.Errorf("unexpected member %s", frp.MRID)
			}
		}
	})
	t.Run("the stored response is not rewritten", func(t *testing.T) {
		raw, err := responses.Get(ctx, "E1", "R1")
		if err != nil {
			t.Fatal(err)
		}
		wantAsStored(t, raw)
	})
}

var errLifecycleDown = errors.New("fake: lifecycle store unreachable")

type failingLifecycles struct{}

func (failingLifecycles) Get(context.Context, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, errLifecycleDown
}

// A lookup failure fails the read: serving the stored status would show a
// cancelled grant as live.
func TestDerivedStatusResponseStore_LookupFailureFailsTheRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
	if err := responses.Create(ctx, "E1", "R1", storedResponse("E1", "R1")); err != nil {
		t.Fatal(err)
	}
	decorated := NewDerivedStatusResponseStore(responses, failingLifecycles{})
	if _, err := decorated.Get(ctx, "E1", "R1"); !errors.Is(err, errLifecycleDown) {
		t.Errorf("Get = %v, want the lifecycle failure", err)
	}
	if _, err := decorated.List(ctx, "E1", store.ListOptions{Unbounded: true}); !errors.Is(err, errLifecycleDown) {
		t.Errorf("List = %v, want the lifecycle failure", err)
	}
}

// A member whose href yields no store id cannot be matched to its record,
// so the list fails rather than serve it with an unchecked status.
func TestDerivedStatusResponseStore_ListRefusesAnUnkeyableMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
	frp := storedResponse("E1", "R1")
	frp.Href = "/edev/E1/frq/R1"
	if err := responses.Create(ctx, "E1", "R1", frp); err != nil {
		t.Fatal(err)
	}
	decorated := NewDerivedStatusResponseStore(responses, memory.NewScopedStore[dercontrol.LifecycleRecord]())
	if _, err := decorated.List(ctx, "E1", store.ListOptions{Unbounded: true}); err == nil {
		t.Fatal("List = nil error, want a refusal for a member with no store id")
	}
}
