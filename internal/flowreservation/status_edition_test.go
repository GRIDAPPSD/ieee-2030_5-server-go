package flowreservation

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for #798: potentiallySuperseded follows the edition, read from the
// wire XML of a served response.
func TestDerivedStatusResponseStore_PotentiallySupersededFollowsEdition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cancelledAt := int64(4242)
	for _, tc := range []struct {
		name        string
		edition2023 bool
		want        string
	}{
		{"2018 keeps false", false, "<potentiallySuperseded>false</potentiallySuperseded>"},
		{"2023 serves true", true, "<potentiallySuperseded>true</potentiallySuperseded>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
			lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
			for _, id := range []string{"R1", "R2"} {
				if err := responses.Create(ctx, "E1", id, storedResponse("E1", id)); err != nil {
					t.Fatal(err)
				}
			}
			// R1 is cancelled, R2 is live and still Scheduled.
			if err := lifecycles.Create(ctx, "E1", "R1", dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
				t.Fatal(err)
			}
			decorated := NewDerivedStatusResponseStoreFor(responses, lifecycles, tc.edition2023)
			for id, wantStatus := range map[string]uint8{"R1": sep2.EventStatusCancelled, "R2": sep2.EventStatusScheduled} {
				got, err := decorated.Get(ctx, "E1", id)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := xml.Marshal(&got)
				if err != nil {
					t.Fatal(err)
				}
				x := string(wire)
				if !strings.Contains(x, "<currentStatus>"+strconv.Itoa(int(wantStatus))+"</currentStatus>") {
					t.Errorf("%s: wire has no currentStatus %d: %s", id, wantStatus, x)
				}
				if !strings.Contains(x, tc.want) {
					t.Errorf("%s: wire lacks %s: %s", id, tc.want, x)
				}
			}
		})
	}
}

// A same-second revise stores creationTime one second past the second it
// was made in; a cancel read in that second must not carry an earlier
// dateTime than the response's own creationTime.
func TestDeriveEventStatus_CancelNeverPrecedesCreationTime(t *testing.T) {
	t.Parallel()
	cancelled := int64(1000)
	got := deriveEventStatus(5000, 1001, 1000, dercontrol.LifecycleRecord{CancelledAt: &cancelled})
	if got.CurrentStatus != sep2.EventStatusCancelled || got.DateTime != 1001 {
		t.Fatalf("EventStatus = %+v, want Cancelled at creationTime 1001", got)
	}
	later := int64(1500)
	got = deriveEventStatus(5000, 1001, 1500, dercontrol.LifecycleRecord{CancelledAt: &later})
	if got.DateTime != 1500 {
		t.Fatalf("DateTime = %d, want the cancel time 1500 when it is the later", got.DateTime)
	}
}
