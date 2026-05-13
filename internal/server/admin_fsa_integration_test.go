package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-096 integration tests. Exercise the admin router end-to-end with the
// real stores wired up, including the topology shape after a create-attach-
// assign sequence.

const adminKey = "test-admin-key"

func setupAdminRouter(t *testing.T) (*server.Stores, http.Handler) {
	t.Helper()
	stores := &server.Stores{
		EndDevices:  memory.NewEndDeviceStore(),
		FSAs:        memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms: memory.NewDERProgramStore(),
		AdminFSAs:   memory.NewAdminFSAStore(),
	}
	tickets := auth.NewTicketStore(5 * time.Minute)
	router := server.NewAdminRouter(adminKey, nil, stores, "GCM", tickets)
	return stores, router
}

func authedDo(t *testing.T, h http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAdminFSAIntegration_FullLifecycle(t *testing.T) {
	t.Parallel()
	stores, router := setupAdminRouter(t)

	// Seed a device and a DERProgram so attach + assign have targets.
	ctx := context.Background()
	enabled := true
	if err := stores.EndDevices.Create(ctx, "dev-X", sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev-X"}},
		SFDI:                 "121212121212",
		LFDI:                 "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Enabled:              &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stores.DERPrograms.Create(ctx, "dev-X", "p1", sep2.DERProgram{
		MRID: "prog-1",
	}); err != nil {
		t.Fatal(err)
	}

	// 1) Create an admin FSA.
	w := authedDo(t, router, http.MethodPost, "/api/fsas",
		`{"description":"Solar template","primacy":1,"mRID":"fsa-solar"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}

	// 2) Attach a DERProgram. Use the spec-shape href so the validator
	//    accepts it.
	w = authedDo(t, router, http.MethodPost, "/api/fsas/fsa-solar/programs",
		`{"programHref":"/edev/dev-X/fsa/anyfsa/derp/p1"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("attach: %d body=%s", w.Code, w.Body.String())
	}

	// 3) Attach to a non-existent program → 404.
	w = authedDo(t, router, http.MethodPost, "/api/fsas/fsa-solar/programs",
		`{"programHref":"/edev/dev-X/fsa/anyfsa/derp/ghost"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("attach-missing: expected 404, got %d", w.Code)
	}

	// 4) Assign the FSA to dev-X.
	w = authedDo(t, router, http.MethodPost, "/api/devices/dev-X/fsa-assignment",
		`{"fsaHref":"/api/fsas/fsa-solar"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("assign: %d body=%s", w.Code, w.Body.String())
	}

	// Confirm materialization into the device-scoped FSA store.
	if got, err := stores.FSAs.Get(ctx, "dev-X", "fsa-solar"); err != nil {
		t.Fatalf("scoped FSA missing: %v", err)
	} else if got.Description != "Solar template" {
		t.Errorf("scoped FSA description = %q, want %q", got.Description, "Solar template")
	}

	// 5) Topology should show the SY -> FD -> SP -> DEV tree with fsa-solar
	//    on dev-X and no unassigned templates.
	w = authedDo(t, router, http.MethodGet, "/api/topology", "")
	if w.Code != http.StatusOK {
		t.Fatalf("topology: %d body=%s", w.Code, w.Body.String())
	}
	var root handler.TopologyNode
	if err := json.Unmarshal(w.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if root.Kind != "SY" || len(root.Children) != 1 {
		t.Fatalf("topology root malformed: %+v", root)
	}
	fd := root.Children[0]
	if fd.Kind != "FD" || len(fd.Children) != 1 {
		t.Fatalf("FD malformed: %+v", fd)
	}
	sp := fd.Children[0]
	if sp.Kind != "SP" || len(sp.Children) != 1 {
		t.Fatalf("SP malformed: %+v", sp)
	}
	dev := sp.Children[0]
	if dev.Kind != "DEV" || dev.ID != "dev-X" {
		t.Errorf("DEV id mismatch: %+v", dev)
	}
	if len(dev.FSAs) != 1 || dev.FSAs[0].MRID != "fsa-solar" {
		t.Errorf("DEV.FSAs mismatch: %+v", dev.FSAs)
	}
	if len(root.FSAs) != 0 {
		t.Errorf("unassigned templates should be empty, got %+v", root.FSAs)
	}

	// 6) Deletion is blocked while assignments / programs remain.
	w = authedDo(t, router, http.MethodDelete, "/api/fsas/fsa-solar", "")
	if w.Code != http.StatusConflict {
		t.Errorf("delete-in-use: expected 409, got %d body=%s", w.Code, w.Body.String())
	}

	// 7) Detach program + unassign device, then delete succeeds.
	w = authedDo(t, router, http.MethodDelete,
		"/api/fsas/fsa-solar/programs?href=%2Fedev%2Fdev-X%2Ffsa%2Fanyfsa%2Fderp%2Fp1", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("detach: expected 204, got %d", w.Code)
	}
	w = authedDo(t, router, http.MethodDelete,
		"/api/devices/dev-X/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-solar", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("unassign: expected 204, got %d", w.Code)
	}
	w = authedDo(t, router, http.MethodDelete, "/api/fsas/fsa-solar", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}

	// Topology now has zero devices' FSAs and zero templates.
	w = authedDo(t, router, http.MethodGet, "/api/topology", "")
	if w.Code != http.StatusOK {
		t.Fatalf("topology-empty: %d", w.Code)
	}
	var root2 handler.TopologyNode
	_ = json.Unmarshal(w.Body.Bytes(), &root2)
	if len(root2.FSAs) != 0 {
		t.Errorf("post-cleanup unassigned not empty: %+v", root2.FSAs)
	}
}

func TestAdminFSAIntegration_RoutesRequireAuth(t *testing.T) {
	t.Parallel()
	_, router := setupAdminRouter(t)

	// Without auth header, /api/fsas should be rejected by AdminAuthMiddleware.
	req := httptest.NewRequest(http.MethodGet, "/api/fsas", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 for unauthenticated request, got %d", w.Code)
	}
}

func TestAdminFSAIntegration_ListAfterCreate(t *testing.T) {
	t.Parallel()
	_, router := setupAdminRouter(t)

	for _, mrid := range []string{"fsa-a", "fsa-b", "fsa-c"} {
		w := authedDo(t, router, http.MethodPost, "/api/fsas",
			`{"description":"x","mRID":"`+mrid+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", mrid, w.Code)
		}
	}

	w := authedDo(t, router, http.MethodGet, "/api/fsas", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var out struct {
		FSAs []map[string]any `json:"fsas"`
	}
	_ = json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&out)
	if len(out.FSAs) != 3 {
		t.Errorf("expected 3 FSAs, got %d", len(out.FSAs))
	}
}

// Smoke for the href validator: malformed program hrefs reject; well-formed
// but unknown still rejects.
func TestProgramHrefValidator_ShapeCases(t *testing.T) {
	t.Parallel()
	stores, router := setupAdminRouter(t)
	_ = stores.DERPrograms.Create(context.Background(), "dev-Y", "p-real", sep2.DERProgram{})

	// Seed an FSA so attach can reach the validator.
	wc := authedDo(t, router, http.MethodPost, "/api/fsas",
		`{"description":"x","mRID":"fsa-v"}`)
	if wc.Code != http.StatusCreated {
		t.Fatalf("seed: %d", wc.Code)
	}

	cases := []struct {
		name   string
		href   string
		expect int
	}{
		{"valid", "/edev/dev-Y/fsa/anyfsa/derp/p-real", http.StatusCreated},
		{"wrong-prefix", "/wrong/dev-Y/fsa/anyfsa/derp/p-real", http.StatusNotFound},
		{"missing-segment", "/edev/dev-Y/derp/p-real", http.StatusNotFound},
		{"unknown-program", "/edev/dev-Y/fsa/anyfsa/derp/ghost", http.StatusNotFound},
		{"empty-href", "", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"programHref":"` + c.href + `"}`
			w := authedDo(t, router, http.MethodPost, "/api/fsas/fsa-v/programs", body)
			if w.Code != c.expect {
				t.Errorf("%s: expected %d, got %d body=%s", c.name, c.expect, w.Code, w.Body.String())
			}
			// Detach to keep test idempotent for the next valid-case attempt.
			if w.Code == http.StatusCreated {
				_ = authedDo(t, router, http.MethodDelete,
					"/api/fsas/fsa-v/programs?href="+strings.ReplaceAll(c.href, "/", "%2F"), "")
			}
		})
	}
}
