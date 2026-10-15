package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #922: the program edit and delete routes need a real credential even from
// loopback, and a read-only plane does not mount them.

const editBody = `{"description":"edited","primacy":2}`

func programEditStores(t *testing.T) *Stores {
	t.Helper()
	s := deviceProgramStores(t)
	s.DERControls = memory.NewDERControlStore()
	s.DERControlLifecycles = dercontrol.NewLifecycleStore()
	if err := s.AdminFSAs.Create(context.Background(), "fsa-T", sep2.FunctionSetAssignments{MRID: "fsa-T"}); err != nil {
		t.Fatal(err)
	}
	p := sep2.DERProgram{MRID: "0000000000000000000000000000F1C5", Description: "fixture", Primacy: 1}
	p.Href = "/edev/1/fsa/fsa-A/derp/0"
	if err := s.DERPrograms.Create(context.Background(), "1", "0", p); err != nil {
		t.Fatal(err)
	}
	return s
}

func withMethod(m string) func(*http.Request) { return func(r *http.Request) { r.Method = m } }

func noCredential(r *http.Request) { r.Header.Del("Authorization") }

func TestProgramEditRoutes_NeedARealCredential(t *testing.T) {
	stores := programEditStores(t)
	h := buildActions(t, Config{Stores: stores})
	r := post(h, "/api/fsas/fsa-T/der-programs", `{"description":"t","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":6000}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("template create = %d %s", r.Code, r.Body)
	}
	var tmpl struct{ ID string }
	if err := json.Unmarshal(r.Body.Bytes(), &tmpl); err != nil {
		t.Fatal(err)
	}

	routes := []struct{ method, pattern, path, body string }{
		{http.MethodPut, "PUT /api/fsas/{id}/der-programs/{derpId}", "/api/fsas/fsa-T/der-programs/" + tmpl.ID, editBody},
		{http.MethodPut, "PUT /api/devices/{id}/der-programs/{derpId}", "/api/devices/1/der-programs/0", editBody},
		{http.MethodDelete, "DELETE /api/fsas/{id}/der-programs/{derpId}", "/api/fsas/fsa-T/der-programs/" + tmpl.ID, ""},
		{http.MethodDelete, "DELETE /api/devices/{id}/der-programs/{derpId}", "/api/devices/1/der-programs/0", ""},
	}
	for _, rt := range routes {
		if _, exempt := nonSensitiveAdminWrites[rt.pattern]; exempt {
			t.Errorf("%s is on nonSensitiveAdminWrites", rt.pattern)
		}
		if r := post(h, rt.path, rt.body, withMethod(rt.method), noCredential); r.Code != http.StatusUnauthorized {
			t.Errorf("loopback %s with no credential = %d, want 401", rt.pattern, r.Code)
		}
	}
	ctx := context.Background()
	if p, _ := stores.DERPrograms.Get(ctx, "1", "0"); p.Description != "fixture" {
		t.Fatalf("a refused request wrote the program: %+v", p)
	}
	if got, _ := stores.AdminFSAs.Template(ctx, "fsa-T", tmpl.ID); got.Description != "t" {
		t.Fatalf("a refused request wrote the template: %+v", got)
	}

	want := []int{http.StatusOK, http.StatusOK, http.StatusNoContent, http.StatusNoContent}
	for i, rt := range routes {
		if r := post(h, rt.path, rt.body, withMethod(rt.method)); r.Code != want[i] {
			t.Errorf("%s with the key = %d %s, want %d", rt.pattern, r.Code, r.Body, want[i])
		}
		if i == 1 {
			if p, _ := stores.DERPrograms.Get(ctx, "1", "0"); p.Description != "edited" || p.Version == nil || *p.Version != 1 {
				t.Errorf("program after PUT = %+v, want edited at version 1", p)
			}
		}
	}
	if _, err := stores.DERPrograms.Get(ctx, "1", "0"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("program after DELETE: %v, want gone", err)
	}
	if _, err := stores.AdminFSAs.Template(ctx, "fsa-T", tmpl.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("template after DELETE: %v, want gone", err)
	}
}

func TestProgramEditRoutes_AbsentOnAReadOnlyPlane(t *testing.T) {
	h := buildActions(t, Config{Stores: programEditStores(t), ReadOnly: true})
	for _, rt := range []struct{ method, path string }{
		{http.MethodPut, "/api/fsas/fsa-T/der-programs/derp-0000000000000001"},
		{http.MethodDelete, "/api/fsas/fsa-T/der-programs/derp-0000000000000001"},
		{http.MethodPut, "/api/devices/1/der-programs/0"},
		{http.MethodDelete, "/api/devices/1/der-programs/0"},
	} {
		if r := post(h, rt.path, editBody, withMethod(rt.method)); r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s on a read-only plane = %d, want 404 or 405", rt.method, rt.path, r.Code)
		}
	}
}

// Program deletes and control creates must lock the same scopes, so the FSA
// handler takes the process's one issuer when the stores carry it.
func TestProgramRetirer_IsTheControlRoutesIssuer(t *testing.T) {
	stores := programEditStores(t)
	issuer, err := assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, stores.PEN)
	if err != nil {
		t.Fatal(err)
	}
	stores.DERControlIssuer = issuer
	fsaH := newAdminFSAHandler(stores)
	if fsaH.Retirer != handler.ProgramRetirer(issuer) {
		t.Errorf("FSA handler retirer = %v, want the stores' issuer %p", fsaH.Retirer, issuer)
	}
	if derH := newAdminDERControlHandler(stores); derH.Issuer != handler.DERControlIssuer(issuer) {
		t.Errorf("control handler issuer = %v, want the stores' issuer %p", derH.Issuer, issuer)
	}
}
