package assembly_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1156: a grid request from the admin route to the DERControl a device of
// each aggregator is served.

const (
	wireAggA = "A000000000000000000000000000000000000001"
	wireAggB = "B000000000000000000000000000000000000001"
	wireDevA = "A100000000000000000000000000000000000001"
	wireDevB = "B100000000000000000000000000000000000001"
)

func TestGridRequestReachesEachAggregatorsDevicesOnTheWire(t *testing.T) {
	ctx := context.Background()
	stores := testStores()
	controls, lifecycles := memory.NewDERControlStore(), dercontrol.NewLifecycleStore()
	stores.DERControls, stores.DERControlLifecycles = controls, lifecycles
	stores.CommitmentLedger = sources.NewLedger(stores.EndDevices, stores.EndDeviceManagers,
		stores.FlowReservationResponses, stores.FlowReservationResponseLifecycles, controls, lifecycles)
	devs := map[string]string{"agA": wireAggA, "agB": wireAggB, "da": wireDevA, "db": wireDevB}
	for id, lfdi := range devs {
		seedDevice(t, stores.EndDevices, id, lfdi, "")
	}
	managers := stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	for agg, dev := range map[string]string{wireAggA: wireDevA, wireAggB: wireDevB} {
		if err := managers.Assign(ctx, agg, dev); err != nil {
			t.Fatal(err)
		}
	}
	// A reports 30 kW, B 10 kW, so 20 kW splits 15 and 5.
	for dev, w := range map[string]int16{"da": 30, "db": 10} {
		der := sep2.DER{}
		der.Href = "/edev/" + dev + "/der/1"
		if err := stores.DERs.Create(ctx, dev, "1", der); err != nil {
			t.Fatal(err)
		}
		if err := stores.DERCapabilities.Create(ctx, dev+"/1", "default", sep2.DERCapability{RTGMaxW: &sep2.ActivePower{Value: w, Multiplier: 3}}); err != nil {
			t.Fatal(err)
		}
	}

	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	copies := fsaprogram.Copies{Programs: stores.DERPrograms, Defaults: stores.DefaultDERControls, Origins: origins}
	pen := fsaWirePEN
	issuer, err := dercontrol.NewIssuer(stores.DERPrograms, controls, lifecycles, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatal(err)
	}
	admin := memory.NewAdminFSAStore()
	fsaH := &handler.AdminFSAHandler{AdminFSAs: admin, DeviceFSAs: stores.FSAs, EndDevices: stores.EndDevices, Copies: &copies,
		PEN: &pen, Retirer: issuer, Managers: managers, FleetDevices: stores.EndDevices}
	ctlH := &handler.AdminDERControlHandler{Issuer: issuer, Controls: controls, Lifecycles: lifecycles,
		Programs: stores.DERPrograms, EndDevices: stores.EndDevices, Ledger: stores.CommitmentLedger, FSAControls: admin,
		Fleets: commitment.Resolver{Devices: stores.EndDevices, Managers: managers}}
	fsaCtlH := &handler.AdminFSAControlHandler{FSAs: fsaH, Controls: ctlH, Issuer: issuer}
	fsaH.Replicas = fsaCtlH
	gridH := &handler.AdminGridRequestHandler{Controls: fsaCtlH, Aggregators: managers, DERs: stores.DERs, Capabilities: stores.DERCapabilities}

	rec := httptest.NewRecorder()
	body := `{"rows":[{"direction":"discharge","x":20,"y":3,"durationSeconds":900}]}`
	gridH.HandleCreate()(rec, httptest.NewRequest(http.MethodPost, "/api/derms/grid-requests", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("grid request = %d %s", rec.Code, rec.Body.String())
	}
	var created handler.GridRequestCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	shares := created.Rows[0].Shares

	router, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	wire := httptest.NewServer(router)
	t.Cleanup(wire.Close)
	for n, want := range []struct {
		dev, agg string
		value    int16
	}{{"da", wireAggA, 15}, {"db", wireAggB, 5}} {
		s := shares[n]
		if s.AggregatorLFDI != want.agg {
			t.Fatalf("share %d is %s's, want %s's", n, s.AggregatorLFDI, want.agg)
		}
		path := "/edev/" + want.dev + "/fsa/" + s.FSA + "/derp/" + s.Program + "/derc"
		req, err := http.NewRequest(http.MethodGet, wire.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(gateIdentityHeader, want.agg)
		resp, err := wire.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s as its aggregator = %d %s %v", path, resp.StatusCode, raw, err)
		}
		var list sep2.DERControlList
		if err := xml.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.DERControl) != 1 {
			t.Fatalf("%s serves %d controls, want the one share", want.dev, len(list.DERControl))
		}
		c := list.DERControl[0]
		b := c.DERControlBase
		if c.MRID != s.Control.MRID || b == nil || b.OpModTargetW == nil || *b.OpModTargetW != (sep2.ActivePower{Value: want.value, Multiplier: 3}) ||
			b.OpModConnect == nil || !*b.OpModConnect || b.OpModEnergize == nil || !*b.OpModEnergize {
			t.Errorf("%s serves %+v base %+v, want mRID %s with target %d x 10^3, connect and energize true", want.dev, c, b, s.Control.MRID, want.value)
		}
		for _, frag := range []string{"<opModConnect>true</opModConnect>", "<opModEnergize>true</opModEnergize>", "<opModTargetW><multiplier>3</multiplier><value>"} {
			if !bytes.Contains(raw, []byte(frag)) {
				t.Errorf("%s served XML lacks %s:\n%s", want.dev, frag, raw)
			}
		}
	}
}
