package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// afterListPrograms runs hook once, right after List returns, so a test can
// place a concurrent writer between the guard's listing and its Retire.
type afterListPrograms struct {
	store.ScopedStore[sep2.DERProgram]
	hook func()
}

func (p *afterListPrograms) List(ctx context.Context, parent string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	res, err := p.ScopedStore.List(ctx, parent, opts)
	if p.hook != nil {
		p.hook()
	}
	return res, err
}

func seedLiveProgram(t *testing.T, programs store.ScopedStore[sep2.DERProgram], controls store.ScopedStore[sep2.DERControl], dev, prog string) {
	t.Helper()
	ctx := context.Background()
	var p sep2.DERProgram
	p.Href = "/edev/" + dev + "/fsa/f1/derp/" + prog
	if err := programs.Create(ctx, dev, prog, p); err != nil {
		t.Fatalf("seed program %s: %v", prog, err)
	}
	var c sep2.DERControl
	c.Href = p.Href + "/derc/c1"
	c.Interval = &sep2.DateTimeInterval{Start: time.Now().Unix() + 600, Duration: 300}
	if err := controls.Create(ctx, dev+"/f1/"+prog, "c1", c); err != nil {
		t.Fatalf("seed control on %s: %v", prog, err)
	}
}

// An assignment copies a live control onto the device as a new program, so
// the delete must hold the program lock from its listing through the delete:
// the assignment then either lands first and is refused as live, or finds the
// device gone. The writer here takes the same lock as every program writer and,
// like an assignment, writes only to a device that still exists.
func TestGuardEndDeviceDelete_HoldsTheProgramLockAcrossTheListing(t *testing.T) {
	ctx := context.Background()
	programs, controls, lifecycles := memory.NewDERProgramStore(), memory.NewDERControlStore(), dercontrol.NewLifecycleStore()
	pen := uint32(0xA0B1)
	issuer, err := dercontrol.NewIssuer(programs, controls, lifecycles, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	devices := memory.NewEndDeviceStore()
	if err := devices.Create(ctx, "7", sep2.EndDevice{}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	// A program with no control: nothing live when the guard lists it.
	var quiet sep2.DERProgram
	quiet.Href = "/edev/7/fsa/f1/derp/p1"
	if err := programs.Create(ctx, "7", "p1", quiet); err != nil {
		t.Fatalf("seed program: %v", err)
	}

	h := &AdminFSAHandler{Retirer: issuer}
	del := func(w http.ResponseWriter, r *http.Request) {
		if err := devices.Delete(r.Context(), r.PathValue("id")); err != nil {
			t.Errorf("delete device: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}

	writerDone := make(chan struct{})
	hooked := &afterListPrograms{ScopedStore: programs}
	hooked.hook = func() {
		go func() {
			defer close(writerDone)
			h.programMu.Lock()
			defer h.programMu.Unlock()
			if _, err := devices.Get(ctx, "7"); err == nil {
				seedLiveProgram(t, programs, controls, "7", "p2")
			}
		}()
		// Long enough for an unlocked writer to finish inside the window.
		select {
		case <-writerDone:
		case <-time.After(300 * time.Millisecond):
		}
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/devices/7", nil)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()
	h.GuardEndDeviceDelete(hooked, del)(rec, req)
	<-writerDone

	_, devErr := devices.Get(ctx, "7")
	live, err := controls.Count(ctx, "7/f1/p2")
	if err != nil {
		t.Fatalf("count controls: %v", err)
	}
	if devErr != nil && live > 0 {
		t.Fatalf("device 7 was deleted (status %d) and a live control on its new program was left behind", rec.Code)
	}
}

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
