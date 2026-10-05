package sep2adminplane_test

import (
	"context"
	"net/http"
	"testing"

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

	p.Close()
	p.Close() // safe to repeat
	if rec := send(p, http.MethodPost, "/api/ui/panels/switch/actions/publishing", `{"on":false}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST after Close = %d %s, want 503", rec.Code, rec.Body)
	}
	if len(got) != 1 {
		t.Fatalf("Run called after Close: %v", got)
	}
}
