package assembly_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #970: a client polling its own FSA, program and control lists reads the
// device-only program and the control sent through POST /api/devices/{id}/controls.
func TestDeviceControl_IsReadOnTheWireByThatDevicesPoll(t *testing.T) {
	t.Parallel()
	stores := testStores()
	controls := memory.NewDERControlStore()
	stores.DERControls = controls
	stores.CommitmentLedger = sources.NewLedger(
		stores.EndDevices, stores.EndDeviceManagers,
		stores.FlowReservationResponses, stores.FlowReservationResponseLifecycles,
		controls, stores.DERControlLifecycles,
	)
	seedOwnedDevices(t, stores.EndDevices, "e1", "e2")
	pen := uint32(0xA0B1)
	issuer, err := assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, &pen)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	fsaH := &handler.AdminFSAHandler{
		AdminFSAs:  memory.NewAdminFSAStore(),
		DeviceFSAs: stores.FSAs,
		EndDevices: stores.EndDevices,
		Copies:     &fsaprogram.Copies{Programs: stores.DERPrograms, Defaults: stores.DefaultDERControls, Origins: origins},
		PEN:        &pen,
	}
	ctlH := &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   controls,
		Lifecycles: stores.DERControlLifecycles,
		Programs:   stores.DERPrograms,
		EndDevices: stores.EndDevices,
		Fleets:     commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers},
		Ledger:     stores.CommitmentLedger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices/{id}/controls", (&handler.AdminDeviceControlHandler{FSAs: fsaH, Controls: ctlH}).HandleCreate())

	// e1 already follows a primacy-100 program, which the new one must beat.
	other := sep2.DERProgram{MRID: "00000000000000000000000000000064", Description: "feeder", Primacy: 100}
	other.Href = "/edev/e1/fsa/f1/derp/p100"
	other.DERControlListLink = &sep2.ListLink{Href: other.Href + "/derc"}
	if err := stores.DERPrograms.Create(t.Context(), "e1", "p100", other); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"kind":"scheduled","type":"maxLimW","maxLimW":4500,"startTime":%d,"durationSeconds":900}`, sep2time.Now().Unix()+3600)
	req := httptest.NewRequest(http.MethodPost, "/api/devices/e1/controls", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	fsaID, ok := fsaH.AdminFSAs.DeviceOnlyFSA("e1")
	if !ok {
		t.Fatal("no device-only FSA for e1")
	}

	router, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	fsas := getList[sep2.FunctionSetAssignmentsList](t, srv, "/edev/e1/fsa?l=255")
	if fsas.All != 1 || len(fsas.FunctionSetAssignments) != 1 || fsas.FunctionSetAssignments[0].Href != "/edev/e1/fsa/"+fsaID {
		t.Fatalf("FSA list = all %d %+v, want one at /edev/e1/fsa/%s", fsas.All, fsas.FunctionSetAssignments, fsaID)
	}
	progs := getList[sep2.DERProgramList](t, srv, "/edev/e1/fsa/"+fsaID+"/derp?l=255")
	if progs.All != 1 || len(progs.DERProgram) != 1 {
		t.Fatalf("program list all=%d items=%d, want 1", progs.All, len(progs.DERProgram))
	}
	p := progs.DERProgram[0]
	if p.Primacy != 99 || p.Primacy >= other.Primacy {
		t.Errorf("served primacy = %d, want 99, below the feeder program's %d", p.Primacy, other.Primacy)
	}
	if p.DERControlListLink == nil || p.DERControlListLink.Href != p.Href+"/derc" {
		t.Errorf("served program = %+v", p)
	}
	ctrls := getList[sep2.DERControlList](t, srv, p.Href+"/derc?l=255")
	if ctrls.All != 1 || len(ctrls.DERControl) != 1 {
		t.Fatalf("control list all=%d items=%d, want 1", ctrls.All, len(ctrls.DERControl))
	}
	c := ctrls.DERControl[0]
	if c.DERControlBase == nil || c.DERControlBase.OpModMaxLimW == nil || uint16(*c.DERControlBase.OpModMaxLimW) != 4500 ||
		c.Interval == nil || c.Interval.Duration != 900 {
		t.Errorf("served control = %+v, want maxLimW 4500 for 900 s", c)
	}

	// The control is on e1's poll only: e2 has no such FSA.
	if other := getList[sep2.FunctionSetAssignmentsList](t, srv, "/edev/e2/fsa?l=255"); other.All != 0 {
		t.Errorf("e2 FSA list all=%d, want 0", other.All)
	}
}
