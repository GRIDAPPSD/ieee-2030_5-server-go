package assembly_test

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// TestFlowReservation_FallbackConsultsTheCommitmentLedger: the queue the
// router mounts is gated by the stores' commitment ledger (#714), so two
// requests on one fleet window, each answered by the deadline fallback,
// end as one grant and one denial. e1 and deviceA share the test LFDI and
// so one fleet.
func TestFlowReservation_FallbackConsultsTheCommitmentLedger(t *testing.T) {
	t.Parallel()
	srv, _ := frqServer(t)
	start := time.Now().Add(time.Hour).Unix()

	for i, edev := range []string{"e1", "deviceA"} {
		body, err := xml.Marshal(&sep2.FlowReservationRequest{
			MRID:              []string{"A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1", "B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2"}[i],
			EnergyRequested:   &sep2.SignedRealEnergy{Value: 1000},
			IntervalRequested: &sep2.DateTimeInterval{Start: start + int64(i)*60, Duration: 600},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		resp, err := srv.Client().Post(srv.URL+"/edev/"+edev+"/frq", "application/sep+xml", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("POST /edev/%s/frq: %v", edev, err)
		}
		_ = resp.Body.Close() // only the status is read
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /edev/%s/frq status = %d, want 201", edev, resp.StatusCode)
		}
	}

	var grants, denials int
	for _, edev := range []string{"e1", "deviceA"} {
		list := waitForFRPList(t, srv, "/edev/"+edev+"/frp", 1)
		frp := list.FlowReservationResponse[0]
		switch {
		case frp.Interval == nil:
			t.Fatalf("%s: response has no interval", edev)
		case frp.Interval.Duration == 600:
			grants++
		case frp.Interval.Duration == 0:
			denials++
		default:
			t.Fatalf("%s: response duration = %d, want 600 or 0", edev, frp.Interval.Duration)
		}
	}
	if grants != 1 || denials != 1 {
		t.Fatalf("grants = %d, denials = %d; want one of each on one fleet window", grants, denials)
	}
}
