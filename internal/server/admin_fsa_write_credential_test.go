package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #930: the FSA writes change what devices are told, so a loopback request
// with no credential is refused, per route, and leaves the stores untouched.

const (
	fsaProgramHref  = "/edev/dev-X/fsa/anyfsa/derp/p1"
	fsaProgram2Href = "/edev/dev-X/fsa/anyfsa/derp/p2"
)

func fsaWriteStores(t *testing.T) *server.Stores {
	t.Helper()
	stores := &server.Stores{
		EndDevices:  memory.NewEndDeviceStore(),
		FSAs:        memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms: memory.NewDERProgramStore(),
		AdminFSAs:   memory.NewAdminFSAStore(),
	}
	ctx := context.Background()
	for id, href := range map[string]string{"p1": fsaProgramHref, "p2": fsaProgram2Href} {
		prog := sep2.DERProgram{MRID: id}
		prog.Href = href
		if err := stores.DERPrograms.Create(ctx, "dev-X", id, prog); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	if err := stores.EndDevices.Create(ctx, "dev-Y", sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-Y"}},
		SFDI:                 "343434343434",
		LFDI:                 "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Enabled:              &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stores.AdminFSAs.Create(ctx, "fsa-solar", sep2.FunctionSetAssignments{MRID: "fsa-solar", Description: "Solar template"}); err != nil {
		t.Fatal(err)
	}
	if err := stores.AdminFSAs.AttachProgram(ctx, "fsa-solar", fsaProgramHref); err != nil {
		t.Fatal(err)
	}
	if err := stores.AdminFSAs.AssignDevice(ctx, "fsa-solar", "dev-X"); err != nil {
		t.Fatal(err)
	}
	return stores
}

func fsaWriteRouter(stores *server.Stores) http.Handler {
	router, _ := adminplane.BuildAdminRouter(
		"the-key", nil, stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), http.NotFoundHandler(),
	)
	return router
}

func fsaLoopbackRequest(method, target, body string, bearer bool) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:54321"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer the-key")
	}
	return req
}

func TestFSAWritesRefuseLoopbackWithoutCredential(t *testing.T) {
	cases := []struct {
		pattern, method, target, body string
		wantWithBearer                int
	}{
		{"POST /api/fsas", http.MethodPost, "/api/fsas", `{"description":"New","mRID":"fsa-new"}`, http.StatusCreated},
		{"DELETE /api/fsas/{id}", http.MethodDelete, "/api/fsas/fsa-solar", "", http.StatusConflict},
		{"POST /api/fsas/{id}/programs", http.MethodPost, "/api/fsas/fsa-solar/programs", `{"programHref":"` + fsaProgram2Href + `"}`, http.StatusCreated},
		{"DELETE /api/fsas/{id}/programs", http.MethodDelete, "/api/fsas/fsa-solar/programs?href=%2Fedev%2Fdev-X%2Ffsa%2Fanyfsa%2Fderp%2Fp1", "", http.StatusNoContent},
		{"POST /api/devices/{id}/fsa-assignment", http.MethodPost, "/api/devices/dev-Y/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-solar"}`, http.StatusOK},
		{"DELETE /api/devices/{id}/fsa-assignment", http.MethodDelete, "/api/devices/dev-X/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-solar", "", http.StatusNoContent},
	}

	// Control: the table must cover every FSA write the router mounts, or a
	// route added later is exempt without failing here.
	patterns := adminplane.AuthedAdminPatterns(
		"the-key", nil, fsaWriteStores(t), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		http.NotFoundHandler(),
	)
	var mounted []string
	for _, p := range patterns {
		method, path, _ := strings.Cut(p, " ")
		if method != http.MethodGet && (strings.HasPrefix(path, "/api/fsas") || strings.HasSuffix(path, "/fsa-assignment")) {
			mounted = append(mounted, p)
		}
	}
	if len(mounted) != len(cases) {
		t.Fatalf("router mounts %d FSA write routes %v, test covers %d", len(mounted), mounted, len(cases))
	}
	for _, tc := range cases {
		if !slices.Contains(mounted, tc.pattern) {
			t.Fatalf("test case %q is not a mounted route; mounted = %v", tc.pattern, mounted)
		}
		if _, exempt := adminplane.NonSensitiveAdminWrites()[tc.pattern]; exempt {
			t.Errorf("%s is on nonSensitiveAdminWrites", tc.pattern)
		}
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			stores := fsaWriteStores(t)
			router := fsaWriteRouter(stores)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, fsaLoopbackRequest(tc.method, tc.target, tc.body, false))
			if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
				t.Errorf("%s with no credential: status = %d body = %q, want 401 %q", tc.pattern, rec.Code, rec.Body.String(), sensitiveRefusalBody)
			}

			if _, err := stores.AdminFSAs.Get(ctx, "fsa-new"); err == nil {
				t.Errorf("%s: refused request still created fsa-new", tc.pattern)
			}
			if got, err := stores.AdminFSAs.Get(ctx, "fsa-solar"); err != nil || got.Description != "Solar template" {
				t.Errorf("%s: fsa-solar = %+v, err = %v, want it untouched", tc.pattern, got, err)
			}
			if got := stores.AdminFSAs.Programs(ctx, "fsa-solar"); !slices.Equal(got, []string{fsaProgramHref}) {
				t.Errorf("%s: programs = %v, want [%s]", tc.pattern, got, fsaProgramHref)
			}
			if got := stores.AdminFSAs.Devices(ctx, "fsa-solar"); !slices.Equal(got, []string{"dev-X"}) {
				t.Errorf("%s: devices = %v, want [dev-X]", tc.pattern, got)
			}

			// Control: the same request with a real credential reaches the
			// handler, and the two POSTs that seeded a target land a write.
			ctl := httptest.NewRecorder()
			router.ServeHTTP(ctl, fsaLoopbackRequest(tc.method, tc.target, tc.body, true))
			if ctl.Code != tc.wantWithBearer {
				t.Errorf("%s with a valid Bearer: status = %d, want %d; body = %q", tc.pattern, ctl.Code, tc.wantWithBearer, ctl.Body.String())
			}
			switch tc.pattern {
			case "POST /api/fsas/{id}/programs":
				if got := stores.AdminFSAs.Programs(ctx, "fsa-solar"); !slices.Contains(got, fsaProgram2Href) {
					t.Errorf("%s with a valid Bearer: programs = %v, want %s attached", tc.pattern, got, fsaProgram2Href)
				}
			case "POST /api/devices/{id}/fsa-assignment":
				if got := stores.AdminFSAs.Devices(ctx, "fsa-solar"); !slices.Contains(got, "dev-Y") {
					t.Errorf("%s with a valid Bearer: devices = %v, want dev-Y assigned", tc.pattern, got)
				}
			}
		})
	}
}

// The credentialed create stores the submitted fields.
func TestFSACreateWithCredentialStoresFields(t *testing.T) {
	stores := fsaWriteStores(t)
	rec := httptest.NewRecorder()
	fsaWriteRouter(stores).ServeHTTP(rec, fsaLoopbackRequest(http.MethodPost, "/api/fsas", `{"description":"New","mRID":"fsa-new"}`, true))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d body = %q", rec.Code, rec.Body.String())
	}
	got, err := stores.AdminFSAs.Get(context.Background(), "fsa-new")
	if err != nil || got.MRID != "fsa-new" || got.Description != "New" {
		t.Fatalf("stored FSA = %+v, err = %v, want mRID fsa-new description New", got, err)
	}
}
