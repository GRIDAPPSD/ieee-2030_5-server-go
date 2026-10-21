package assembly_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #972: one control on an FSA's program template reaches every device the
// FSA is assigned to, as each device's own poll reads it.

const fsaWirePEN = uint32(0xA0B1)

// fsaWireServer is one boot of a server over a data dir: the stores, the
// admin routes the FSA control path uses, and the protocol router a device
// polls.
type fsaWireServer struct {
	stores   *assembly.Stores
	admin    *memory.AdminFSAStore
	controls *memory.DERControlStore
	fsaH     *handler.AdminFSAHandler
	mux      *http.ServeMux
	wire     *httptest.Server
}

// controlCreateFailer fails the nth control Create from the count's start,
// standing in for a disk that fills while one fan-out is written.
type controlCreateFailer struct {
	*memory.DERControlStore
	failAt, calls int
}

func (c *controlCreateFailer) Create(ctx context.Context, parentID, id string, ctrl sep2.DERControl) error {
	c.calls++
	if c.calls == c.failAt {
		return errors.New("disk full")
	}
	return c.DERControlStore.Create(ctx, parentID, id, ctrl)
}

func bootFSAWire(t *testing.T, dir string, failCreateAt int) *fsaWireServer {
	t.Helper()
	ctx := context.Background()
	stores := testStores()
	path := func(name string) string { return filepath.Join(dir, name+".json") }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	programs, err := memory.NewDERProgramStoreWithPersistence(path("derprograms"))
	must(err)
	controls, err := memory.NewDERControlStoreWithPersistence(path("dercontrols"))
	must(err)
	lifecycles, err := dercontrol.NewLifecycleStoreWithPersistence(path("dercontrol-lifecycles"))
	must(err)
	defaults, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](path("defaultdercontrols"), "DefaultDERControl")
	must(err)
	admin, err := memory.NewAdminFSAStoreWithPersistence(path("fsas"))
	must(err)
	origins, err := memory.NewProgramOriginStore(path("program-origins"))
	must(err)
	stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, stores.DefaultDERControls = programs, controls, lifecycles, defaults
	stores.CommitmentLedger = sources.NewLedger(stores.EndDevices, stores.EndDeviceManagers,
		stores.FlowReservationResponses, stores.FlowReservationResponseLifecycles, controls, lifecycles)
	seedOwnedDevices(t, stores.EndDevices, "e1", "e2", "e3", "e4")

	// The boot rebuild of Run's seedStores.
	copies := fsaprogram.Copies{Programs: programs, Defaults: defaults, Origins: origins}
	must(fsaprogram.Reconcile(ctx, admin, stores.FSAs))
	must(fsaprogram.ReconcileCopies(ctx, admin, copies, t.Logf))

	var issuerControls store.ScopedStore[sep2.DERControl] = controls
	if failCreateAt > 0 {
		issuerControls = &controlCreateFailer{DERControlStore: controls, failAt: failCreateAt}
	}
	pen := fsaWirePEN
	issuer, err := dercontrol.NewIssuer(programs, issuerControls, lifecycles, dercontrol.Config{PEN: &pen})
	must(err)
	fsaH := &handler.AdminFSAHandler{
		AdminFSAs:  admin,
		DeviceFSAs: stores.FSAs,
		EndDevices: stores.EndDevices,
		Copies:     &copies,
		PEN:        &pen,
		Retirer:    issuer,
	}
	ctlH := &handler.AdminDERControlHandler{
		Issuer:      issuer,
		Controls:    controls,
		Lifecycles:  lifecycles,
		Programs:    programs,
		EndDevices:  stores.EndDevices,
		Fleets:      commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers},
		Ledger:      stores.CommitmentLedger,
		FSAControls: admin,
	}
	fsaCtlH := &handler.AdminFSAControlHandler{FSAs: fsaH, Controls: ctlH, Issuer: issuer}
	fsaH.Replicas = fsaCtlH

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/fsas/{id}/der-programs", fsaH.HandleCreateProgramTemplate())
	mux.HandleFunc("POST /api/devices/{id}/fsa-assignment", fsaH.HandleAssignDeviceFSA())
	mux.HandleFunc("DELETE /api/devices/{id}/fsa-assignment", fsaH.HandleUnassignDeviceFSA())
	mux.HandleFunc("POST /api/devices/{id}/controls", (&handler.AdminDeviceControlHandler{FSAs: fsaH, Controls: ctlH}).HandleCreate())
	mux.HandleFunc("POST /api/der/controls/{mrid}/cancel", ctlH.HandleCancel())
	mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls", fsaCtlH.HandleCreate())
	mux.HandleFunc("GET /api/fsas/{id}/der-programs/{derpId}/controls", fsaCtlH.HandleList())
	mux.HandleFunc("POST /api/fsas/{id}/der-programs/{derpId}/controls/{controlId}/cancel", fsaCtlH.HandleCancel())

	router, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	wire := httptest.NewServer(router)
	t.Cleanup(wire.Close)
	return &fsaWireServer{stores: stores, admin: admin, controls: controls, fsaH: fsaH, mux: mux, wire: wire}
}

func (s *fsaWireServer) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func (s *fsaWireServer) mustAdmin(t *testing.T, method, path, body string, want int) []byte {
	t.Helper()
	rec := s.call(t, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body.String(), want)
	}
	return rec.Body.Bytes()
}

// setupFSA creates fsa-1 with one template and returns the template id.
func (s *fsaWireServer) setupFSA(t *testing.T) string {
	t.Helper()
	if err := s.admin.Create(context.Background(), "fsa-1", sep2.FunctionSetAssignments{MRID: "fsa-1", Description: "feeder 7"}); err != nil {
		t.Fatal(err)
	}
	var tmpl struct{ ID string }
	body := s.mustAdmin(t, http.MethodPost, "/api/fsas/fsa-1/der-programs", `{"description":"feeder","primacy":120,"defaultControl":{"type":"maxLimW","maxLimW":10000}}`, http.StatusCreated)
	if err := json.Unmarshal(body, &tmpl); err != nil || tmpl.ID == "" {
		t.Fatalf("template reply %s: %v", body, err)
	}
	return tmpl.ID
}

func (s *fsaWireServer) assign(t *testing.T, dev string) *httptest.ResponseRecorder {
	t.Helper()
	return s.call(t, http.MethodPost, "/api/devices/"+dev+"/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-1"}`)
}

// wireControls is the DERControlList dev's poll reads under the template.
func (s *fsaWireServer) wireControls(t *testing.T, dev, tmpl string) sep2.DERControlList {
	t.Helper()
	return getList[sep2.DERControlList](t, s.wire, "/edev/"+dev+"/fsa/fsa-1/derp/"+tmpl+"/derc?l=255")
}

type fsaControlReply struct {
	ID           string `json:"id"`
	MRID         string `json:"mRID"`
	CreationTime int64  `json:"creationTime"`
	Cancelled    bool   `json:"cancelled"`
	Copies       []struct {
		Device      string `json:"device"`
		Href        string `json:"href"`
		EventStatus *struct {
			CurrentStatus uint8 `json:"currentStatus"`
		} `json:"eventStatus"`
	} `json:"copies"`
}

func fsaControlBody(maxLimW int) string {
	return fmt.Sprintf(`{"type":"maxLimW","maxLimW":%d,"startTime":%d,"durationSeconds":900}`, maxLimW, sep2time.Now().Unix()+3600)
}

// wantOneControl asserts dev's poll lists exactly the one control, by mRID,
// creationTime and value.
func wantOneControl(t *testing.T, s *fsaWireServer, dev, tmpl string, want fsaControlReply) sep2.DERControl {
	t.Helper()
	list := s.wireControls(t, dev, tmpl)
	if list.All != 1 || len(list.DERControl) != 1 {
		t.Fatalf("%s lists all=%d items=%d, want the one FSA control", dev, list.All, len(list.DERControl))
	}
	c := list.DERControl[0]
	if c.MRID != want.MRID || c.CreationTime != want.CreationTime {
		t.Errorf("%s lists mRID %s creationTime %d, want %s %d", dev, c.MRID, c.CreationTime, want.MRID, want.CreationTime)
	}
	if c.DERControlBase == nil || c.DERControlBase.OpModMaxLimW == nil || uint16(*c.DERControlBase.OpModMaxLimW) != 4500 || c.Interval == nil || c.Interval.Duration != 900 {
		t.Errorf("%s lists %+v, want maxLimW 4500 for 900 s", dev, c)
	}
	return c
}

func TestFSAControl_EveryDeviceListsItIncludingLaterAndAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s := bootFSAWire(t, dir, 0)
	tmpl := s.setupFSA(t)
	for _, d := range []string{"e1", "e2", "e3"} {
		if rec := s.assign(t, d); rec.Code != http.StatusOK {
			t.Fatalf("assign %s: %d %s", d, rec.Code, rec.Body.String())
		}
	}

	var got fsaControlReply
	body := s.mustAdmin(t, http.MethodPost, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls", fsaControlBody(4500), http.StatusCreated)
	if err := json.Unmarshal(body, &got); err != nil || got.MRID == "" {
		t.Fatalf("create reply %s: %v", body, err)
	}
	if len(got.Copies) != 3 {
		t.Errorf("reply copies = %+v, want e1, e2 and e3", got.Copies)
	}
	for _, d := range []string{"e1", "e2", "e3"} {
		wantOneControl(t, s, d, tmpl, got)
	}

	if rec := s.assign(t, "e4"); rec.Code != http.StatusOK {
		t.Fatalf("assign e4: %d %s", rec.Code, rec.Body.String())
	}
	wantOneControl(t, s, "e4", tmpl, got)

	s.wire.Close()
	r := bootFSAWire(t, dir, 0)
	for _, d := range []string{"e1", "e2", "e3", "e4"} {
		wantOneControl(t, r, d, tmpl, got)
	}
	var list struct {
		Controls []fsaControlReply `json:"controls"`
	}
	if err := json.Unmarshal(r.mustAdmin(t, http.MethodGet, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls", "", http.StatusOK), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Controls) != 1 || list.Controls[0].MRID != got.MRID || len(list.Controls[0].Copies) != 4 {
		t.Errorf("FSA control list after restart = %+v, want the one record held by four devices", list.Controls)
	}
}

func TestFSAControl_ThirdCopyFailingLeavesNoCopyAndNoRecord(t *testing.T) {
	dir := t.TempDir()
	s := bootFSAWire(t, dir, 0)
	tmpl := s.setupFSA(t)
	for _, d := range []string{"e1", "e2", "e3"} {
		if rec := s.assign(t, d); rec.Code != http.StatusOK {
			t.Fatalf("assign %s: %d %s", d, rec.Code, rec.Body.String())
		}
	}
	s.wire.Close()

	f := bootFSAWire(t, dir, 3)
	rec := f.call(t, http.MethodPost, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls", fsaControlBody(4500))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create with the third copy failing = %d %s, want 500", rec.Code, rec.Body.String())
	}
	for _, d := range []string{"e1", "e2", "e3"} {
		if list := f.wireControls(t, d, tmpl); list.All != 0 || len(list.DERControl) != 0 {
			t.Errorf("%s lists %d controls after the failed fan-out, want 0", d, list.All)
		}
	}
	if recs := f.admin.FSAControls(context.Background(), "fsa-1", tmpl); len(recs) != 0 {
		t.Errorf("records after the failed fan-out = %+v, want none", recs)
	}
	f.wire.Close()
	r := bootFSAWire(t, dir, 0)
	for _, d := range []string{"e1", "e2", "e3"} {
		if list := r.wireControls(t, d, tmpl); list.All != 0 {
			t.Errorf("%s lists %d controls after a restart, want 0", d, list.All)
		}
	}
}

func TestFSAControl_CancelReachesEveryCopyAndUnassignWaitsForIt(t *testing.T) {
	s := bootFSAWire(t, t.TempDir(), 0)
	tmpl := s.setupFSA(t)
	for _, d := range []string{"e1", "e2", "e3"} {
		if rec := s.assign(t, d); rec.Code != http.StatusOK {
			t.Fatalf("assign %s: %d %s", d, rec.Code, rec.Body.String())
		}
	}
	var got fsaControlReply
	if err := json.Unmarshal(s.mustAdmin(t, http.MethodPost, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls", fsaControlBody(4500), http.StatusCreated), &got); err != nil {
		t.Fatal(err)
	}

	rec := s.call(t, http.MethodDelete, "/api/devices/e2/fsa-assignment?fsaHref=/api/fsas/fsa-1", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"live_controls"`) {
		t.Fatalf("unassign with a live control = %d %s, want 409 live_controls", rec.Code, rec.Body.String())
	}
	wantOneControl(t, s, "e2", tmpl, got)

	// A copy is cancelled through the FSA, never one device at a time.
	rec = s.call(t, http.MethodPost, "/api/der/controls/"+got.MRID+"/cancel", `{}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"fsa_control"`) {
		t.Fatalf("device cancel of an FSA control = %d %s, want 409 fsa_control", rec.Code, rec.Body.String())
	}

	var cancelled fsaControlReply
	if err := json.Unmarshal(s.mustAdmin(t, http.MethodPost, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls/"+got.ID+"/cancel", `{"reason":"storm"}`, http.StatusOK), &cancelled); err != nil {
		t.Fatal(err)
	}
	if !cancelled.Cancelled || len(cancelled.Copies) != 3 {
		t.Errorf("cancel reply = %+v, want cancelled with three copies", cancelled)
	}
	for _, d := range []string{"e1", "e2", "e3"} {
		c := wantOneControl(t, s, d, tmpl, got)
		if c.EventStatus == nil || c.EventStatus.CurrentStatus != sep2.EventStatusCancelled {
			t.Errorf("%s reads status %+v, want cancelled (%d)", d, c.EventStatus, sep2.EventStatusCancelled)
		}
	}

	s.mustAdmin(t, http.MethodDelete, "/api/devices/e2/fsa-assignment?fsaHref=/api/fsas/fsa-1", "", http.StatusNoContent)
	// A cancelled control is not replicated to a device assigned later.
	if rec := s.assign(t, "e4"); rec.Code != http.StatusOK {
		t.Fatalf("assign e4: %d %s", rec.Code, rec.Body.String())
	}
	if list := s.wireControls(t, "e4", tmpl); list.All != 0 {
		t.Errorf("e4 lists %d controls, want 0: the only control is cancelled", list.All)
	}
}

// A device with its own device-only FSA, assigned to fsa-1 too, gets the
// fan-out under fsa-1 only.
func TestFSAControl_DeviceOnlyFSAGetsNoFanOut(t *testing.T) {
	s := bootFSAWire(t, t.TempDir(), 0)
	tmpl := s.setupFSA(t)
	s.mustAdmin(t, http.MethodPost, "/api/devices/e1/controls",
		fmt.Sprintf(`{"kind":"scheduled","type":"maxLimW","maxLimW":3000,"startTime":%d,"durationSeconds":600}`, sep2time.Now().Unix()+600), http.StatusCreated)
	only, ok := s.admin.DeviceOnlyFSA("e1")
	if !ok {
		t.Fatal("no device-only FSA for e1")
	}
	if rec := s.assign(t, "e1"); rec.Code != http.StatusOK {
		t.Fatalf("assign e1: %d %s", rec.Code, rec.Body.String())
	}
	var got fsaControlReply
	if err := json.Unmarshal(s.mustAdmin(t, http.MethodPost, "/api/fsas/fsa-1/der-programs/"+tmpl+"/controls", fsaControlBody(4500), http.StatusCreated), &got); err != nil {
		t.Fatal(err)
	}
	wantOneControl(t, s, "e1", tmpl, got)

	progs := getList[sep2.DERProgramList](t, s.wire, "/edev/e1/fsa/"+only+"/derp?l=255")
	if len(progs.DERProgram) != 1 {
		t.Fatalf("device-only program list = %+v", progs)
	}
	ctrls := getList[sep2.DERControlList](t, s.wire, progs.DERProgram[0].Href+"/derc?l=255")
	if ctrls.All != 1 || len(ctrls.DERControl) != 1 || ctrls.DERControl[0].MRID == got.MRID {
		t.Errorf("device-only control list = %+v, want only its own control", ctrls.DERControl)
	}

	rec := s.call(t, http.MethodPost, "/api/fsas/"+only+"/der-programs/x/controls", fsaControlBody(4500))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"fsa_device_only"`) {
		t.Errorf("FSA control on a device-only FSA = %d %s, want 409 fsa_device_only", rec.Code, rec.Body.String())
	}
}
