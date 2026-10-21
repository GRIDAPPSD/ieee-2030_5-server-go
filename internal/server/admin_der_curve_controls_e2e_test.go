package server_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// #969: the admin plane checks a curve value against its own DERCurve store,
// on the control route and on a program's default control alike.
func TestAdminRouterChecksCurvesAgainstTheCurveStore(t *testing.T) {
	stores := newTestStores()
	pen := uint32(0xA0B1)
	stores.PEN = &pen
	ctx := context.Background()
	if err := stores.EndDevices.Create(ctx, "0", sep2.EndDevice{SFDI: "1", LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"}); err != nil {
		t.Fatal(err)
	}
	program := sep2.DERProgram{MRID: "P0", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/0/derp/0/derc"}}
	program.Href = "/edev/0/fsa/0/derp/0"
	if err := stores.DERPrograms.Create(ctx, "0", "0", program); err != nil {
		t.Fatal(err)
	}
	vv := sep2.DERCurve{MRID: "vv", CurveType: sep2.CurveTypeOpModVoltVar}
	vv.Href = "/dc/vv"
	if err := stores.DERCurves.Create(ctx, "vv", vv); err != nil {
		t.Fatal(err)
	}
	router, _ := adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), nil,
	)
	post := func(target, body string) (int, string) {
		w := serveDERControl(router, http.MethodPost, target, derControlRouteRequest{remote: "127.0.0.1:4000", contentType: "application/json", bearer: "the-key", body: body})
		return w.Code, w.Body.String()
	}
	control := func(curve string) string {
		return fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","functions":{"opModVoltVar":%q},"startTime":%d,"durationSeconds":300}`, curve, sep2time.Now().Unix()+600)
	}
	template := func(curve string) string {
		return fmt.Sprintf(`{"description":"vv","primacy":1,"defaultControl":{"functions":{"opModVoltVar":%q}}}`, curve)
	}

	if code, body := post("/api/fsas", `{"description":"roof","mRID":"fsa-roof"}`); code != http.StatusCreated {
		t.Fatalf("create FSA: %d %s", code, body)
	}
	for _, tc := range []struct {
		name, target, body string
		want               int
		error              string
	}{
		{"control, stored curve", "/api/der/controls", control("vv"), http.StatusCreated, ""},
		{"control, unknown curve", "/api/der/controls", control("nope"), http.StatusBadRequest, `{"error":"functions.opModVoltVar: no stored curve has this id"}`},
		{"default, stored curve", "/api/fsas/fsa-roof/der-programs", template("vv"), http.StatusCreated, ""},
		{"default, unknown curve", "/api/fsas/fsa-roof/der-programs", template("nope"), http.StatusBadRequest, `{"error":"defaultControl.functions.opModVoltVar: no stored curve has this id"}`},
	} {
		code, body := post(tc.target, tc.body)
		if code != tc.want || (tc.error != "" && body != tc.error+"\n") {
			t.Errorf("%s: %d %s, want %d %s", tc.name, code, body, tc.want, tc.error)
		}
	}
}
