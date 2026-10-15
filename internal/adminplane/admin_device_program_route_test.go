package adminplane

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #921: the device program route needs a real credential even from loopback,
// and a read-only plane does not mount it.

const deviceProgramPath = "/api/devices/1/fsas/fsa-A/der-programs"
const deviceProgramBody = `{"description":"volt support","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":6000}}`

func deviceProgramStores(t *testing.T) *Stores {
	t.Helper()
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Stores{
		EndDevices:         memory.NewEndDeviceStore(),
		DERPrograms:        memory.NewDERProgramStore(),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		AdminFSAs:          memory.NewAdminFSAStore(),
		ProgramOrigins:     origins,
	}
	ctx := context.Background()
	if err := s.EndDevices.Create(ctx, "1", sep2.EndDevice{LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7", SFDI: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FSAs.Create(ctx, "1", "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeviceProgramRoute_NeedsARealCredentialAndWritesThroughTheStores(t *testing.T) {
	const pattern = "POST /api/devices/{id}/fsas/{fsaId}/der-programs"
	if _, exempt := nonSensitiveAdminWrites[pattern]; exempt {
		t.Fatalf("%s is on nonSensitiveAdminWrites, so it would skip the credential guard", pattern)
	}
	stores := deviceProgramStores(t)
	h := buildActions(t, Config{Stores: stores})

	noKey := func(r *http.Request) { r.Header.Del("Authorization") }
	if r := post(h, deviceProgramPath, deviceProgramBody, noKey); r.Code != http.StatusUnauthorized {
		t.Fatalf("loopback POST with no credential = %d, want 401", r.Code)
	}
	if list, _ := stores.DERPrograms.List(context.Background(), "1", store.ListOptions{Unbounded: true}); len(list.Items) != 0 {
		t.Fatalf("a refused request wrote %d programs", len(list.Items))
	}

	r := post(h, deviceProgramPath, deviceProgramBody)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST with the key = %d %s, want 201", r.Code, r.Body)
	}
	list, err := stores.DERPrograms.List(context.Background(), "1", store.ListOptions{Unbounded: true})
	if err != nil || len(list.Items) != 1 || !strings.HasPrefix(list.Items[0].Href, "/edev/1/fsa/fsa-A/derp/derp-") {
		t.Fatalf("stored programs = %+v, %v", list.Items, err)
	}
	id := strings.TrimPrefix(list.Items[0].Href, "/edev/1/fsa/fsa-A/derp/")
	if o, err := stores.ProgramOrigins.Get(context.Background(), "1", id); err != nil || o.Kind != memory.ProgramOriginDevice || o.FSAID != "fsa-A" {
		t.Errorf("origin = %+v, %v", o, err)
	}
}

func TestDeviceProgramRoute_AbsentOnAReadOnlyPlane(t *testing.T) {
	h := buildActions(t, Config{Stores: deviceProgramStores(t), ReadOnly: true})
	if r := post(h, deviceProgramPath, deviceProgramBody); r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST on a read-only plane = %d, want 404 or 405", r.Code)
	}
}
