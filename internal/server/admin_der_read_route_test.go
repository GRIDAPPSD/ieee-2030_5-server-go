package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #1209: the per-device DER read route is mounted, needs a credential, and
// reaches its own handler. A status of 404 alone cannot show that, because
// the mux answers 404 for an unmounted path too; the handler's own refusal
// body can.
func TestDERReadRouteIsMountedAndCredentialed(t *testing.T) {
	stores := newTestStores()
	ctx := context.Background()
	if err := stores.EndDevices.Create(ctx, "3", sep2.EndDevice{LFDI: "D001000000000000000000000000000000000001", SFDI: "3"}); err != nil {
		t.Fatal(err)
	}
	der := sep2.DER{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/der/1"}}}
	if err := stores.DERs.Create(ctx, "3", "1", der); err != nil {
		t.Fatal(err)
	}
	router, _ := adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), http.NotFoundHandler(),
	)
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("no credential is refused", func(t *testing.T) {
		rec := serve(bypassOnlyRequest(http.MethodGet, "/api/derms/devices/3/ders"))
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
			t.Fatalf("status %d body %q, want 401 and the refusal body", rec.Code, rec.Body.String())
		}
	})
	t.Run("known device with a credential reads its DERs", func(t *testing.T) {
		rec := serve(bearerFromLoopbackRequest(http.MethodGet, "/api/derms/devices/3/ders", "", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
		}
		var reply struct {
			Device string `json:"device"`
			DERs   []struct {
				ID     string `json:"id"`
				Status struct {
					Reported    bool   `json:"reported"`
					ReadingTime *int64 `json:"readingTime"`
				} `json:"status"`
			} `json:"ders"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Device != "3" || len(reply.DERs) != 1 || reply.DERs[0].ID != "1" || reply.DERs[0].Status.Reported || reply.DERs[0].Status.ReadingTime != nil {
			t.Errorf("reply = %+v", reply)
		}
	})
	t.Run("unknown device with a credential is the handler's 404", func(t *testing.T) {
		rec := serve(bearerFromLoopbackRequest(http.MethodGet, "/api/derms/devices/99/ders", "", ""))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "device not found") {
			t.Fatalf("status %d body %q, want 404 with the handler's body", rec.Code, rec.Body.String())
		}
	})
	t.Run("control: an unrouted path gets the mux 404, not the handler's", func(t *testing.T) {
		rec := serve(bearerFromLoopbackRequest(http.MethodGet, "/api/derms/devices/99/nothing", "", ""))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "device not found") {
			t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
		}
	})
}
