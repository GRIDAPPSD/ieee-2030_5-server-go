package adminplane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

const actionPath = "/api/ui/panels/bus/actions/send"

const goodActionBody = `{"device":"dev-1","multiplier":3,"on":true,"raw":"{\"a\":1}"}`

// actionRecorder is what a test's Run saw.
type actionRecorder struct {
	calls atomic.Int32
	mu    sync.Mutex
	last  sep2admin.ActionValues
}

func (a *actionRecorder) run(res sep2admin.ActionResult, err error) func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
	return func(_ context.Context, v sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		a.calls.Add(1)
		a.mu.Lock()
		a.last = v
		a.mu.Unlock()
		return res, err
	}
}

func actionPanel(run func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error)) sep2admin.Panel {
	p := testPanel("bus", 1, okView)
	p.Actions = []sep2admin.Action{{
		ID:    "send",
		Label: "Send",
		Fields: []sep2admin.ActionField{
			{Name: "device", Label: "Device", Kind: sep2admin.ActionChoice, Choices: func(context.Context) ([]sep2admin.Choice, error) {
				return []sep2admin.Choice{{ID: "dev-1", Label: "Inverter 1"}, {ID: "dev-2", Label: "Inverter 2"}}, nil
			}},
			{Name: "multiplier", Label: "Multiplier", Kind: sep2admin.ActionInteger, Min: -9, Max: 9},
			{Name: "on", Label: "On", Kind: sep2admin.ActionToggle},
			{Name: "raw", Label: "Raw", Kind: sep2admin.ActionText, MaxLen: 64},
		},
		Run: run,
	}}
	return p
}

func buildActions(t *testing.T, cfg Config, panels ...sep2admin.Panel) http.Handler {
	t.Helper()
	cfg.AdminKey = panelTestKey
	cfg.Panels = panels
	cfg.LoopbackBypass = true
	h, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

// post sends a JSON POST from loopback with the Bearer key.
func post(h http.Handler, path, body string, mut ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Authorization", "Bearer "+panelTestKey)
	req.Header.Set("Content-Type", "application/json")
	for _, m := range mut {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPanelActionsOffAnswer404AndAreNotListed(t *testing.T) {
	var rec actionRecorder
	// Stores set, so the dashboard's catch-all "GET /" is mounted and an
	// unmounted POST would be a 405 unless panelActionsOff answers it.
	h := buildActions(t, Config{Stores: &Stores{}}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))

	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusNotFound || r.Body.String() != `{"error":"panel actions are off"}` {
		t.Errorf("POST with actions off = %d %s, want 404 panel actions are off", r.Code, r.Body)
	}
	if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusNotFound {
		t.Errorf("GET actions with actions off = %d, want 404", r.Code)
	}
	want := `[{"id":"bus","label":"Label bus"}]`
	if r := get(h, "/api/ui/panels", true); r.Body.String() != want {
		t.Errorf("list with actions off = %s, want %s", r.Body, want)
	}
	if n := rec.calls.Load(); n != 0 {
		t.Errorf("Run called %d times with actions off, want 0", n)
	}
}

func TestPanelActionsOnAreListedAndDescribed(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))

	wantList := `[{"id":"bus","label":"Label bus","actions":[{"id":"send","label":"Send"}]}]`
	if r := get(h, "/api/ui/panels", true); r.Body.String() != wantList {
		t.Errorf("list = %s, want %s", r.Body, wantList)
	}
	wantSchema := `{"actions":[{"id":"send","label":"Send","fields":[` +
		`{"name":"device","label":"Device","kind":"choice","choices":[{"id":"dev-1","label":"Inverter 1"},{"id":"dev-2","label":"Inverter 2"}]},` +
		`{"name":"multiplier","label":"Multiplier","kind":"integer","min":-9,"max":9},` +
		`{"name":"on","label":"On","kind":"toggle"},` +
		`{"name":"raw","label":"Raw","kind":"text","maxLen":64}]}]}`
	if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusOK || r.Body.String() != wantSchema {
		t.Errorf("schema = %d %s\nwant 200 %s", r.Code, r.Body, wantSchema)
	}
	if r := get(h, "/api/ui/panels/bus/actions", false); r.Code != http.StatusUnauthorized {
		t.Errorf("schema read with no credential = %d, want 401", r.Code)
	}
}

func TestPanelActionRunsWithTheValidatedValues(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{Message: "sent"}, nil)))

	r := post(h, actionPath, goodActionBody)
	if r.Code != http.StatusOK || r.Body.String() != `{"ok":true,"message":"sent"}` {
		t.Fatalf("POST = %d %s, want 200 {\"ok\":true,\"message\":\"sent\"}", r.Code, r.Body)
	}
	if ct := r.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	rec.mu.Lock()
	v := rec.last
	rec.mu.Unlock()
	if v.String("device") != "dev-1" || v.Int("multiplier") != 3 || !v.Bool("on") || v.String("raw") != `{"a":1}` {
		t.Errorf("Run saw device=%q multiplier=%d on=%t raw=%q", v.String("device"), v.Int("multiplier"), v.Bool("on"), v.String("raw"))
	}
}

func TestPanelActionWorksOnAReadOnlyPlane(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true, ReadOnly: true}, actionPanel(rec.run(sep2admin.ActionResult{Message: "sent"}, nil)))

	// Control: the read-only plane really refuses a store write.
	if r := post(h, "/api/fsas", `{}`); r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("control: POST /api/fsas on a read-only plane = %d, want 404 or 405", r.Code)
	}
	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusOK {
		t.Fatalf("action on a read-only plane = %d, want 200; body %s", r.Code, r.Body)
	}
	if n := rec.calls.Load(); n != 1 {
		t.Errorf("Run called %d times, want 1", n)
	}
}

func TestPanelActionRefusesInvalidValuesBeforeRun(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"integer over bound", `{"device":"dev-1","multiplier":10,"on":true,"raw":"x"}`, `{"error":"invalid action values","field":"multiplier"}`},
		{"fraction", `{"device":"dev-1","multiplier":1.5,"on":true,"raw":"x"}`, `{"error":"invalid action values","field":"multiplier"}`},
		{"choice not offered", `{"device":"dev-9","multiplier":1,"on":true,"raw":"x"}`, `{"error":"invalid action values","field":"device"}`},
		{"string for toggle", `{"device":"dev-1","multiplier":1,"on":"yes","raw":"x"}`, `{"error":"invalid action values","field":"on"}`},
		{"text over its max", `{"device":"dev-1","multiplier":1,"on":true,"raw":"` + strings.Repeat("a", 65) + `"}`, `{"error":"invalid action values","field":"raw"}`},
		{"unknown field", `{"device":"dev-1","multiplier":1,"on":true,"raw":"x","extra":1}`, `{"error":"invalid action values"}`},
		{"not json", `device=dev-1`, `{"error":"invalid action values"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec actionRecorder
			h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))
			r := post(h, actionPath, c.body)
			if r.Code != http.StatusBadRequest || r.Body.String() != c.want {
				t.Fatalf("POST = %d %s, want 400 %s", r.Code, r.Body, c.want)
			}
			if n := rec.calls.Load(); n != 0 {
				t.Errorf("Run called %d times for a refused submission, want 0", n)
			}
		})
	}
}

func TestPanelActionAnswersTheEmbedderRefusalAndFailure(t *testing.T) {
	t.Run("refusal reason", func(t *testing.T) {
		var rec actionRecorder
		h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, &sep2admin.ActionRefusal{Reason: "publishing is off"})))
		r := post(h, actionPath, goodActionBody)
		if r.Code != http.StatusUnprocessableEntity || r.Body.String() != `{"error":"publishing is off"}` {
			t.Fatalf("POST = %d %s, want 422 with the reason", r.Code, r.Body)
		}
	})
	t.Run("other error is not echoed", func(t *testing.T) {
		var rec actionRecorder
		h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, errors.New("dial tcp 10.0.0.7:61613: refused"))))
		r := post(h, actionPath, goodActionBody)
		if r.Code != http.StatusInternalServerError || r.Body.String() != `{"error":"action failed"}` {
			t.Fatalf("POST = %d %s, want 500 action failed", r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "10.0.0.7") {
			t.Error("the embedder's error text reached the response")
		}
	})
	t.Run("a panic is contained", func(t *testing.T) {
		h := buildActions(t, Config{PanelActions: true}, actionPanel(func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
			panic("boom /srv/x")
		}))
		r := post(h, actionPath, goodActionBody)
		if r.Code != http.StatusInternalServerError || strings.Contains(r.Body.String(), "/srv/x") {
			t.Fatalf("POST = %d %s, want 500 without the panic text", r.Code, r.Body)
		}
	})
}

func TestBoundedActionText(t *testing.T) {
	long := strings.Repeat("a", sep2admin.MaxActionMessageBytes-1) + "\u00e9"
	got := boundedActionText(long)
	if want := strings.Repeat("a", sep2admin.MaxActionMessageBytes-1); got != want {
		t.Errorf("cut at a rune boundary = %d bytes, want the %d ASCII bytes", len(got), len(want))
	}
	if got := boundedActionText("a\xffb"); got != "a\ufffdb" {
		t.Errorf("invalid UTF-8 = %q, want the replacement character", got)
	}
	if got := boundedActionText("sent"); got != "sent" {
		t.Errorf("short text = %q, want unchanged", got)
	}
}

func TestPanelActionRequiresJSONAndCapsTheBody(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))

	r := post(h, actionPath, goodActionBody, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
	if r.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", r.Code)
	}

	over := `{"device":"dev-1","multiplier":1,"on":true,"raw":"` + strings.Repeat("a", sep2admin.MaxActionBodyBytes) + `"}`
	if r := post(h, actionPath, over); r.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared oversize body = %d, want 413", r.Code)
	}
	// Control: the unknown length path. A body of exactly the cap is read
	// and judged by its content, not refused for size.
	exact := goodActionBody + strings.Repeat(" ", sep2admin.MaxActionBodyBytes-len(goodActionBody))
	if r := post(h, actionPath, exact); r.Code != http.StatusOK {
		t.Errorf("body of exactly %d bytes = %d, want 200; body %s", sep2admin.MaxActionBodyBytes, r.Code, r.Body)
	}
	var read atomic.Bool
	unread := func(r *http.Request) {
		r.ContentLength = sep2admin.MaxActionBodyBytes + 1
		r.Body = io.NopCloser(readFlag{&read})
	}
	if r := post(h, actionPath, "", unread); r.Code != http.StatusRequestEntityTooLarge || read.Load() {
		t.Errorf("declared oversize length = %d, body read = %t; want 413 with the body unread", r.Code, read.Load())
	}
	chunked := func(r *http.Request) { r.ContentLength = -1 }
	if r := post(h, actionPath, over, chunked); r.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body of unknown length = %d, want 413", r.Code)
	}
	if n := rec.calls.Load(); n != 1 {
		t.Errorf("Run called %d times, want 1 (only the exact-cap body)", n)
	}
}

// readFlag records that its body was read.
type readFlag struct{ read *atomic.Bool }

func (r readFlag) Read([]byte) (int, error) {
	r.read.Store(true)
	return 0, io.EOF
}

func TestPanelActionIsRateLimitedToABurstOfFive(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))

	for i := 1; i <= actionBurst; i++ {
		if r := post(h, actionPath, goodActionBody); r.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, r.Code)
		}
	}
	r := post(h, actionPath, goodActionBody)
	if r.Code != http.StatusTooManyRequests || r.Body.String() != `{"error":"too many panel actions"}` {
		t.Fatalf("request %d = %d %s, want 429", actionBurst+1, r.Code, r.Body)
	}
	if got := r.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if n := rec.calls.Load(); n != actionBurst {
		t.Errorf("Run called %d times, want %d", n, actionBurst)
	}
}

func TestTokenBucketRefillsOnePerSecondAndCapsAtTheBurst(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newTokenBucket(func() time.Time { return now })
	for i := 0; i < actionBurst; i++ {
		if !b.allow() {
			t.Fatalf("call %d refused inside the burst", i+1)
		}
	}
	if b.allow() {
		t.Fatal("call after the burst allowed")
	}
	now = now.Add(500 * time.Millisecond)
	if b.allow() {
		t.Error("allowed after half a refill period")
	}
	now = now.Add(500 * time.Millisecond)
	if !b.allow() {
		t.Error("refused after one full refill period")
	}
	if b.allow() {
		t.Error("a second token appeared after one period")
	}
	now = now.Add(time.Hour)
	allowed := 0
	for b.allow() {
		allowed++
	}
	if allowed != actionBurst {
		t.Errorf("after an idle hour %d calls allowed, want the burst of %d", allowed, actionBurst)
	}
}

func TestPanelActionRefusalsDoNotSpendTheRate(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))
	for i := 0; i < actionBurst*2; i++ {
		if r := post(h, "/api/ui/panels/bus/actions/nope", goodActionBody); r.Code != http.StatusNotFound {
			t.Fatalf("unknown action = %d, want 404", r.Code)
		}
	}
	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusOK {
		t.Fatalf("a valid action after %d 404s = %d, want 200", actionBurst*2, r.Code)
	}
}

// TestPanelActionIsGuardedAsASensitiveWrite proves the credential guard
// covers a POST under the sensitive panel prefix: the route is not on the
// non-sensitive allowlist, so a loopback request with no credential is
// refused where the bypass would otherwise admit it.
func TestPanelActionIsGuardedAsASensitiveWrite(t *testing.T) {
	const pattern = "POST /api/ui/panels/{id}/actions/{action}"
	if _, exempt := nonSensitiveAdminWrites[pattern]; exempt {
		t.Fatalf("%s is on nonSensitiveAdminWrites, so it would skip the credential guard", pattern)
	}
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))

	// Control: the bypass is on, so an exempt write from loopback with no
	// credential gets past the guard (it is then refused as a bad body, not 401).
	noKey := func(r *http.Request) { r.Header.Del("Authorization") }
	if r := post(h, "/api/fsas", `{}`, noKey); r.Code == http.StatusUnauthorized {
		t.Fatalf("control: loopback POST /api/fsas with no credential = 401, want it admitted by the bypass")
	}
	if r := post(h, actionPath, goodActionBody, noKey); r.Code != http.StatusUnauthorized {
		t.Errorf("loopback POST of an action with no credential = %d, want 401", r.Code)
	}
	wrong := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong-key") }
	if r := post(h, actionPath, goodActionBody, wrong); r.Code != http.StatusUnauthorized {
		t.Errorf("POST of an action with a wrong key = %d, want 401", r.Code)
	}
	if n := rec.calls.Load(); n != 0 {
		t.Errorf("Run called %d times without a credential, want 0", n)
	}
}

func TestPanelActionRefusesAOneTimeTicket(t *testing.T) {
	var rec actionRecorder
	tickets := auth.NewTicketStore(AdminTicketTTL)
	h := buildActions(t, Config{PanelActions: true, Tickets: tickets}, actionPanel(rec.run(sep2admin.ActionResult{Message: "sent"}, nil)))

	// Control: the auth chain itself admits a ticket (it is not a 401 before
	// the action's own check).
	tk, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	noKey := func(r *http.Request) { r.Header.Del("Authorization") }
	if r := post(h, "/api/ui/panels/bus/actions?ticket="+tk, ``, noKey); r.Code == http.StatusUnauthorized {
		t.Fatalf("control: a ticket was refused on a sensitive path before the action check: %s", r.Body)
	}
	tk, err = tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	r := post(h, actionPath+"?ticket="+tk, goodActionBody, noKey)
	if r.Code != http.StatusUnauthorized || r.Body.String() != `{"error":"panel actions do not accept a ticket"}` {
		t.Fatalf("ticket POST = %d %s, want 401 from the action's own refusal", r.Code, r.Body)
	}
	if n := rec.calls.Load(); n != 0 {
		t.Errorf("Run called %d times for a ticket, want 0", n)
	}
}

func TestPanelActionRefusesACrossSiteRequest(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))
	cross := func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }
	if r := post(h, actionPath, goodActionBody, cross); r.Code != http.StatusForbidden {
		t.Errorf("cross-site POST through the router = %d, want 403", r.Code)
	}
	if r := post(h, actionPath, goodActionBody, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }); r.Code != http.StatusOK {
		t.Errorf("control: same-origin POST = %d, want 200", r.Code)
	}

	// The handler refuses on its own, with no outer middleware in front.
	ps, err := newPanelSet([]sep2admin.Panel{actionPanel(rec.run(sep2admin.ActionResult{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ps.actions = true
	direct := func(site string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, actionPath, strings.NewReader(goodActionBody))
		req.SetPathValue("id", "bus")
		req.SetPathValue("action", "send")
		req.Header.Set("Content-Type", "application/json")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rr := httptest.NewRecorder()
		ps.handleAction().ServeHTTP(rr, req)
		return rr
	}
	if r := direct("cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("cross-site POST to the handler alone = %d, want 403", r.Code)
	}
	if r := direct("same-site"); r.Code != http.StatusForbidden {
		t.Errorf("same-site (a sibling origin) POST to the handler alone = %d, want 403", r.Code)
	}
	if r := direct(""); r.Code != http.StatusOK {
		t.Errorf("control: no Sec-Fetch-Site to the handler alone = %d, want 200", r.Code)
	}
}

func TestPanelActionRunsUnderTheViewsOneAtATimeFlag(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	run := func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		close(entered)
		<-release
		return sep2admin.ActionResult{Message: "done"}, nil
	}
	h := buildActions(t, Config{PanelActions: true}, actionPanel(run))

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- post(h, actionPath, goodActionBody) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the action never started")
	}

	if r := get(h, "/api/ui/panels/bus", true); r.Code != http.StatusGatewayTimeout {
		t.Errorf("View during a running action = %d, want 504", r.Code)
	}
	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusGatewayTimeout {
		t.Errorf("second action during a running one = %d, want 504", r.Code)
	}
	close(release)
	if r := <-first; r.Code != http.StatusOK || r.Body.String() != `{"ok":true,"message":"done"}` {
		t.Errorf("first action = %d %s, want 200 done", r.Code, r.Body)
	}
	if r := get(h, "/api/ui/panels/bus", true); r.Code != http.StatusOK {
		t.Errorf("View after the action returned = %d, want 200", r.Code)
	}
}

func TestPanelActionEndsWhenThePlaneShutsDown(t *testing.T) {
	shutdown := make(chan struct{})
	closePlane := func() { close(shutdown) }
	entered := make(chan struct{})
	ended := make(chan error, 1)
	run := func(ctx context.Context, _ sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		close(entered)
		select {
		case <-ctx.Done():
			ended <- ctx.Err()
		case <-time.After(3 * time.Second):
			ended <- errors.New("the action's context did not end on shutdown")
		}
		return sep2admin.ActionResult{}, ctx.Err()
	}
	h := buildActions(t, Config{PanelActions: true, StreamsDone: shutdown}, actionPanel(run))

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(h, actionPath, goodActionBody) }()
	<-entered
	closePlane()

	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("action ended with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the action did not end")
	}
	r := <-done
	if r.Code != http.StatusServiceUnavailable {
		t.Errorf("response to the interrupted action = %d %s, want 503", r.Code, r.Body)
	}
	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusServiceUnavailable || r.Body.String() != `{"error":"plane is shutting down"}` {
		t.Errorf("new action after shutdown = %d %s, want 503 shutting down", r.Code, r.Body)
	}
}

func TestPanelActionRoutesAreListedOnlyWhenOn(t *testing.T) {
	const post = "POST /api/ui/panels/{id}/actions/{action}"
	const read = "GET /api/ui/panels/{id}/actions"
	for _, on := range []bool{false, true} {
		_, patterns, err := Build(Config{AdminKey: panelTestKey, PanelActions: on, Panels: []sep2admin.Panel{actionPanel(okRunFn)}})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(patterns, post); got != on {
			t.Errorf("PanelActions=%t: %q listed = %t, want %t", on, post, got, on)
		}
		if got := slices.Contains(patterns, read); got != on {
			t.Errorf("PanelActions=%t: %q listed = %t, want %t", on, read, got, on)
		}
	}
}

func okRunFn(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
	return sep2admin.ActionResult{}, nil
}

func TestPanelActionBodyIsNotReadForAnUnknownAction(t *testing.T) {
	var rec actionRecorder
	h := buildActions(t, Config{PanelActions: true}, actionPanel(rec.run(sep2admin.ActionResult{}, nil)))
	body := bytes.Repeat([]byte("a"), sep2admin.MaxActionBodyBytes*2)
	if r := post(h, "/api/ui/panels/bus/actions/nope", string(body)); r.Code != http.StatusNotFound {
		t.Errorf("unknown action with a large body = %d, want 404 before the cap", r.Code)
	}
	if r := post(h, "/api/ui/panels/nope/actions/send", goodActionBody); r.Code != http.StatusNotFound {
		t.Errorf("unknown panel = %d, want 404", r.Code)
	}
}

// logBuffer is a log sink a handler goroutine and a test can share.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureLog routes the standard logger, which slog's default handler also
// uses, into a buffer for the test.
func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

func TestTimedOutActionIsAuditedAndTellsTheOperatorItMayComplete(t *testing.T) {
	logs := captureLog(t)
	release := make(chan struct{})
	returned := make(chan struct{})
	run := func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		defer close(returned)
		<-release // ignores ctx: the action keeps going after the 504
		return sep2admin.ActionResult{}, nil
	}
	ps, err := newPanelSet([]sep2admin.Panel{actionPanel(run)})
	if err != nil {
		t.Fatal(err)
	}
	ps.actions = true
	ps.timeout = 50 * time.Millisecond
	cfg := runConfig(panelTestKey, nil, nil, "GCM", nil, nil, nil)
	authed, withMW := buildAuthedAdminMux(cfg, ps)
	h, _ := buildOuterAdminRouter(cfg, authed, withMW)

	r := post(h, actionPath, goodActionBody)
	want := `{"error":"action did not answer in time; it may still complete, so do not retry blindly"}`
	if r.Code != http.StatusGatewayTimeout || r.Body.String() != want {
		t.Fatalf("POST = %d %s, want 504 %s", r.Code, r.Body, want)
	}
	for _, frag := range []string{`panel "bus" action "send"`, "admission=bearer", "remote=127.0.0.1:40000", "outcome=timeout"} {
		if !strings.Contains(logs.String(), frag) {
			t.Errorf("log lacks %q after a timed-out action:\n%s", frag, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "outcome=started") {
		t.Errorf("log lacks the attempt line (outcome=started):\n%s", logs.String())
	}
	close(release)
	<-returned
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "outcome=late-ok") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "outcome=late-ok") {
		t.Errorf("log lacks the late completion line (outcome=late-ok):\n%s", logs.String())
	}
}

func TestEveryRefusalIsLogged(t *testing.T) {
	cases := []struct {
		name string
		do   func(h http.Handler) *httptest.ResponseRecorder
		code int
		frag string
	}{
		{"unknown values", func(h http.Handler) *httptest.ResponseRecorder {
			return post(h, actionPath, `{"device":"dev-1","multiplier":99,"on":true,"raw":"x"}`)
		}, http.StatusBadRequest, "reason=invalid"},
		{"oversize body", func(h http.Handler) *httptest.ResponseRecorder {
			return post(h, actionPath, strings.Repeat("a", sep2admin.MaxActionBodyBytes+1))
		}, http.StatusRequestEntityTooLarge, "reason=body-too-large"},
		{"ticket", func(h http.Handler) *httptest.ResponseRecorder {
			return post(h, actionPath, goodActionBody, func(r *http.Request) {
				r.Header.Del("Authorization")
				r.URL.RawQuery = "ticket=" + testTicket(t)
			})
		}, http.StatusUnauthorized, "reason=ticket"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLog(t)
			tickets = auth.NewTicketStore(AdminTicketTTL)
			h := buildActions(t, Config{PanelActions: true, Tickets: tickets}, actionPanel(okRunFn))
			r := c.do(h)
			if r.Code != c.code {
				t.Fatalf("status = %d %s, want %d", r.Code, r.Body, c.code)
			}
			for _, frag := range []string{`panel "bus" action "send"`, c.frag, "remote=127.0.0.1:40000"} {
				if !strings.Contains(logs.String(), frag) {
					t.Errorf("log lacks %q:\n%s", frag, logs.String())
				}
			}
		})
	}

	t.Run("rate limit", func(t *testing.T) {
		logs := captureLog(t)
		h := buildActions(t, Config{PanelActions: true}, actionPanel(okRunFn))
		var last *httptest.ResponseRecorder
		for i := 0; i <= actionBurst; i++ {
			last = post(h, actionPath, goodActionBody)
		}
		if last.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", last.Code)
		}
		if !strings.Contains(logs.String(), "reason=rate-limited") {
			t.Errorf("log lacks reason=rate-limited:\n%s", logs.String())
		}
	})
}

var tickets *auth.TicketStore

func testTicket(t *testing.T) string {
	t.Helper()
	tk, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestActionSchemaReadFollowsShutdownAndTheSharedRate(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		done := make(chan struct{})
		h := buildActions(t, Config{PanelActions: true, StreamsDone: done}, actionPanel(okRunFn))
		if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusOK {
			t.Fatalf("control: schema read before shutdown = %d, want 200", r.Code)
		}
		close(done)
		if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusServiceUnavailable {
			t.Errorf("schema read after shutdown = %d %s, want 503", r.Code, r.Body)
		}
	})
	t.Run("rate", func(t *testing.T) {
		h := buildActions(t, Config{PanelActions: true}, actionPanel(okRunFn))
		for i := 0; i < actionBurst; i++ {
			if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusOK {
				t.Fatalf("read %d = %d, want 200", i+1, r.Code)
			}
		}
		if r := get(h, "/api/ui/panels/bus/actions", true); r.Code != http.StatusTooManyRequests {
			t.Errorf("read %d = %d, want 429", actionBurst+1, r.Code)
		}
		if r := post(h, actionPath, goodActionBody); r.Code != http.StatusTooManyRequests {
			t.Errorf("an action after the reads spent the burst = %d, want 429 (shared bucket)", r.Code)
		}
	})
}

func TestAnUndeclaredFieldNameInRunIsNotASilentZero(t *testing.T) {
	logs := captureLog(t)
	h := buildActions(t, Config{PanelActions: true}, actionPanel(func(_ context.Context, v sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		return sep2admin.ActionResult{Message: strconv.FormatInt(v.Int("multipler"), 10)}, nil // typo
	}))
	r := post(h, actionPath, goodActionBody)
	if r.Code != http.StatusInternalServerError || r.Body.String() != `{"error":"action failed"}` {
		t.Fatalf("POST = %d %s, want 500 action failed", r.Code, r.Body)
	}
	if !strings.Contains(logs.String(), `"multipler"`) {
		t.Errorf("log does not name the undeclared field:\n%s", logs.String())
	}
}

func TestAPanickingRunLogsAnOutcomeLineWithoutThePanicText(t *testing.T) {
	logs := captureLog(t)
	h := buildActions(t, Config{PanelActions: true}, actionPanel(func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		panic("secret-token-123")
	}))
	if r := post(h, actionPath, goodActionBody); r.Code != http.StatusInternalServerError {
		t.Fatalf("POST = %d %s, want 500", r.Code, r.Body)
	}
	var outcome string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "outcome=panic") {
			outcome = line
		}
	}
	if !strings.Contains(outcome, `panel "bus" action "send"`) || !strings.Contains(outcome, "admission=bearer") {
		t.Fatalf("no outcome=panic line naming the panel and action:\n%s", logs.String())
	}
	if strings.Contains(outcome, "secret-token-123") {
		t.Errorf("the outcome line carries the panic text: %s", outcome)
	}
}
