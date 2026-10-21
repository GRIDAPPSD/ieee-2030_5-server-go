package adminplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #972: the FSA control writes change what every assigned device does, so
// they need a real credential even from loopback and are absent without
// control writes; the boot rebuild completes a copy a fan-out left missing.

func fsaControlStores(t *testing.T) *Stores {
	t.Helper()
	s := programEditStores(t)
	s.EndDeviceManagers = memory.NewEndDeviceManagementStore()
	s.FlowReservationResponses = memory.NewScopedStore[sep2.FlowReservationResponse]()
	s.FlowReservationResponseLifecycles = memory.NewScopedStore[dercontrol.LifecycleRecord]()
	s.CommitmentLedger = NewCommitmentLedger(s)
	pen := uint32(0xA0B1)
	s.PEN = &pen
	return s
}

// fsaControlSetup creates a template on fsa-T, assigns device 1 and returns
// the template id.
func fsaControlSetup(t *testing.T, h http.Handler) string {
	t.Helper()
	r := post(h, "/api/fsas/fsa-T/der-programs", `{"description":"t","primacy":120,"defaultControl":{"type":"maxLimW","maxLimW":10000}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("template create = %d %s", r.Code, r.Body)
	}
	var tmpl struct{ ID string }
	if err := json.Unmarshal(r.Body.Bytes(), &tmpl); err != nil {
		t.Fatal(err)
	}
	if r := post(h, "/api/devices/1/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-T"}`); r.Code != http.StatusOK {
		t.Fatalf("assign = %d %s", r.Code, r.Body)
	}
	return tmpl.ID
}

func fsaControlCreateBody() string {
	return fmt.Sprintf(`{"type":"maxLimW","maxLimW":4000,"startTime":%d,"durationSeconds":900}`, sep2time.Now().Unix()+3600)
}

func TestFSAControlRoutes_NeedARealCredential(t *testing.T) {
	stores := fsaControlStores(t)
	h := buildActions(t, Config{Stores: stores, ControlWrites: true})
	tmpl := fsaControlSetup(t, h)
	base := "/api/fsas/fsa-T/der-programs/" + tmpl + "/controls"
	for _, rt := range []struct{ pattern, path string }{
		{"POST /api/fsas/{id}/der-programs/{derpId}/controls", base},
		{"POST /api/fsas/{id}/der-programs/{derpId}/controls/{controlId}/cancel", base + "/x/cancel"},
	} {
		if _, exempt := nonSensitiveAdminWrites[rt.pattern]; exempt {
			t.Errorf("%s is on nonSensitiveAdminWrites", rt.pattern)
		}
		if r := post(h, rt.path, fsaControlCreateBody(), noCredential); r.Code != http.StatusUnauthorized {
			t.Errorf("loopback %s with no credential = %d, want 401", rt.pattern, r.Code)
		}
	}
	if recs := stores.AdminFSAs.FSAControls(context.Background(), "fsa-T", tmpl); len(recs) != 0 {
		t.Fatalf("a refused request wrote %d records", len(recs))
	}
	if r := post(h, base, fsaControlCreateBody()); r.Code != http.StatusCreated {
		t.Fatalf("create with the key = %d %s", r.Code, r.Body)
	}
	if r := post(h, base, "", withMethod(http.MethodGet), noCredential); r.Code != http.StatusOK {
		t.Errorf("list from loopback = %d %s, want 200", r.Code, r.Body)
	}
}

func TestFSAControlRoutes_WritesAbsentWithoutControlWrites(t *testing.T) {
	stores := fsaControlStores(t)
	h := buildActions(t, Config{Stores: stores})
	tmpl := fsaControlSetup(t, h)
	r := post(h, "/api/fsas/fsa-T/der-programs/"+tmpl+"/controls", fsaControlCreateBody())
	if r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
		t.Errorf("create with control writes off = %d, want 404 or 405", r.Code)
	}
}

func TestCompleteFSAControlReplicas_RestoresAMissingCopy(t *testing.T) {
	stores := fsaControlStores(t)
	h := buildActions(t, Config{Stores: stores, ControlWrites: true})
	tmpl := fsaControlSetup(t, h)
	r := post(h, "/api/fsas/fsa-T/der-programs/"+tmpl+"/controls", fsaControlCreateBody())
	if r.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.Code, r.Body)
	}
	var got struct{ ID, MRID string }
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := "1/fsa-T/" + tmpl
	// The copy a crash before its write would have left out.
	if err := stores.DERControls.Delete(ctx, scope, got.ID); err != nil {
		t.Fatal(err)
	}
	if err := stores.DERControlLifecycles.Delete(ctx, scope, got.ID); err != nil {
		t.Fatal(err)
	}

	if err := CompleteFSAControlReplicas(ctx, stores, t.Logf); err != nil {
		t.Fatalf("CompleteFSAControlReplicas: %v", err)
	}
	c, err := stores.DERControls.Get(ctx, scope, got.ID)
	if err != nil || c.MRID != got.MRID || c.DERControlBase == nil || c.DERControlBase.OpModMaxLimW == nil || *c.DERControlBase.OpModMaxLimW != 4000 {
		t.Fatalf("restored copy = %+v, %v; want mRID %s and maxLimW 4000", c, err, got.MRID)
	}
	if err := CompleteFSAControlReplicas(ctx, stores, t.Logf); err != nil {
		t.Fatalf("second run: %v", err)
	}
	list, err := stores.DERControls.List(ctx, scope, store.ListOptions{Unbounded: true})
	if err != nil || len(list.Items) != 1 {
		t.Errorf("copies after a second run = %d, %v; want 1", len(list.Items), err)
	}
}
