// Tests for the FSA handler family.
// No auth seam: FSA handlers are auth-clean (they rely on the auth middleware
// upstream, so unit tests exercise path-routing and store behaviour directly).
package fsa_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	corefsa "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/fsa"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandleFSA_NotFoundReturnsDefault: when no FSA is stored for (edevID,
// fsaID), HandleFSA returns 200 with a synthetic default that includes a
// DERProgramListLink. Asserts Href and DERProgramListLink (data-invariants
// Rule 1: field-value assertions on the wire-level resource).
func TestHandleFSA_NotFoundReturnsDefault(t *testing.T) {
	t.Parallel()

	fsaStore := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}", corefsa.HandleFSA(fsaStore))

	req := httptest.NewRequest(http.MethodGet, "/edev/e1/fsa/f1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got sep2.FunctionSetAssignments
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Href != "/edev/e1/fsa/f1" {
		t.Errorf("Href = %q, want /edev/e1/fsa/f1", got.Href)
	}
	if got.DERProgramListLink == nil {
		t.Fatal("DERProgramListLink is nil in default FSA")
	}
	if got.DERProgramListLink.Href != "/edev/e1/fsa/f1/derp" {
		t.Errorf("DERProgramListLink.Href = %q, want /edev/e1/fsa/f1/derp",
			got.DERProgramListLink.Href)
	}
}

// TestHandleFSA_StoredValueReturned: when an FSA is stored, the exact stored
// record (including MRID) is returned.
func TestHandleFSA_StoredValueReturned(t *testing.T) {
	t.Parallel()

	fsaStore := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	fsa := sep2.FunctionSetAssignments{
		Resource:    sep2.Resource{Href: "/edev/e1/fsa/f1"},
		MRID:        "fsa-mrid-01",
		Description: "Test FSA",
		DERProgramListLink: &sep2.ListLink{
			Href: "/edev/e1/fsa/f1/derp",
		},
	}
	if err := fsaStore.Create(context.Background(), "e1", "f1", fsa); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}", corefsa.HandleFSA(fsaStore))

	req := httptest.NewRequest(http.MethodGet, "/edev/e1/fsa/f1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got sep2.FunctionSetAssignments
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MRID != "fsa-mrid-01" {
		t.Errorf("MRID = %q, want fsa-mrid-01", got.MRID)
	}
}

// TestHandleFSA_MethodNotAllowed: only GET/HEAD are permitted.
func TestHandleFSA_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	fsaStore := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	h := corefsa.HandleFSA(fsaStore)

	req := httptest.NewRequest(http.MethodPost, "/edev/e1/fsa/f1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestHandleCreateFSA_HappyPath: POST with edevId query param returns 201
// with Location and a well-formed FSA including DERProgramListLink.
func TestHandleCreateFSA_HappyPath(t *testing.T) {
	t.Parallel()

	fsaStore := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	h := corefsa.HandleCreateFSA(fsaStore)

	req := httptest.NewRequest(http.MethodPost, "/api/fsa?edevId=e1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc == "" {
		t.Error("missing Location header")
	}
	var got sep2.FunctionSetAssignments
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DERProgramListLink == nil {
		t.Fatal("DERProgramListLink is nil in created FSA")
	}
}

// TestHandleCreateFSA_MissingEdevIDReturns400: query param edevId is required.
func TestHandleCreateFSA_MissingEdevIDReturns400(t *testing.T) {
	t.Parallel()

	fsaStore := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	h := corefsa.HandleCreateFSA(fsaStore)

	req := httptest.NewRequest(http.MethodPost, "/api/fsa", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestBuildFSAList: asserts All, Results, and item-slice length.
func TestBuildFSAList(t *testing.T) {
	t.Parallel()

	items := []sep2.FunctionSetAssignments{{}, {}}
	result := store.ListResult[sep2.FunctionSetAssignments]{
		All: 4, Results: 2, Items: items,
	}
	list := corefsa.BuildFSAList("/edev/e1/fsa", result, 900)
	if list.All != 4 {
		t.Errorf("All = %d, want 4", list.All)
	}
	if list.Results != 2 {
		t.Errorf("Results = %d, want 2", list.Results)
	}
	if len(list.FunctionSetAssignments) != 2 {
		t.Errorf("len(FunctionSetAssignments) = %d, want 2", len(list.FunctionSetAssignments))
	}
}
