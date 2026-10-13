package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// #566 criteria 7 and 10: the DER control routes through the full admin
// chain. The two POST routes change what a physical device does, so they
// refuse the loopback bypass; the two GET routes keep today's posture.

const derControlTestMRID = "0123456789ABCDEF0123456789ABCDEF"

func newDERControlRouter(t *testing.T) http.Handler {
	t.Helper()
	stores := newTestStores()
	pen := uint32(0xA0B1)
	stores.PEN = &pen
	ctx := context.Background()
	if err := stores.EndDevices.Create(ctx, "0", sep2.EndDevice{SFDI: "1", LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"}); err != nil {
		t.Fatal(err)
	}
	program := sep2.DERProgram{MRID: "P0", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/0/derp/0/derc"}}
	program.Href = "/edev/0/fsa/0/derp/0"
	if err := stores.DERPrograms.Create(ctx, "0", "0", program); err != nil {
		t.Fatal(err)
	}
	router, _ := adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), nil,
	)
	return router
}

func derControlCreateBody() string {
	return fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":6000,"startTime":%d,"durationSeconds":300}`, sep2time.Now().Unix()+600)
}

type derControlRouteRequest struct {
	remote, contentType, bearer, secFetchSite, body string
}

func serveDERControl(router http.Handler, method, target string, rr derControlRouteRequest) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(rr.body))
	req.Host = "127.0.0.1"
	req.RemoteAddr = rr.remote
	if rr.contentType != "" {
		req.Header.Set("Content-Type", rr.contentType)
	}
	if rr.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+rr.bearer)
	}
	if rr.secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", rr.secFetchSite)
		req.Header.Set("Origin", "http://attacker.example")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Criterion 7: both POST routes are in the body-type table, and each is
// refused for text/plain, for a cross-site request, and without a credential
// from a non-loopback address.
func TestDERControlPostRoutesAdminGates(t *testing.T) {
	for _, pattern := range []string{"POST /api/der/controls", "POST /api/der/controls/{mrid}/cancel"} {
		if got := adminplane.AdminBodyTypes()[pattern]; len(got) != 1 || got[0] != "application/json" {
			t.Errorf("AdminBodyTypes[%q] = %v, want [application/json]", pattern, got)
		}
	}

	router := newDERControlRouter(t)
	targets := []string{"/api/der/controls", "/api/der/controls/" + derControlTestMRID + "/cancel"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			w := serveDERControl(router, http.MethodPost, target, derControlRouteRequest{remote: "192.0.2.9:4000", contentType: "text/plain", bearer: "the-key", body: derControlCreateBody()})
			if w.Code != http.StatusUnsupportedMediaType {
				t.Errorf("text/plain: status = %d body = %s, want 415", w.Code, w.Body.String())
			}
			w = serveDERControl(router, http.MethodPost, target, derControlRouteRequest{remote: "192.0.2.9:4000", contentType: "application/json", bearer: "the-key", secFetchSite: "cross-site", body: derControlCreateBody()})
			if w.Code != http.StatusForbidden || w.Body.String() != crossOriginRefusalBody {
				t.Errorf("cross-site: status = %d body = %s, want 403 %s", w.Code, w.Body.String(), crossOriginRefusalBody)
			}
			w = serveDERControl(router, http.MethodPost, target, derControlRouteRequest{remote: "192.0.2.9:4000", contentType: "application/json", body: derControlCreateBody()})
			if w.Code != http.StatusUnauthorized || w.Body.String() != sensitiveRefusalBody {
				t.Errorf("no credential from non-loopback: status = %d body = %s, want 401 %s", w.Code, w.Body.String(), sensitiveRefusalBody)
			}
		})
	}
}

// Criterion 10: from loopback with no credential, both POST routes answer
// 401 while both GET routes are still admitted by the bypass. A Bearer from
// loopback still creates and cancels.
func TestDERControlRoutesLoopbackBypass(t *testing.T) {
	router := newDERControlRouter(t)
	const loopback = "127.0.0.1:4000"

	for _, target := range []string{"/api/der/controls", "/api/der/controls/" + derControlTestMRID + "/cancel"} {
		w := serveDERControl(router, http.MethodPost, target, derControlRouteRequest{remote: loopback, contentType: "application/json", body: derControlCreateBody()})
		if w.Code != http.StatusUnauthorized || w.Body.String() != sensitiveRefusalBody {
			t.Errorf("bypass-only POST %s: status = %d body = %s, want 401", target, w.Code, w.Body.String())
		}
	}
	list := serveDERControl(router, http.MethodGet, "/api/der/controls?device=0", derControlRouteRequest{remote: loopback})
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"controls":[]`) {
		t.Fatalf("bypass-only GET list after refused creates: status = %d body = %s, want 200 with no controls", list.Code, list.Body.String())
	}
	programs := serveDERControl(router, http.MethodGet, "/api/devices/0/der-programs", derControlRouteRequest{remote: loopback})
	if programs.Code != http.StatusOK || !strings.Contains(programs.Body.String(), `"href":"/edev/0/fsa/0/derp/0"`) {
		t.Fatalf("bypass-only GET der-programs: status = %d body = %s, want 200 listing the program", programs.Code, programs.Body.String())
	}

	logs := captureSlogForSensitiveRoutes(t)
	created := serveDERControl(router, http.MethodPost, "/api/der/controls", derControlRouteRequest{remote: loopback, contentType: "application/json", bearer: "the-key", body: derControlCreateBody()})
	if created.Code != http.StatusCreated {
		t.Fatalf("Bearer POST from loopback: status = %d body = %s, want 201", created.Code, created.Body.String())
	}
	if !strings.Contains(logs.String(), `"event":"der_control_created","remote_addr":"127.0.0.1:4000","admission":"bearer"`) {
		t.Errorf("the create's audit line does not name the Bearer admission:\n%s", logs.String())
	}
	var view struct {
		MRID string `json:"mRID"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.MRID == "" {
		t.Fatalf("create body %s: %v", created.Body.String(), err)
	}
	list = serveDERControl(router, http.MethodGet, "/api/der/controls?device=0", derControlRouteRequest{remote: loopback})
	if !strings.Contains(list.Body.String(), view.MRID) {
		t.Fatalf("list does not show the created control: %s", list.Body.String())
	}
	cancelled := serveDERControl(router, http.MethodPost, "/api/der/controls/"+view.MRID+"/cancel", derControlRouteRequest{remote: loopback, contentType: "application/json", bearer: "the-key"})
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("Bearer cancel from loopback: status = %d body = %s, want 200 cancelled", cancelled.Code, cancelled.Body.String())
	}
}

// The der-programs route is matched through a wildcard so that it does not
// conflict with by-lfdi; both must still resolve, and an unknown device
// sub-collection is a 404.
func TestDeviceCollectionRoutesResolve(t *testing.T) {
	router := newDERControlRouter(t)
	const loopback = "127.0.0.1:4000"
	byLFDI := serveDERControl(router, http.MethodGet, "/api/devices/by-lfdi/65DE1159BA8C8897D5A7F94997D22544EB90A2B7", derControlRouteRequest{remote: loopback})
	if byLFDI.Code != http.StatusOK {
		t.Errorf("by-lfdi: status = %d body = %s, want 200", byLFDI.Code, byLFDI.Body.String())
	}
	unknown := serveDERControl(router, http.MethodGet, "/api/devices/0/nope", derControlRouteRequest{remote: loopback})
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown collection: status = %d, want 404", unknown.Code)
	}
}
