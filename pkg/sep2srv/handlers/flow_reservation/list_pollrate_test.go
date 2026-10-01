package flow_reservation_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A pending check that fails must serve the short pollRate and still serve
// the list, not fail the GET (#669).
func TestHandleListFlowReservationResponses_PollRate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pending flow_reservation.PendingFunc
		want    uint32
	}{
		{"pending serves the short rate", func(context.Context, string) (bool, error) { return true, nil }, 5},
		{"not pending serves the registered rate", func(context.Context, string) (bool, error) { return false, nil }, 900},
		{"failed check serves the short rate", func(context.Context, string) (bool, error) { return false, errors.New("down") }, 5},
		{"no check serves the registered rate", nil, 900},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
			var frp sep2.FlowReservationResponse
			frp.MRID = "ABCD"
			if err := responses.Create(context.Background(), "e1", "r1", frp); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /edev/{id}/frp", flow_reservation.HandleListFlowReservationResponses(responses, tc.pending, 900, 5, true))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/edev/e1/frp", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var list sep2.FlowReservationResponseList
			if err := xml.Unmarshal(rec.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			if list.PollRate != tc.want || len(list.FlowReservationResponse) != 1 || list.FlowReservationResponse[0].MRID != "ABCD" {
				t.Errorf("pollRate %d, members %+v; want pollRate %d and the one stored response", list.PollRate, list.FlowReservationResponse, tc.want)
			}
		})
	}
}

// The list's subscribable attribute is read from the marshaled bytes: an
// absent attribute means not subscribable (2030.5 B.2).
func TestFlowReservationResponseList_SubscribableOnTheWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		subscribable bool
		want         bool
	}{
		{"wired for notifications", true, true},
		{"nobody to notify", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			responses := memory.NewScopedStore[sep2.FlowReservationResponse]()
			mux := http.NewServeMux()
			mux.HandleFunc("GET /edev/{id}/frp", flow_reservation.HandleListFlowReservationResponses(responses, nil, 900, 5, tc.subscribable))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/edev/e1/frp", nil))

			has := strings.Contains(rec.Body.String(), `subscribable="1"`)
			if has != tc.want {
				t.Errorf("body has subscribable=\"1\": %v, want %v\n%s", has, tc.want, rec.Body.String())
			}
		})
	}
}

func TestBuildFlowReservationResponseList_AdvertisesSubscribable(t *testing.T) {
	t.Parallel()
	list := flow_reservation.BuildFlowReservationResponseList("/edev/e1/frp", store.ListResult[sep2.FlowReservationResponse]{}, 900)
	b, err := xml.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `subscribable="1"`) {
		t.Errorf("marshaled list = %s, want subscribable=\"1\"", b)
	}
}
