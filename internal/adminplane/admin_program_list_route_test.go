package adminplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
