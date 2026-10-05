package sep2adminplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
)

func actionConfig(onAction func(sep2admin.ActionValues)) sep2adminplane.Config {
	cfg := baseConfig()
	cfg.Panels = []sep2admin.Panel{{
		ID:                "switch",
		Label:             "Switch",
		Placement:         sep2admin.ExtensionSlot(1),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		View: func(context.Context) (sep2admin.Descriptor, error) {
			return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
		},
		Actions: []sep2admin.Action{{
			ID:     "publishing",
			Label:  "Publishing",
			Fields: []sep2admin.ActionField{{Name: "on", Label: "On", Kind: sep2admin.ActionToggle}},
			Run: func(_ context.Context, v sep2admin.ActionValues) (sep2admin.ActionResult, error) {
				onAction(v)
				return sep2admin.ActionResult{Message: "ok"}, nil
			},
		}},
	}}
	return cfg
}

func TestPanelActionsAreOffByDefault(t *testing.T) {
	p := newPlane(t, actionConfig(func(sep2admin.ActionValues) { t.Error("Run called with PanelActions off") }))
	if rec := send(p, http.MethodPost, "/api/ui/panels/switch/actions/publishing", `{"on":true}`, true); rec.Code != http.StatusNotFound {
		t.Fatalf("POST with PanelActions off = %d %s, want 404", rec.Code, rec.Body)
	}
	if rec := send(p, http.MethodGet, "/api/ui/panels/switch/actions", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("GET actions with PanelActions off = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestPanelActionsWorkOnAReadOnlyPlaneAndStopOnClose(t *testing.T) {
	var got []bool
	cfg := actionConfig(func(v sep2admin.ActionValues) { got = append(got, v.Bool("on")) })
	cfg.PanelActions = true
	cfg.ReadOnly = true
	p := newPlane(t, cfg)

	rec := send(p, http.MethodPost, "/api/ui/panels/switch/actions/publishing", `{"on":true}`, true)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true,"message":"ok"}` {
		t.Fatalf("POST = %d %s, want 200 ok", rec.Code, rec.Body)
	}
	if len(got) != 1 || !got[0] {
		t.Fatalf("Run saw %v, want [true]", got)
	}

	if n := p.Close(time.Second); n != 0 {
		t.Fatalf("Close reported %d running actions, want 0", n)
	}
	p.Close(0) // safe to repeat
	if rec := send(p, http.MethodPost, "/api/ui/panels/switch/actions/publishing", `{"on":false}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST after Close = %d %s, want 503", rec.Code, rec.Body)
	}
	if len(got) != 1 {
		t.Fatalf("Run called after Close: %v", got)
	}
}

func TestCloseWaitsBoundedForARunThatIgnoresItsContext(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	cfg := actionConfig(nil)
	cfg.Panels[0].Actions[0].Run = func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
		close(entered)
		<-release // ignores ctx on purpose
		return sep2admin.ActionResult{}, nil
	}
	cfg.PanelActions = true
	p := newPlane(t, cfg)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- send(p, http.MethodPost, "/api/ui/panels/switch/actions/publishing", `{"on":true}`, true)
	}()
	<-entered

	start := time.Now()
	if n := p.Close(50 * time.Millisecond); n != 1 {
		t.Fatalf("Close with a stuck action = %d running, want 1", n)
	}
	if el := time.Since(start); el < 40*time.Millisecond || el > 2*time.Second {
		t.Errorf("Close waited %v, want about the 50ms bound", el)
	}
	close(release)
	if n := p.Close(2 * time.Second); n != 0 {
		t.Fatalf("Close after the action returned = %d running, want 0", n)
	}
	<-done
}

func TestCloseCountsAnActionStillInChoicesAndRunNeverStarts(t *testing.T) {
	inChoices := make(chan struct{})
	releaseChoices := make(chan struct{})
	var runs atomic.Int32
	cfg := baseConfig()
	cfg.PanelActions = true
	cfg.Panels = []sep2admin.Panel{{
		ID:                "switch",
		Label:             "Switch",
		Placement:         sep2admin.ExtensionSlot(1),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		View: func(context.Context) (sep2admin.Descriptor, error) {
			return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
		},
		Actions: []sep2admin.Action{{
			ID:    "pick",
			Label: "Pick",
			Fields: []sep2admin.ActionField{{Name: "dev", Label: "Dev", Kind: sep2admin.ActionChoice, Choices: func(context.Context) ([]sep2admin.Choice, error) {
				close(inChoices)
				<-releaseChoices // ignores ctx: a slow registry
				return []sep2admin.Choice{{ID: "d1", Label: "D1"}}, nil
			}}},
			Run: func(context.Context, sep2admin.ActionValues) (sep2admin.ActionResult, error) {
				runs.Add(1)
				return sep2admin.ActionResult{Message: "ran"}, nil
			},
		}},
	}}
	p := newPlane(t, cfg)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- send(p, http.MethodPost, "/api/ui/panels/switch/actions/pick", `{"dev":"d1"}`, true) }()
	<-inChoices

	if n := p.Close(50 * time.Millisecond); n != 1 {
		t.Fatalf("Close with a request inside Choices = %d running, want 1", n)
	}
	close(releaseChoices)
	r := <-done
	if r.Code != http.StatusServiceUnavailable {
		t.Errorf("response after Close = %d %s, want 503", r.Code, r.Body)
	}
	// The response can be sent while Choices is still running, so wait for
	// the count to reach 0 before asking whether Run started.
	if n := p.Close(time.Second); n != 0 {
		t.Errorf("Close after the request ended = %d running, want 0", n)
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("Run started %d times after Close, want 0", n)
	}
}
