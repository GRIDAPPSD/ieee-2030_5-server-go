package flow_reservation_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A pending check that fails must serve the registered pollRate and still
// serve the list, not fail the GET (#669).
func TestHandleListFlowReservationResponses_PollRate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pending flow_reservation.PendingFunc
		want    uint32
	}{
		{"pending serves the short rate", func(context.Context, string) (bool, error) { return true, nil }, 5},
		{"not pending serves the registered rate", func(context.Context, string) (bool, error) { return false, nil }, 900},
		{"failed check serves the registered rate", func(context.Context, string) (bool, error) { return true, errors.New("down") }, 900},
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
			mux.HandleFunc("GET /edev/{id}/frp", flow_reservation.HandleListFlowReservationResponses(responses, tc.pending, 900, 5))
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
