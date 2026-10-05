package server_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const frAgreeLFDI = "AABBCCDDEEFF0011223344556677889900112233"

// frAgreementFixture stores three responses to one device, scheduled, active
// and cancelled against the protocol clock, and returns the protocol server
// and the admin router over the same stores.
func frAgreementFixture(t *testing.T) (device *httptest.Server, admin http.Handler, stores *server.Stores) {
	t.Helper()
	ctx := context.Background()
	stores = newTestStores()
	if err := stores.EndDevices.Create(ctx, "dev", sep2.EndDevice{LFDI: frAgreeLFDI}); err != nil {
		t.Fatal(err)
	}
	now := sep2time.Now().Unix()
	seed := func(id string, created, start int64, lc *dercontrol.LifecycleRecord) {
		frq := sep2.FlowReservationRequest{
			Resource: sep2.Resource{Href: "/edev/dev/frq/" + id}, MRID: "M-" + id, CreationTime: created,
			IntervalRequested: &sep2.DateTimeInterval{Start: start, Duration: 3600},
			EnergyRequested:   &sep2.SignedRealEnergy{Value: 1000},
		}
		frp := sep2.FlowReservationResponse{
			Event: sep2.Event{
				SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev/frp/" + id}},
				MRID:                 "R-" + id, CreationTime: created, Interval: &sep2.DateTimeInterval{Start: start, Duration: 3600},
			},
			EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 500}, Subject: "M-" + id,
		}
		if err := stores.FlowReservationRequests.Create(ctx, "dev", id, frq); err != nil {
			t.Fatal(err)
		}
		if err := stores.FlowReservationResponses.Create(ctx, "dev", id, frp); err != nil {
			t.Fatal(err)
		}
		if lc != nil {
			if err := stores.FlowReservationResponseLifecycles.Create(ctx, "dev", id, *lc); err != nil {
				t.Fatal(err)
			}
		}
	}
	cancelledAt := now - 30
	seed("frq-scheduled", now-100, now+3600, nil)
	seed("frq-active", now-200, now-100, nil)
	seed("frq-cancelled", now-300, now+3600, &dercontrol.LifecycleRecord{CancelledAt: &cancelledAt})

	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return frAgreeLFDI, "AABBCCDD11223344", true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	protocol, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{FlowReservationDeadline: time.Hour},
		server.NewCoreStores(stores), policy, "serverSFDI", "serverLFDI", nil)
	device = httptest.NewServer(protocol)
	t.Cleanup(device.Close)
	admin, _ = adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), nil,
	)
	return device, admin, stores
}

// The admin view and the protocol GET must report the same EventStatus for a
// scheduled, an active and a cancelled response, because both read it from
// the one decorated store. The expected values are pinned too, so two
// matching wrong answers cannot pass.
func TestAdminFlowReservationEventStatusMatchesProtocolGET(t *testing.T) {
	device, admin, _ := frAgreementFixture(t)

	w := httptest.NewRecorder()
	admin.ServeHTTP(w, bearerFromLoopbackRequest(http.MethodGet, "/api/derms/flow-reservations?aggregatorLFDI="+frAgreeLFDI, "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("admin list = %d %s, want 200", w.Code, w.Body.String())
	}
	var list struct {
		Requests []struct {
			FrqID string `json:"frqId"`
			Tip   struct {
				EventStatus struct {
					CurrentStatus uint8  `json:"currentStatus"`
					Status        string `json:"status"`
					DateTime      int64  `json:"dateTime"`
				} `json:"eventStatus"`
			} `json:"tip"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode admin list: %v\n%s", err, w.Body.String())
	}
	if len(list.Requests) != 3 {
		t.Fatalf("admin list holds %d requests, want 3", len(list.Requests))
	}

	want := map[string]struct {
		current uint8
		word    string
	}{
		"frq-scheduled": {sep2.EventStatusScheduled, "scheduled"},
		"frq-active":    {sep2.EventStatusActive, "active"},
		"frq-cancelled": {sep2.EventStatusCancelled, "cancelled"},
	}
	for _, r := range list.Requests {
		resp, err := http.Get(device.URL + "/edev/dev/frp/" + r.FrqID)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("protocol GET frp/%s = %d (%v) %s, want 200", r.FrqID, resp.StatusCode, err, body)
		}
		var proto sep2.FlowReservationResponse
		if err := xml.Unmarshal(body, &proto); err != nil || proto.EventStatus == nil {
			t.Fatalf("protocol frp/%s: decode %v, EventStatus %v\n%s", r.FrqID, err, proto.EventStatus, body)
		}

		got := r.Tip.EventStatus
		if got.CurrentStatus != proto.EventStatus.CurrentStatus || got.DateTime != proto.EventStatus.DateTime {
			t.Errorf("%s: admin eventStatus = {%d %d}, protocol GET = {%d %d}", r.FrqID, got.CurrentStatus, got.DateTime,
				proto.EventStatus.CurrentStatus, proto.EventStatus.DateTime)
		}
		if w := want[r.FrqID]; got.CurrentStatus != w.current || got.Status != w.word {
			t.Errorf("%s: admin eventStatus = %d %q, want %d %q", r.FrqID, got.CurrentStatus, got.Status, w.current, w.word)
		}
	}
}

// The read routes change nothing: any write method is refused, and the
// stores they read are field-for-field as they were.
func TestAdminFlowReservationRoutesAreReadOnly(t *testing.T) {
	_, admin, stores := frAgreementFixture(t)
	ctx := context.Background()
	before, err := stores.FlowReservationResponses.List(ctx, "dev", store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/derms/flow-reservations?aggregatorLFDI=" + frAgreeLFDI,
		"/api/derms/flow-reservations/dev/frq-active",
		"/api/derms/grants?aggregatorLFDI=" + frAgreeLFDI + "&live=true",
		"/api/derms/commitments?aggregatorLFDI=" + frAgreeLFDI,
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			w := httptest.NewRecorder()
			admin.ServeHTTP(w, bearerFromLoopbackRequest(method, path, "application/json", "{}"))
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s with a valid credential = %d %s, want 405", method, path, w.Code, w.Body.String())
			}
		}
	}

	after, err := stores.FlowReservationResponses.List(ctx, "dev", store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != len(before.Items) {
		t.Fatalf("responses = %d after the writes, want the %d before", len(after.Items), len(before.Items))
	}
	for i := range before.Items {
		a, _ := json.Marshal(after.Items[i])
		b, _ := json.Marshal(before.Items[i])
		if string(a) != string(b) {
			t.Errorf("response %d changed: %s -> %s", i, b, a)
		}
	}
}

// A request that carries no credential from loopback is refused: these
// routes follow /api/derms/fleets, not the dashboard's open reads.
func TestAdminFlowReservationRoutesNeedACredential(t *testing.T) {
	_, admin, _ := frAgreementFixture(t)
	for _, path := range []string{
		"/api/derms/flow-reservations?aggregatorLFDI=" + frAgreeLFDI,
		"/api/derms/flow-reservations/dev/frq-active",
		"/api/derms/grants?aggregatorLFDI=" + frAgreeLFDI + "&live=true",
		"/api/derms/commitments?aggregatorLFDI=" + frAgreeLFDI,
	} {
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, bypassOnlyRequest(http.MethodGet, path))
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "admin authentication required") {
			t.Errorf("GET %s with no credential = %d %s, want 401", path, w.Code, w.Body.String())
		}
	}
}

// An execution's eventStatus, read through the admin router, is the one the
// protocol serves for a control: derived from its lifecycle record.
func TestAdminFlowReservationExecutionStatusThroughTheRouter(t *testing.T) {
	_, admin, stores := frAgreementFixture(t)
	ctx := context.Background()
	now := sep2time.Now().Unix()
	mk := func(id string, cancelledAt *int64) {
		ctrl := sep2.DERControl{
			RandomizableEvent: sep2.RandomizableEvent{Event: sep2.Event{
				SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/7/fsa/1/derp/1/derc/" + id}},
				MRID:                 "C-" + id, CreationTime: now - 150, Interval: &sep2.DateTimeInterval{Start: now - 100, Duration: 600},
			}},
			DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: -100}},
		}
		if err := stores.DERControls.Create(ctx, "7/1/1", id, ctrl); err != nil {
			t.Fatal(err)
		}
		lc := dercontrol.LifecycleRecord{GrantMRID: "R-frq-active", FleetKey: frAgreeLFDI, Reach: 1, CancelledAt: cancelledAt}
		if err := stores.DERControlLifecycles.Create(ctx, "7/1/1", id, lc); err != nil {
			t.Fatal(err)
		}
	}
	cancelledAt := now - 20
	mk("1", nil)
	mk("2", &cancelledAt)

	w := httptest.NewRecorder()
	admin.ServeHTTP(w, bearerFromLoopbackRequest(http.MethodGet, "/api/derms/flow-reservations/dev/frq-active", "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body.String())
	}
	var v struct {
		Tip struct {
			Executions []struct {
				MRID        string `json:"mRID"`
				Href        string `json:"href"`
				EventStatus struct {
					CurrentStatus uint8  `json:"currentStatus"`
					Status        string `json:"status"`
					DateTime      int64  `json:"dateTime"`
				} `json:"eventStatus"`
			} `json:"executions"`
		} `json:"tip"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || len(v.Tip.Executions) != 2 {
		t.Fatalf("decode %v, %d executions\n%s", err, len(v.Tip.Executions), w.Body.String())
	}
	a, c := v.Tip.Executions[0], v.Tip.Executions[1]
	if a.MRID != "C-1" || a.EventStatus.CurrentStatus != sep2.EventStatusActive || a.EventStatus.DateTime != now-100 || a.Href != "/edev/7/fsa/1/derp/1/derc/1" {
		t.Errorf("live execution = %+v, want active since %d under its own href", a, now-100)
	}
	if c.MRID != "C-2" || c.EventStatus.CurrentStatus != sep2.EventStatusCancelled || c.EventStatus.DateTime != cancelledAt {
		t.Errorf("cancelled execution = %+v, want cancelled at %d", c, cancelledAt)
	}
}
