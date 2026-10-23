package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestGuardEndDeviceDelete_RefusesAnUnaddressableProgram(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	programs := memory.NewDERProgramStore()
	var p sep2.DERProgram
	p.Href = "/not/a/program"
	if err := programs.Create(ctx, "7", "p1", p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &AdminFSAHandler{}
	called := false
	del := func(http.ResponseWriter, *http.Request) { called = true }
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/7", nil)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()
	h.GuardEndDeviceDelete(programs, del)(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"unaddressable_program"`) || called {
		t.Errorf("unaddressable program: %d %s (delete ran: %t), want 409 unaddressable_program and no delete", rec.Code, rec.Body.String(), called)
	}
}

func TestGuardEndDeviceDelete_ServerErrorBodyIsFixed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	programs := memory.NewDERProgramStore()
	var p sep2.DERProgram
	p.Href = "/edev/7/fsa/f1/derp/p1"
	if err := programs.Create(ctx, "7", "p1", p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &AdminFSAHandler{} // no Retirer: fails closed
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/7", nil)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()
	h.GuardEndDeviceDelete(programs, func(http.ResponseWriter, *http.Request) { t.Error("delete ran without a retirer") })(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), templateInternalError) || strings.Contains(rec.Body.String(), "issuer") {
		t.Errorf("no retirer: %d %s, want 500 with the fixed body %q", rec.Code, rec.Body.String(), templateInternalError)
	}
}
