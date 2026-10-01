package sep2adminplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const testKey = "plane-test-key-0123"

// controlWritePatterns are the five routes ControlWrites mounts.
var controlWritePatterns = []string{
	"POST /api/der/controls",
	"POST /api/der/controls/{mrid}/cancel",
	"POST /api/derms/flow-reservations/{edevId}/{frqId}/answer",
	"POST /api/derms/flow-reservations/{edevId}/{frqId}/revise",
	"POST /api/derms/flow-reservations/{edevId}/{frqId}/cancel",
}

// baseConfig mounts every admin route family: NewStores wires all of them
// but the DER control routes, which need the concrete DER control store.
func baseConfig() sep2adminplane.Config {
	stores := sep2server.NewStores()
	stores.DERControls = memory.NewDERControlStore()
	return sep2adminplane.Config{
		Stores:       stores,
		AdminKey:     testKey,
		AllowedHosts: []string{"localhost"},
	}
}

func newPlane(t *testing.T, cfg sep2adminplane.Config) *sep2adminplane.Plane {
	t.Helper()
	p, err := sep2adminplane.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// send serves one request from loopback with no forwarded header, the
// request the standalone server's bypass admits; withKey adds the Bearer.
func send(p *sep2adminplane.Plane, method, path, body string, withKey bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "localhost"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withKey {
		req.Header.Set("Authorization", "Bearer "+testKey)
	}
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	return rec
}

func TestBypassOffRefusesLoopbackWithoutCredential(t *testing.T) {
	rec := send(newPlane(t, baseConfig()), http.MethodPost, "/api/fsas", `{"description":"x"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bypass off: loopback POST /api/fsas with no credential = %d, want 401; body %s", rec.Code, rec.Body)
	}

	// The control: the same request with the bypass on is admitted, so the
	// 401 above comes from the switch and not from the route.
	on := baseConfig()
	on.LoopbackBypass = true
	if rec := send(newPlane(t, on), http.MethodPost, "/api/fsas", `{"description":"x"}`, false); rec.Code == http.StatusUnauthorized {
		t.Fatalf("bypass on: loopback POST /api/fsas = 401, want it admitted; body %s", rec.Body)
	}
}

func TestNewRefusesUnsafeConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sep2adminplane.Config)
		want   error
	}{
		{"empty key", func(c *sep2adminplane.Config) { c.AdminKey = "" }, sep2adminplane.ErrNoCredential},
		{"whitespace key", func(c *sep2adminplane.Config) { c.AdminKey = " \t" }, sep2adminplane.ErrNoCredential},
		{"15-character key", func(c *sep2adminplane.Config) { c.AdminKey = "fifteen-chars-x" }, sep2adminplane.ErrShortCredential},
		{"blank host among real ones", func(c *sep2adminplane.Config) { c.AllowedHosts = []string{"localhost", ""} }, sep2adminplane.ErrBlankAllowedHost},
		{"whitespace host among real ones", func(c *sep2adminplane.Config) { c.AllowedHosts = []string{" ", "localhost"} }, sep2adminplane.ErrBlankAllowedHost},
		{"unknown edition", func(c *sep2adminplane.Config) { c.Edition = "2030" }, sep2adminplane.ErrUnknownEdition},
		{"edition 2023 over 2018 stores", func(c *sep2adminplane.Config) { c.Edition = "2023" }, sep2adminplane.ErrEditionMismatch},
		{"edition 2018 over 2023 stores", func(c *sep2adminplane.Config) { c.Stores.Edition2023 = true }, sep2adminplane.ErrEditionMismatch},
		{"nil hosts", func(c *sep2adminplane.Config) { c.AllowedHosts = nil }, sep2adminplane.ErrNoAllowedHosts},
		{"empty hosts", func(c *sep2adminplane.Config) { c.AllowedHosts = []string{} }, sep2adminplane.ErrNoAllowedHosts},
		{"blank hosts", func(c *sep2adminplane.Config) { c.AllowedHosts = []string{"", "  "} }, sep2adminplane.ErrBlankAllowedHost},
		{"nil stores", func(c *sep2adminplane.Config) { c.Stores = nil }, sep2adminplane.ErrNoStores},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			p, err := sep2adminplane.New(cfg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("New error = %v, want %v", err, tc.want)
			}
			if p != nil {
				t.Fatalf("New returned a plane alongside its error")
			}
		})
	}

	// The control: a key of exactly MinAdminKeyLength characters, multi-byte
	// ones included, is accepted.
	exact := baseConfig()
	exact.AdminKey = strings.Repeat("\u00e9", sep2adminplane.MinAdminKeyLength)
	newPlane(t, exact)
}

func TestNewRefusesInconsistentSettings(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sep2adminplane.Config)
	}{
		{"deadline below 1s", func(c *sep2adminplane.Config) { c.FlowReservationDeadline = time.Millisecond }},
		{"grace below 15m", func(c *sep2adminplane.Config) { c.RetentionGrace = time.Minute }},
		{"FSAs not the concrete store", func(c *sep2adminplane.Config) {
			c.Stores.FSAs = memory.WithDependents(c.Stores.FSAs, memory.NewScopedStore[dercontrol.LifecycleRecord]())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			if _, err := sep2adminplane.New(cfg); err == nil {
				t.Fatal("New accepted the config")
			}
		})
	}

	ok := baseConfig()
	ok.Edition = "2023"
	ok.Stores.Edition2023 = true
	newPlane(t, ok)
}

func TestHostGateIsOn(t *testing.T) {
	p := newPlane(t, baseConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/fsas", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "evil.example"
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("Host evil.example = %d, want 421", rec.Code)
	}
	if rec := send(p, http.MethodGet, "/api/fsas", "", true); rec.Code != http.StatusOK {
		t.Fatalf("Host localhost = %d, want 200; body %s", rec.Code, rec.Body)
	}
}

func TestControlWritesOffLeavesWriteRoutesUnmounted(t *testing.T) {
	off := newPlane(t, baseConfig())
	for _, w := range controlWritePatterns {
		if slices.Contains(off.Patterns(), w) {
			t.Errorf("ControlWrites false: %s is mounted", w)
		}
	}
	for _, r := range []string{"GET /api/der/controls", "GET /api/derms/flow-reservations", "GET /api/derms/grants"} {
		if !slices.Contains(off.Patterns(), r) {
			t.Errorf("ControlWrites false: read %s is not mounted", r)
		}
	}
	if rec := send(off, http.MethodPost, "/api/der/controls", `{}`, true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("ControlWrites false: credentialed POST /api/der/controls = %d, want 405", rec.Code)
	}

	// The control: with ControlWrites on, the same stores mount all five.
	on := baseConfig()
	on.ControlWrites = true
	patterns := newPlane(t, on).Patterns()
	for _, w := range controlWritePatterns {
		if !slices.Contains(patterns, w) {
			t.Errorf("ControlWrites true: %s is not mounted", w)
		}
	}
}

// TestGuardTablesCannotBeEditedFromOutside edits every table the admin
// plane exports, in the way that would open a guarded route, and checks a
// plane built afterwards still guards it. The bypass is on, so the guard
// tables are the only thing between a loopback caller and these routes.
func TestGuardTablesCannotBeEditedFromOutside(t *testing.T) {
	sensitive := adminplane.SensitiveAdminPatterns()
	clear(sensitive)
	nonSensitive := adminplane.NonSensitiveAdminWrites()
	nonSensitive["POST /api/management-pairs"] = struct{}{}
	bodyTypes := adminplane.AdminBodyTypes()
	bodyTypes["POST /api/management-pairs"][0] = "text/plain"

	cfg := baseConfig()
	cfg.LoopbackBypass = true
	p := newPlane(t, cfg)

	if rec := send(p, http.MethodGet, "/api/certs/device-types", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("loopback GET /api/certs/device-types with no credential = %d, want 401", rec.Code)
	}
	if rec := send(p, http.MethodPost, "/api/management-pairs", `{}`, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("loopback POST /api/management-pairs with no credential = %d, want 401", rec.Code)
	}
	if _, ok := adminplane.SensitiveAdminPatterns()["GET /api/certs/device-types"]; !ok {
		t.Error("clearing a returned copy emptied the live sensitive table")
	}
	if got := adminplane.AdminBodyTypes()["POST /api/management-pairs"]; !slices.Equal(got, []string{"application/json"}) {
		t.Errorf("editing a returned copy changed the live body types to %v", got)
	}
}

func TestPanelsAreServed(t *testing.T) {
	cfg := baseConfig()
	cfg.Panels = []sep2admin.Panel{{
		ID:                "ext-status",
		Label:             "Status",
		Placement:         sep2admin.ExtensionSlot(1),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		View: func(context.Context) (sep2admin.Descriptor, error) {
			return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion, Sections: []sep2admin.Section{{Heading: "Feeder", Empty: "none", Body: sep2admin.NewTableBody(sep2admin.TableBody{Columns: []string{"name"}})}}}, nil
		},
	}}
	p := newPlane(t, cfg)

	rec := send(p, http.MethodGet, "/api/ui/panels", "", true)
	var list []struct{ ID, Label string }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ui/panels = %d %s (%v)", rec.Code, rec.Body, err)
	}
	if len(list) != 1 || list[0].ID != "ext-status" || list[0].Label != "Status" {
		t.Fatalf("panel list = %+v, want one ext-status/Status", list)
	}
	if rec := send(p, http.MethodGet, "/api/ui/panels/ext-status", "", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"heading":"Feeder"`) {
		t.Fatalf("GET /api/ui/panels/ext-status = %d %s", rec.Code, rec.Body)
	}

	bad := baseConfig()
	bad.Panels = []sep2admin.Panel{{ID: "fsas", Label: "x", Placement: sep2admin.ExtensionSlot(1), DescriptorVersion: sep2admin.CurrentDescriptorVersion, View: cfg.Panels[0].View}}
	if _, err := sep2adminplane.New(bad); !errors.Is(err, sep2admin.ErrInvalidID) {
		t.Fatalf("New with a reserved panel ID: error = %v, want ErrInvalidID", err)
	}
}

func TestPatternsIsACopy(t *testing.T) {
	p := newPlane(t, baseConfig())
	first := p.Patterns()
	if len(first) == 0 {
		t.Fatal("no patterns")
	}
	want := first[0]
	first[0] = "edited"
	if got := p.Patterns()[0]; got != want {
		t.Fatalf("Patterns()[0] = %q after editing a returned slice, want %q", got, want)
	}
}
