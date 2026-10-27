package assembly_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A control stored under a manager's EndDevice counts the Responses the
// manager POSTed through /rsps for a device it manages, and no others. The
// same rule that admits the POST decides the count, so a Response from a
// device whose management ended is refused at the POST and never counted.
func TestAdminControlList_CountsResponsesAManagerSentForItsDevice(t *testing.T) {
	t.Parallel()
	stores := newManagementFleet(t).stores
	controls := memory.NewDERControlStore()
	stores.DERControls = controls
	stores.CommitmentLedger = sources.NewLedger(
		stores.EndDevices, stores.EndDeviceManagers,
		stores.FlowReservationResponses, stores.FlowReservationResponseLifecycles,
		controls, stores.DERControlLifecycles,
	)
	pen := uint32(0xA0B1)
	issuer, err := assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, &pen)
	if err != nil {
		t.Fatal(err)
	}
	ctlH := &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   controls,
		Lifecycles: stores.DERControlLifecycles,
		Programs:   stores.DERPrograms,
		EndDevices: stores.EndDevices,
		Responses:  stores.Responses,
		Managers:   stores.EndDeviceManagers,
		Fleets:     commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers},
		Ledger:     stores.CommitmentLedger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/der/controls", ctlH.HandleCreate())
	mux.HandleFunc("GET /api/der/controls", ctlH.HandleList())

	prog := sep2.DERProgram{MRID: "00000000000000000000000000000064", Primacy: 1}
	prog.Href = "/edev/" + managerID + "/fsa/0/derp/0"
	prog.DERControlListLink = &sep2.ListLink{Href: prog.Href + "/derc"}
	if err := stores.DERPrograms.Create(context.Background(), managerID, "0", prog); err != nil {
		t.Fatal(err)
	}
	admin := func(method, target, body string) (int, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec.Code, rec.Body.Bytes()
	}
	status, body := admin(http.MethodPost, "/api/der/controls", fmt.Sprintf(
		`{"derProgramHref":"%s","type":"connect","startTime":%d,"durationSeconds":300}`, prog.Href, sep2time.Now().Unix()+600))
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created struct {
		MRID string `json:"mRID"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	srv := gateServer(t, stores, gateTestPolicy())
	post := func(as, named, st string) int {
		return postResponse(t, srv.URL, as, responseBody(named, created.MRID, st))
	}
	if got := post(managerLFDI, victimLFDI, "1"); got != http.StatusCreated {
		t.Fatalf("manager for managed device: %d, want 201", got)
	}
	if got := post(managerLFDI, secondChildLFDI, "2"); got != http.StatusCreated {
		t.Fatalf("manager for second managed device: %d, want 201", got)
	}
	if got := post(managerLFDI, managerLFDI, "1"); got != http.StatusCreated {
		t.Fatalf("manager for itself: %d, want 201", got)
	}
	// The caller is managed by someone else: the POST is refused, so nothing
	// is stored to count.
	if got := post(managerLFDI, callerLFDI, "3"); got != http.StatusForbidden {
		t.Fatalf("manager for another manager's device: %d, want 403", got)
	}

	counts := func() handler.DERControlResponseCounts {
		t.Helper()
		status, body := admin(http.MethodGet, "/api/der/controls?device="+managerID, "")
		var list handler.DERControlList
		if err := json.Unmarshal(body, &list); err != nil || status != http.StatusOK || len(list.Controls) != 1 {
			t.Fatalf("list: %d %s", status, body)
		}
		return list.Controls[0].Responses
	}
	want := handler.DERControlResponseCounts{
		Total:    3,
		ByStatus: map[string]int{"1": 2, "2": 1},
		// Ordered by LFDI.
		ByDevice: []handler.DeviceResponseCounts{
			{EndDeviceLFDI: victimLFDI, Total: 1, ByStatus: map[string]int{"1": 1}},
			{EndDeviceLFDI: managerLFDI, Total: 1, ByStatus: map[string]int{"1": 1}},
			{EndDeviceLFDI: secondChildLFDI, Total: 1, ByStatus: map[string]int{"2": 1}},
		},
	}
	got := counts()
	sum := 0
	for _, e := range got.ByDevice {
		sum += e.Total
	}
	if sum != got.Total {
		t.Errorf("byDevice sums to %d, want total %d", sum, got.Total)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}

	// A device that posts for itself after its pair is removed is its own
	// sender; its Response is not the manager's to count. A Response already
	// stored for it stops counting once the pair ends.
	if err := stores.EndDeviceManagers.Unassign(context.Background(), victimLFDI); err != nil {
		t.Fatal(err)
	}
	if got := post(managerLFDI, victimLFDI, "4"); got != http.StatusForbidden {
		t.Fatalf("manager after management ended: %d, want 403", got)
	}
	want = handler.DERControlResponseCounts{
		Total:    2,
		ByStatus: map[string]int{"1": 1, "2": 1},
		ByDevice: []handler.DeviceResponseCounts{
			{EndDeviceLFDI: managerLFDI, Total: 1, ByStatus: map[string]int{"1": 1}},
			{EndDeviceLFDI: secondChildLFDI, Total: 1, ByStatus: map[string]int{"2": 1}},
		},
	}
	if got := counts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("counts after management ended = %+v, want %+v", got, want)
	}
}
