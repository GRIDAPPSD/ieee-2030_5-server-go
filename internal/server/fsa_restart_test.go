package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

func assignFSA(t *testing.T, s *Stores, fsaID, deviceID string) {
	t.Helper()
	ctx := context.Background()
	h := &handler.AdminFSAHandler{AdminFSAs: s.AdminFSAs, DeviceFSAs: s.FSAs}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/fsas", h.HandleCreateAdminFSA())
	mux.HandleFunc("POST /api/devices/{id}/fsa-assignment", h.HandleAssignDeviceFSA())
	for _, req := range []*http.Request{
		httptest.NewRequest("POST", "/api/fsas", strings.NewReader(`{"description":"east feeder","mRID":"`+fsaID+`"}`)),
		httptest.NewRequest("POST", "/api/devices/"+deviceID+"/fsa-assignment", strings.NewReader(`{"fsaHref":"/api/fsas/`+fsaID+`"}`)),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s = %d: %s", req.Method, req.URL.Path, rec.Code, rec.Body)
		}
	}
	if _, err := s.FSAs.Get(ctx, deviceID, fsaID); err != nil {
		t.Fatalf("device FSA after assignment: %v", err)
	}
}

// TestDeviceFSA_SurvivesARestart: the device FSA /edev/{id}/fsa serves is
// rebuilt from the persisted admin record, field for field.
func TestDeviceFSA_SurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	boot1, _, err := newRunStores(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	assignFSA(t, boot1, "fsa-1", "7")
	want, err := boot1.FSAs.Get(ctx, "7", "fsa-1")
	if err != nil {
		t.Fatal(err)
	}

	boot2, _, err := newRunStores(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	got, err := boot2.FSAs.Get(ctx, "7", "fsa-1")
	if err != nil {
		t.Fatalf("device FSA after restart: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rebuilt FSA = %+v, want %+v", got, want)
	}
	if got.Href != "/edev/7/fsa/fsa-1" || got.MRID != "fsa-1" || got.Description != "east feeder" ||
		got.DERProgramListLink == nil || got.DERProgramListLink.Href != "/edev/7/fsa/fsa-1/derp" {
		t.Errorf("rebuilt FSA fields wrong: %+v", got)
	}
	if _, err := boot2.FSAs.Get(ctx, "8", "fsa-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unassigned device 8 FSA err = %v, want not found", err)
	}
}

// TestDeviceFSA_NoDataDirIsEmpty: with no data dir nothing is rebuilt.
func TestDeviceFSA_NoDataDirIsEmpty(t *testing.T) {
	s, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.AdminFSAs.List(context.Background()); len(got) != 0 {
		t.Errorf("admin FSAs = %d, want 0", len(got))
	}
}
