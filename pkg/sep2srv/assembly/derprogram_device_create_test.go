package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #921: a program created on one device is served under its own FSA's list
// and single-program route, and under no other FSA.
func TestDeviceProgramCreate_IsServedUnderItsOwnFSAOnly(t *testing.T) {
	t.Parallel()
	stores := testStores()
	ctx := context.Background()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	for _, fsa := range []string{"f1", "f2"} {
		if err := stores.FSAs.Create(ctx, "e1", fsa, sep2.FunctionSetAssignments{MRID: fsa}); err != nil {
			t.Fatal(err)
		}
	}
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler.AdminFSAHandler{
		AdminFSAs:  memory.NewAdminFSAStore(),
		DeviceFSAs: stores.FSAs,
		EndDevices: stores.EndDevices,
		Copies:     &fsaprogram.Copies{Programs: stores.DERPrograms, Defaults: stores.DefaultDERControls, Origins: origins},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices/{id}/fsas/{fsaId}/der-programs", h.HandleCreateDeviceProgram())
	req := httptest.NewRequest(http.MethodPost, "/api/devices/e1/fsas/f1/der-programs",
		strings.NewReader(`{"description":"volt support","primacy":2,"mRID":"0123456789ABCDEF0123456789ABCDEF","defaultControl":{"type":"maxLimW","maxLimW":6000}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	router, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	in := getList[sep2.DERProgramList](t, srv, "/edev/e1/fsa/f1/derp?l=255")
	if in.All != 1 || len(in.DERProgram) != 1 {
		t.Fatalf("f1 list all=%d items=%d, want 1", in.All, len(in.DERProgram))
	}
	p := in.DERProgram[0]
	href := p.Href
	if !strings.HasPrefix(href, "/edev/e1/fsa/f1/derp/derp-") || p.MRID != "0123456789ABCDEF0123456789ABCDEF" || p.Description != "volt support" || p.Primacy != 2 ||
		p.DERControlListLink == nil || p.DERControlListLink.Href != href+"/derc" ||
		p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != href+"/dderc" {
		t.Errorf("served program = %+v", p)
	}
	if out := getList[sep2.DERProgramList](t, srv, "/edev/e1/fsa/f2/derp?l=255"); out.All != 0 || len(out.DERProgram) != 0 {
		t.Errorf("f2 list all=%d items=%d, want 0", out.All, len(out.DERProgram))
	}

	status := func(path string) int {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := status(href); got != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", href, got)
	}
	other := strings.Replace(href, "/fsa/f1/", "/fsa/f2/", 1)
	if got := status(other); got != http.StatusNotFound {
		t.Errorf("GET %s = %d, want 404", other, got)
	}
}
