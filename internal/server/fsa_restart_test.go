package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// bootFSA is one boot as Run performs it for the FSA stores.
func bootFSA(t *testing.T, cfg *config.Config) *Stores {
	t.Helper()
	s, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatalf("newRunStores: %v", err)
	}
	if err := seedStores(context.Background(), cfg, s); err != nil {
		t.Fatalf("seedStores: %v", err)
	}
	return s
}

func adminCall(t *testing.T, s *Stores, method, path, body string) {
	t.Helper()
	h := &handler.AdminFSAHandler{AdminFSAs: s.AdminFSAs, DeviceFSAs: s.FSAs}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/fsas", h.HandleCreateAdminFSA())
	mux.HandleFunc("POST /api/devices/{id}/fsa-assignment", h.HandleAssignDeviceFSA())
	mux.HandleFunc("DELETE /api/devices/{id}/fsa-assignment", h.HandleUnassignDeviceFSA())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	if rec.Code >= 300 {
		t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body)
	}
}

func createFSAVia(t *testing.T, s *Stores, id, desc string) {
	t.Helper()
	adminCall(t, s, "POST", "/api/fsas", `{"description":"`+desc+`","mRID":"`+id+`"}`)
}

func assignVia(t *testing.T, s *Stores, fsaID, deviceID string) {
	t.Helper()
	adminCall(t, s, "POST", "/api/devices/"+deviceID+"/fsa-assignment", `{"fsaHref":"/api/fsas/`+fsaID+`"}`)
}

func wantDeviceFSA(t *testing.T, s *Stores, deviceID, fsaID, desc string) {
	t.Helper()
	got, err := s.FSAs.Get(context.Background(), deviceID, fsaID)
	if err != nil {
		t.Fatalf("FSA %s on device %s: %v", fsaID, deviceID, err)
	}
	want := sep2.FunctionSetAssignments{
		Resource:           sep2.Resource{Href: "/edev/" + deviceID + "/fsa/" + fsaID},
		MRID:               fsaID,
		Description:        desc,
		DERProgramListLink: &sep2.ListLink{Href: "/edev/" + deviceID + "/fsa/" + fsaID + "/derp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FSA %s on device %s = %+v, want %+v", fsaID, deviceID, got, want)
	}
}

func wantNoDeviceFSA(t *testing.T, s *Stores, deviceID, fsaID string) {
	t.Helper()
	if _, err := s.FSAs.Get(context.Background(), deviceID, fsaID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("FSA %s on device %s: err = %v, want ErrNotFound", fsaID, deviceID, err)
	}
}

// TestDeviceFSA_SurvivesARestart: the device FSA /edev/{id}/fsa serves is
// rebuilt from the persisted admin record, field for field.
func TestDeviceFSA_SurvivesARestart(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	boot1 := bootFSA(t, cfg)
	createFSAVia(t, boot1, "fsa-1", "east feeder")
	assignVia(t, boot1, "fsa-1", "7")
	wantDeviceFSA(t, boot1, "7", "fsa-1", "east feeder")

	boot2 := bootFSA(t, cfg)
	wantDeviceFSA(t, boot2, "7", "fsa-1", "east feeder")
	wantNoDeviceFSA(t, boot2, "8", "fsa-1")
}

// TestDeviceFSA_ManyAssignmentsSurviveARestart: two FSAs on one device and
// one FSA on two devices all come back.
func TestDeviceFSA_ManyAssignmentsSurviveARestart(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	boot1 := bootFSA(t, cfg)
	createFSAVia(t, boot1, "fsa-a", "alpha")
	createFSAVia(t, boot1, "fsa-b", "beta")
	assignVia(t, boot1, "fsa-a", "7")
	assignVia(t, boot1, "fsa-b", "7")
	assignVia(t, boot1, "fsa-a", "8")

	boot2 := bootFSA(t, cfg)
	wantDeviceFSA(t, boot2, "7", "fsa-a", "alpha")
	wantDeviceFSA(t, boot2, "7", "fsa-b", "beta")
	wantDeviceFSA(t, boot2, "8", "fsa-a", "alpha")
	wantNoDeviceFSA(t, boot2, "8", "fsa-b")
}

// TestDeviceFSA_UnassignmentSurvivesARestart: a device unassigned before the
// restart stays unassigned; the other device keeps its FSA.
func TestDeviceFSA_UnassignmentSurvivesARestart(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	boot1 := bootFSA(t, cfg)
	createFSAVia(t, boot1, "fsa-1", "east feeder")
	assignVia(t, boot1, "fsa-1", "7")
	assignVia(t, boot1, "fsa-1", "8")
	adminCall(t, boot1, "DELETE", "/api/devices/8/fsa-assignment?fsaHref=/api/fsas/fsa-1", "")
	wantNoDeviceFSA(t, boot1, "8", "fsa-1")

	boot2 := bootFSA(t, cfg)
	wantDeviceFSA(t, boot2, "7", "fsa-1", "east feeder")
	wantNoDeviceFSA(t, boot2, "8", "fsa-1")
}

// TestDeviceFSA_AdminAssignmentWinsOverFixture: a boot-fixture FSA with the
// same device and id as an admin assignment does not stop the boot, and the
// device ends up with the admin record.
func TestDeviceFSA_AdminAssignmentWinsOverFixture(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	boot1 := bootFSA(t, cfg)
	createFSAVia(t, boot1, "fsa-1", "from admin")
	assignVia(t, boot1, "fsa-1", "7")

	fixture := filepath.Join(t.TempDir(), "fixture.yaml")
	const yaml = `
end_devices:
  - id: "7"
    sfdi: "1"
    lfdi: "AA"
    changed_time: 100
fsas:
  - end_device_id: "7"
    id: "fsa-1"
    mrid: "FIXTURE"
    description: "from fixture"
`
	if err := os.WriteFile(fixture, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	boot2 := bootFSA(t, &config.Config{DataDir: dir, BootFixtureFile: fixture})
	wantDeviceFSA(t, boot2, "7", "fsa-1", "from admin")
}

// TestDeviceFSA_NoDataDirIsEmpty: with no data dir nothing is rebuilt.
func TestDeviceFSA_NoDataDirIsEmpty(t *testing.T) {
	s := bootFSA(t, &config.Config{})
	if got := s.AdminFSAs.List(context.Background()); len(got) != 0 {
		t.Errorf("admin FSAs = %d, want 0", len(got))
	}
	res, err := s.FSAs.List(context.Background(), "7", store.ListOptions{Unbounded: true})
	if err != nil || len(res.Items) != 0 {
		t.Errorf("device 7 FSAs = %d, %v, want 0", len(res.Items), err)
	}
}
