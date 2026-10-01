package assembly_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// TestFlowReservationResponse_CancelledServesCancelled: a response whose
// lifecycle record carries a cancel mark is served, on both read routes, as
// Cancelled at the mark's time; a response with no record is served as
// stored.
func TestFlowReservationResponse_CancelledServesCancelled(t *testing.T) {
	t.Parallel()
	srv, stores := frqServer(t)
	ctx := context.Background()

	for _, id := range []string{"R1", "R2"} {
		frp := sep2.FlowReservationResponse{}
		frp.Href = "/edev/e1/frp/" + id
		frp.MRID = "MRID-" + id
		frp.CreationTime = 100
		frp.Interval = &sep2.DateTimeInterval{Start: 1 << 40, Duration: 600}
		frp.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: 100}
		if err := stores.FlowReservationResponses.Create(ctx, "e1", id, frp); err != nil {
			t.Fatal(err)
		}
	}
	const cancelledAt = int64(777)
	at := cancelledAt
	if err := stores.FlowReservationResponseLifecycles.Create(ctx, "e1", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, frp sep2.FlowReservationResponse) {
		t.Helper()
		want := sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: 100}
		if frp.MRID == "MRID-R1" {
			want = sep2.EventStatus{CurrentStatus: sep2.EventStatusCancelled, DateTime: cancelledAt}
		}
		if frp.EventStatus == nil || frp.EventStatus.CurrentStatus != want.CurrentStatus || frp.EventStatus.DateTime != want.DateTime {
			t.Errorf("%s EventStatus = %+v, want %+v", frp.MRID, frp.EventStatus, want)
		}
	}

	for _, id := range []string{"R1", "R2"} {
		resp, err := srv.Client().Get(srv.URL + "/edev/e1/frp/" + id)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", id, resp.StatusCode)
		}
		var got sep2.FlowReservationResponse
		decodeXML(t, resp, &got)
		check(t, got)
	}

	resp, err := srv.Client().Get(srv.URL + "/edev/e1/frp")
	if err != nil {
		t.Fatal(err)
	}
	var list sep2.FlowReservationResponseList
	decodeXML(t, resp, &list)
	if len(list.FlowReservationResponse) != 2 {
		t.Fatalf("list has %d members, want 2", len(list.FlowReservationResponse))
	}
	for _, frp := range list.FlowReservationResponse {
		check(t, frp)
	}
}

// The read-only store handle serves the same derived status as the routes.
func TestReaderStores_FlowReservationResponseServesCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := testStores()
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/e1/frp/R1"
	frp.MRID = "MRID-R1"
	frp.CreationTime = 100
	frp.Interval = &sep2.DateTimeInterval{Start: 1 << 40, Duration: 600}
	if err := stores.FlowReservationResponses.Create(ctx, "e1", "R1", frp); err != nil {
		t.Fatal(err)
	}
	at := int64(777)
	if err := stores.FlowReservationResponseLifecycles.Create(ctx, "e1", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}
	got, err := assembly.NewReaderStores(stores).FlowReservationResponses.Get(ctx, "e1", "R1")
	if err != nil {
		t.Fatal(err)
	}
	if got.EventStatus == nil || got.EventStatus.CurrentStatus != sep2.EventStatusCancelled || got.EventStatus.DateTime != at {
		t.Errorf("EventStatus = %+v, want Cancelled at %d", got.EventStatus, at)
	}
}
