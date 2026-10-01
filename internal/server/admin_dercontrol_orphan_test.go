package server_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// A device reserves, the deadline fallback grants, and the device's
// EndDevice is deleted; an unrelated device's DER control create still
// answers 201. The device side runs through the assembled protocol router
// and the create through the admin router, both over one set of stores.
func TestDERControlCreateSurvivesAnOrphanedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(t *testing.T, device *httptest.Server, stores *server.Stores)
		left   int
	}{
		// DELETE /edev cascades the device's flow reservation responses,
		// so the route itself leaves no orphan.
		{"DELETE /edev", deleteThroughRoute, 0},
		// A delete that bypasses the cascade (as the csip_test_hooks
		// out-of-band delete does) leaves the response behind, and the
		// grant scan must skip it.
		{"store delete", func(t *testing.T, _ *httptest.Server, stores *server.Stores) {
			if err := stores.EndDevices.Delete(context.Background(), "dev"); err != nil {
				t.Fatal(err)
			}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runOrphanedResponse(t, tc.remove, tc.left)
		})
	}
}

func runOrphanedResponse(t *testing.T, remove func(*testing.T, *httptest.Server, *server.Stores), wantLeft int) {
	const (
		deviceLFDI = "AABBCCDDEEFF0011223344556677889900112233"
		deviceSFDI = "AABBCCDD11223344"
		otherLFDI  = "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"
	)
	ctx := context.Background()
	stores := newTestStores()
	pen := uint32(0xA0B1)
	stores.PEN = &pen
	if err := stores.EndDevices.Create(ctx, "dev", sep2.EndDevice{LFDI: deviceLFDI}); err != nil {
		t.Fatal(err)
	}
	if err := stores.EndDevices.Create(ctx, "0", sep2.EndDevice{SFDI: "1", LFDI: otherLFDI}); err != nil {
		t.Fatal(err)
	}
	program := sep2.DERProgram{MRID: "P0", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/0/derp/0/derc"}}
	program.Href = "/edev/0/fsa/0/derp/0"
	if err := stores.DERPrograms.Create(ctx, "0", "0", program); err != nil {
		t.Fatal(err)
	}

	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return deviceLFDI, deviceSFDI, true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	protocol, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{FlowReservationDeadline: 20 * time.Millisecond},
		server.NewCoreStores(stores), policy, "serverSFDI", "serverLFDI", nil)
	device := httptest.NewServer(protocol)
	t.Cleanup(device.Close)
	admin, _ := adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), false, nil,
	)

	start := sep2time.Now().Unix() + 600
	body, err := xml.Marshal(&sep2.FlowReservationRequest{
		MRID:              "C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3",
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 1000},
		IntervalRequested: &sep2.DateTimeInterval{Start: start, Duration: 3600},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(device.URL+"/edev/dev/frq", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close() // only the status is read
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/dev/frq = %d, want 201", resp.StatusCode)
	}
	if n := waitForResponses(t, stores, "dev", 1); n != 1 {
		t.Fatalf("responses under dev = %d, want the fallback's 1", n)
	}

	remove(t, device, stores)
	if _, err := stores.EndDevices.Get(ctx, "dev"); err == nil {
		t.Fatal("EndDevice dev still stored after the delete")
	}
	left, err := stores.FlowReservationResponses.List(ctx, "dev", store.ListOptions{Unbounded: true})
	if err != nil || len(left.Items) != wantLeft {
		t.Fatalf("responses under the deleted device = %d (err %v), want %d", len(left.Items), err, wantLeft)
	}

	create := fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":5000,"startTime":%d,"durationSeconds":300}`, start)
	w := serveDERControl(admin, http.MethodPost, "/api/der/controls", derControlRouteRequest{remote: "127.0.0.1:4000", contentType: "application/json", bearer: "the-key", body: create})
	if w.Code != http.StatusCreated {
		t.Fatalf("create on an unrelated device = %d %s, want 201", w.Code, w.Body.String())
	}
}

func deleteThroughRoute(t *testing.T, device *httptest.Server, _ *server.Stores) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, device.URL+"/edev/dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drained so the connection is reused
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("DELETE /edev/dev = %d, want 2xx", resp.StatusCode)
	}
}

// waitForResponses polls the response store under edevID for up to 2 s.
func waitForResponses(t *testing.T, stores *server.Stores, edevID string, want int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := stores.FlowReservationResponses.List(context.Background(), edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) >= want || time.Now().After(deadline) {
			return len(page.Items)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
