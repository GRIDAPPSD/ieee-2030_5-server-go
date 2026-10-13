package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The mounted create route consults the process's commitment ledger: a
// plain control over a grant stored in Stores is refused naming it, and a
// Stores without a ledger refuses every create rather than passing it.
func TestDERControlCreateRouteUsesCommitmentLedger(t *testing.T) {
	const grantMRID = "FEDCBA9876543210FEDCBA9876543210"
	start := sep2time.Now().Unix() + 600
	body := fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","startTime":%d,"durationSeconds":300}`, start)

	for _, tc := range []struct {
		name     string
		ledger   bool
		managers bool
		wantCode int
	}{
		{"ledger wired", true, true, http.StatusConflict},
		{"no ledger", false, true, http.StatusInternalServerError},
		{"typed-nil management store", true, false, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			stores := newTestStores()
			if !tc.ledger {
				stores.CommitmentLedger = nil
			}
			if !tc.managers {
				stores.EndDeviceManagers = (*memory.EndDeviceManagementStore)(nil)
			}
			pen := uint32(0xA0B1)
			stores.PEN = &pen
			if err := stores.EndDevices.Create(ctx, "0", sep2.EndDevice{SFDI: "1", LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"}); err != nil {
				t.Fatal(err)
			}
			program := sep2.DERProgram{MRID: "P0", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/0/derp/0/derc"}}
			program.Href = "/edev/0/fsa/0/derp/0"
			if err := stores.DERPrograms.Create(ctx, "0", "0", program); err != nil {
				t.Fatal(err)
			}
			frp := sep2.FlowReservationResponse{}
			frp.Href = "/edev/0/frp/R1"
			frp.MRID = grantMRID
			frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 3600}
			if err := stores.FlowReservationResponses.Create(ctx, "0", "R1", frp); err != nil {
				t.Fatal(err)
			}
			router, _ := adminplane.BuildAdminRouter(
				"the-key", newScopeTestCertService(t), stores, "GCM",
				auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
				adminplane.DefaultAdminAllowedHosts(), nil,
			)

			w := serveDERControl(router, http.MethodPost, "/api/der/controls", derControlRouteRequest{remote: "127.0.0.1:4000", contentType: "application/json", bearer: "the-key", body: body})
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d body = %s, want %d", w.Code, w.Body.String(), tc.wantCode)
			}
			if tc.wantCode == http.StatusConflict {
				var got struct{ Code, MRID string }
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Code != "fleet_window_committed" || got.MRID != grantMRID {
					t.Errorf("409 body = %s, want the grant named", w.Body.String())
				}
			}
			parents, err := stores.DERControls.Parents(ctx)
			if err != nil || len(parents) != 0 {
				t.Errorf("control scopes %v (err %v) after a refusal, want none", parents, err)
			}
		})
	}
}
