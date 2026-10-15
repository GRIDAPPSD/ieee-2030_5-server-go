package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #920: the template create makes a program on every assigned device, so it
// needs a real credential even from loopback, and ReadOnly leaves it out.

const (
	templateCreatePattern = "POST /api/fsas/{id}/der-programs"
	templateListPattern   = "GET /api/fsas/{id}/der-programs"
	templateCreateBody    = `{"description":"d","primacy":1,"defaultControl":{"type":"connect"}}`
)

func templateRouteConfig(t *testing.T) adminplane.Config {
	t.Helper()
	stores := newTestStores()
	if err := stores.AdminFSAs.Create(context.Background(), "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	return adminplane.Config{
		AdminKey:       "the-key",
		CertService:    newScopeTestCertService(t),
		Stores:         stores,
		TLSMode:        "GCM",
		Tickets:        auth.NewTicketStore(30 * time.Second),
		Sessions:       auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		AllowedHosts:   adminplane.DefaultAdminAllowedHosts(),
		LoopbackBypass: true,
		ControlWrites:  true,
	}
}

func TestProgramTemplateCreateNeedsARealCredential(t *testing.T) {
	router, _, err := adminplane.Build(templateRouteConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	const loopback = "127.0.0.1:4000"
	w := serveDERControl(router, http.MethodPost, "/api/fsas/fsa-A/der-programs", derControlRouteRequest{remote: loopback, contentType: "application/json", body: templateCreateBody})
	if w.Code != http.StatusUnauthorized || w.Body.String() != sensitiveRefusalBody {
		t.Fatalf("bypass-only POST: status = %d body = %s, want 401 %s", w.Code, w.Body.String(), sensitiveRefusalBody)
	}
	if w := serveDERControl(router, http.MethodGet, "/api/fsas/fsa-A/der-programs", derControlRouteRequest{remote: loopback}); w.Code != http.StatusOK || w.Body.String() != "{\"templates\":[]}\n" {
		t.Fatalf("bypass-only GET after the refused create: status = %d body = %q, want 200 and no templates", w.Code, w.Body.String())
	}
	if w := serveDERControl(router, http.MethodPost, "/api/fsas/fsa-A/der-programs", derControlRouteRequest{remote: loopback, contentType: "text/plain", bearer: "the-key", body: templateCreateBody}); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain with Bearer: status = %d, want 415", w.Code)
	}
	if w := serveDERControl(router, http.MethodPost, "/api/fsas/fsa-A/der-programs", derControlRouteRequest{remote: loopback, contentType: "application/json", bearer: "the-key", body: templateCreateBody}); w.Code != http.StatusCreated {
		t.Fatalf("Bearer POST: status = %d body = %s, want 201", w.Code, w.Body.String())
	}
}

func TestProgramTemplateRoutesUnderReadOnly(t *testing.T) {
	cfg := templateRouteConfig(t)
	_, open, err := adminplane.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(open, templateCreatePattern) || !slices.Contains(open, templateListPattern) {
		t.Fatalf("control: without ReadOnly the routes are %q, want both template routes", open)
	}

	cfg.ControlWrites = false
	cfg.ReadOnly = true
	router, patterns, err := adminplane.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(patterns, templateCreatePattern) {
		t.Errorf("ReadOnly mounts %s", templateCreatePattern)
	}
	if !slices.Contains(patterns, templateListPattern) {
		t.Errorf("ReadOnly does not mount %s", templateListPattern)
	}
	w := serveDERControl(router, http.MethodPost, "/api/fsas/fsa-A/der-programs", derControlRouteRequest{remote: "127.0.0.1:4000", contentType: "application/json", bearer: "the-key", body: templateCreateBody})
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("ReadOnly POST with Bearer: status = %d, want 404 or 405", w.Code)
	}
}

type templateNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (n *templateNotifier) Notify(_ context.Context, href string, status uint8) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, href+" "+string(rune('0'+status)))
}

func (n *templateNotifier) take() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.sent
	n.sent = nil
	return out
}

// The admin plane wires the server's notifier and PEN into the template
// handler: the notifications and minted mRIDs below come through Build, not
// through a hand-built handler.
func TestProgramTemplateWiringNotifiesAndMintsUnderThePEN(t *testing.T) {
	cfg := templateRouteConfig(t)
	n := &templateNotifier{}
	pen := uint32(0x0000B1C2)
	cfg.Stores.AdminNotifier = n
	cfg.Stores.PEN = &pen
	if err := cfg.Stores.EndDevices.Create(context.Background(), "0", sep2.EndDevice{SFDI: "1", LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"}); err != nil {
		t.Fatal(err)
	}
	router, _, err := adminplane.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, target, body string) []byte {
		t.Helper()
		w := serveDERControl(router, method, target, derControlRouteRequest{remote: "127.0.0.1:4000", contentType: "application/json", bearer: "the-key", body: body})
		if w.Code/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, target, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}

	send(http.MethodPost, "/api/devices/0/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if got, want := n.take(), []string{"/edev/0/fsa 0", "/edev/0/fsa/fsa-A/derp 0"}; !slices.Equal(got, want) {
		t.Errorf("assign notifications = %q, want %q", got, want)
	}
	var v struct {
		MRID           string `json:"mRID"`
		DefaultControl struct {
			MRID string `json:"mRID"`
		} `json:"defaultControl"`
	}
	if err := json.Unmarshal(send(http.MethodPost, "/api/fsas/fsa-A/der-programs", templateCreateBody), &v); err != nil {
		t.Fatal(err)
	}
	underPEN := regexp.MustCompile(`^[0-9A-F]{24}0000B1C2$`)
	if !underPEN.MatchString(v.MRID) || !underPEN.MatchString(v.DefaultControl.MRID) {
		t.Errorf("mRIDs %q and %q, want both to end in the PEN 0000B1C2", v.MRID, v.DefaultControl.MRID)
	}
	if got, want := n.take(), []string{"/edev/0/fsa/fsa-A/derp 0"}; !slices.Equal(got, want) {
		t.Errorf("create notifications = %q, want %q", got, want)
	}
}
