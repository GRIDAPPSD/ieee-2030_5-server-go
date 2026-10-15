package adminplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #923: GET /api/der-programs is a read on the authed mux, so a request from
// off the loopback needs the admin key, and the route is absent when the
// program stores are not wired.

func TestProgramListRoute_ServesWithTheKeyAndRefusesWithout(t *testing.T) {
	h := buildActions(t, Config{Stores: programEditStores(t)})

	if r := get(h, "/api/der-programs", true); r.Code != http.StatusOK {
		t.Fatalf("with the key = %d %s", r.Code, r.Body)
	} else {
		var got struct {
			Persists bool `json:"persists"`
			Total    int  `json:"total"`
			Programs []struct {
				Device, FSA, ID, Origin, OnRestart string
			} `json:"programs"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Persists || got.Total != 1 || len(got.Programs) != 1 {
			t.Fatalf("reply = %+v", got)
		}
		p := got.Programs[0]
		if p.Device != "1" || p.FSA != "fsa-A" || p.ID != "0" || p.Origin != "fixture" || p.OnRestart != "reseeded" {
			t.Errorf("program = %+v", p)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/der-programs", nil)
	req.RemoteAddr = "203.0.113.9:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("off loopback with no credential = %d, want 401", rec.Code)
	}
}

func TestProgramListRoute_AbsentWithoutProgramStores(t *testing.T) {
	stores := programEditStores(t)
	stores.ProgramOrigins = nil
	h := buildActions(t, Config{Stores: stores})
	if r := get(h, "/api/der-programs", true); r.Code == http.StatusOK {
		t.Errorf("route answered 200 with no origin index: %s", r.Body)
	}
}

// With a data dir the stores Run builds persist, and the route built over
// them reports it: a wiring that lost the persistence answer would list every
// program as gone or reseeded.
func TestProgramListRoute_PersistentStoresReportKept(t *testing.T) {
	dir := t.TempDir()
	programs, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dir, "derprograms.json"))
	if err != nil {
		t.Fatal(err)
	}
	origins, err := memory.NewProgramOriginStore(filepath.Join(dir, "program-origins.json"))
	if err != nil {
		t.Fatal(err)
	}
	fsas, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
	if err != nil {
		t.Fatal(err)
	}
	stores := deviceProgramStores(t)
	stores.DERPrograms, stores.ProgramOrigins, stores.AdminFSAs = programs, origins, fsas
	copies := fsaprogram.Copies{Programs: programs, Defaults: stores.DefaultDERControls, Origins: origins}
	own := memory.ProgramTemplate{ID: "derp-d1", MRID: "BBBB0000000000000000000000000001", Description: "own",
		Primacy: 65, DefaultMRID: "BBBB0000000000000000000000000002"}
	if _, err := copies.EnsureDevice(context.Background(), "1", "fsa-A", own); err != nil {
		t.Fatal(err)
	}
	fx := sep2.DERProgram{MRID: "CCCC0000000000000000000000000001", Primacy: 2}
	fx.Href = "/edev/1/fsa/fsa-A/derp/fx1"
	if err := programs.Create(context.Background(), "1", "fx1", fx); err != nil {
		t.Fatal(err)
	}

	h := buildActions(t, Config{Stores: stores})
	r := get(h, "/api/der-programs", true)
	if r.Code != http.StatusOK {
		t.Fatalf("status %d %s", r.Code, r.Body)
	}
	var got struct {
		Persists bool `json:"persists"`
		Programs []struct {
			ID, Origin, OnRestart string
		} `json:"programs"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Persists || len(got.Programs) != 2 {
		t.Fatalf("reply = %+v", got)
	}
	want := map[string]string{"derp-d1": "device", "fx1": "fixture"}
	for _, p := range got.Programs {
		if p.Origin != want[p.ID] || p.OnRestart != "kept" {
			t.Errorf("program %+v, want origin %q kept", p, want[p.ID])
		}
	}
}
