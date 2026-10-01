package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

const panelTestKey = "panel-test-key"

func testPanel(id string, rank int, view sep2admin.ViewFunc) sep2admin.Panel {
	return sep2admin.Panel{
		ID:                id,
		Label:             "Label " + id,
		Placement:         sep2admin.ExtensionSlot(rank),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		View:              view,
	}
}

func okView(_ context.Context) (sep2admin.Descriptor, error) {
	return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
}

func buildWithPanels(t *testing.T, panels ...sep2admin.Panel) http.Handler {
	t.Helper()
	h, _, err := Build(Config{AdminKey: panelTestKey, Panels: panels, LoopbackBypass: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

// get sends a GET from loopback with no forwarded header, the request the
// loopback bypass admits; withKey adds the Bearer credential.
func get(h http.Handler, path string, withKey bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	if withKey {
		req.Header.Set("Authorization", "Bearer "+panelTestKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPanelListWithNoPanelsIsAnEmptyArray(t *testing.T) {
	rec := get(buildWithPanels(t), "/api/ui/panels", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "[]" {
		t.Fatalf("body = %q, want the bytes []", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestPanelRoutesRefuseLoopbackWithoutCredential(t *testing.T) {
	h := buildWithPanels(t, testPanel("gridappsd-registry", 1, okView))

	// Control: the bypass is on for this router, so a non-sensitive read
	// from loopback is admitted without a credential.
	if rec := get(h, "/ui/", false); rec.Code != http.StatusOK {
		t.Fatalf("control: loopback GET /ui/ = %d, want 200 (bypass admits)", rec.Code)
	}
	for _, path := range []string{"/api/ui/panels", "/api/ui/panels/gridappsd-registry", "/api/ui/panels/no-such-panel"} {
		if rec := get(h, path, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("loopback GET %s with no credential = %d, want 401", path, rec.Code)
		}
	}
	if rec := get(h, "/api/ui/panels/gridappsd-registry", true); rec.Code != http.StatusOK {
		t.Errorf("with the credential = %d, want 200; body %s", rec.Code, rec.Body)
	}
}

func TestHungPanelViewAnswers504(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := testPanel("hung", 1, func(context.Context) (sep2admin.Descriptor, error) {
		<-release // ignores ctx on purpose: the bound must not need the View's help
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
	})
	ps, err := newPanelSet([]sep2admin.Panel{hung})
	if err != nil {
		t.Fatalf("newPanelSet: %v", err)
	}
	ps.timeout = 50 * time.Millisecond
	cfg := runConfig(panelTestKey, nil, nil, "GCM", nil, nil, false, nil)
	authed, withMW := buildAuthedAdminMux(cfg, ps)
	h, _ := buildOuterAdminRouter(cfg, authed, withMW)

	start := time.Now()
	rec := get(h, "/api/ui/panels/hung", true)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body %s", rec.Code, rec.Body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("answered after %v, want close to the 50ms bound", elapsed)
	}
}

func TestPanelGetServesTheEncodedDescriptor(t *testing.T) {
	view := func(context.Context) (sep2admin.Descriptor, error) {
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion, Sections: []sep2admin.Section{{
			Heading: "Registry",
			Body: sep2admin.NewTableBody(sep2admin.TableBody{
				Columns: []string{"State"},
				Rows:    []sep2admin.Row{{sep2admin.BadgeCell(sep2admin.BadgeOK, "accepted")}},
			}),
		}}}, nil
	}
	rec := get(buildWithPanels(t, testPanel("p", 1, view)), "/api/ui/panels/p", true)
	want := `{"version":2,"sections":[{"kind":"table","heading":"Registry","prose":[],"empty":"","body":{"columns":["State"],"rows":[[{"kind":"badge","text":"accepted","badge":"ok"}]]}}]}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s\nwant 200 %s", rec.Code, rec.Body, want)
	}
}

func TestPanelGetRefusals(t *testing.T) {
	unsafe := func(context.Context) (sep2admin.Descriptor, error) {
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion, Sections: []sep2admin.Section{{
			Body: sep2admin.NewTableBody(sep2admin.TableBody{Columns: []string{"l"}, Rows: []sep2admin.Row{{sep2admin.LinkCell("javascript:x", "x")}}}),
		}}}, nil
	}
	panics := func(context.Context) (sep2admin.Descriptor, error) { panic("boom at /home/secret/path") }
	oldVersion := func(context.Context) (sep2admin.Descriptor, error) { return sep2admin.Descriptor{Version: 1}, nil }
	h := buildWithPanels(t, testPanel("unsafe", 1, unsafe), testPanel("panics", 1, panics), testPanel("old", 1, oldVersion))

	cases := []struct {
		path string
		want int
	}{
		{"/api/ui/panels/unsafe", http.StatusInternalServerError},
		{"/api/ui/panels/panics", http.StatusInternalServerError},
		{"/api/ui/panels/old", http.StatusInternalServerError},
		{"/api/ui/panels/missing", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := get(h, tc.path, true)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.path, rec.Code, tc.want)
		}
		body := rec.Body.String()
		if strings.Contains(body, "javascript") || strings.Contains(body, "/home/") || strings.Contains(body, "goroutine") {
			t.Errorf("%s body leaks View detail: %s", tc.path, body)
		}
	}
}

func TestPanelsCannotOverrideOrReorderCoreTabs(t *testing.T) {
	for _, id := range []string{"derms", "overview", "fsas"} {
		_, _, err := Build(Config{AdminKey: panelTestKey, Panels: []sep2admin.Panel{testPanel(id, 1, okView)}})
		if !errors.Is(err, sep2admin.ErrInvalidID) {
			t.Errorf("Build with a panel named %q: err = %v, want ErrInvalidID", id, err)
		}
	}

	// Registered b then a, at a rank below any core tab's: the list is a,
	// b, and no core slug appears in it.
	h := buildWithPanels(t, testPanel("b", -100, okView), testPanel("a", -100, okView))
	rec := get(h, "/api/ui/panels", true)
	var got []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || got[0].Label != "Label a" {
		t.Fatalf("panels = %+v, want a then b with their labels and nothing else", got)
	}
}
