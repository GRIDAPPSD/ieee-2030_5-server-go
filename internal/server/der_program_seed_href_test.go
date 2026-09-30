package server_test

// #743: a seeded DERProgram's href must carry the same FSA-segment shape a
// runtime-created one does, so the admin DER control API accepts a control
// creation on the href it just listed. Proven end to end: seed one device,
// one FSA and one program; list it through the admin API; create a control
// on the listed href; then read it back through the device's own protocol
// FSA walk.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

func TestSeededDERProgramHrefAcceptsAdminControlAndDeviceWalksToIt(t *testing.T) {
	c := newSplitListenerCerts(t)
	device := newManagementE2EDevice(t, c, "seed-href-743")

	fixture := filepath.Join(t.TempDir(), "seed-href-743.yaml")
	fixtureYAML := fmt.Sprintf(`end_devices:
  - id: "0"
    sfdi: %q
    lfdi: %q
    enabled: true
    function_set_assignments_list_link:
      href: "/edev/0/fsa"
      all: 1
fsas:
  - end_device_id: "0"
    id: "0"
    mrid: "SEED-743-FSA"
    der_program_list_link:
      href: "/edev/0/fsa/0/derp"
      all: 1
der_programs:
  - end_device_id: "0"
    fsa_id: "0"
    id: "0"
    mrid: "SEED-743-DERP"
    primacy: 0
    der_control_list_link:
      href: "/edev/0/fsa/0/derp/0/derc"
      all: 0
`, device.sfdi, device.lfdi)
	if err := os.WriteFile(fixture, []byte(fixtureYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	pen := uint32(0xB1C2)
	cfg := &config.Config{
		Addr:                      c.sep2Probe,
		CertFile:                  c.certFile,
		KeyFile:                   c.keyFile,
		CAFile:                    c.caFile,
		AdminListen:               c.adminProbe,
		AdminKey:                  adminTestKey,
		TZOffset:                  -28800,
		TimeQuality:               sep2.TimeQualityNTP,
		PEN:                       &pen,
		DataDir:                   t.TempDir(),
		NotificationAllowLoopback: true,
		BootFixtureFile:           fixture,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})
	if !waitForServerReady(c.sep2Probe, 5*time.Second, device.tlsConfig) {
		t.Fatal("SEP2 listener never became ready")
	}

	admin := &http.Client{Timeout: 3 * time.Second}
	adminDo := func(method, path, body string) (int, []byte) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			req, _ := http.NewRequest(method, "http://"+c.adminProbe+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+adminTestKey)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := admin.Do(req)
			if err != nil {
				if time.Now().After(deadline) {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				time.Sleep(25 * time.Millisecond)
				continue
			}
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode, got
		}
	}

	// List the seeded program through the admin API, exactly as an operator
	// would before dispatching to it.
	status, body := adminDo(http.MethodGet, "/api/devices/0/der-programs", "")
	if status != http.StatusOK {
		t.Fatalf("list programs: status %d body %s", status, body)
	}
	var list handler.DERProgramListView
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode program list: %v", err)
	}
	if len(list.Programs) != 1 {
		t.Fatalf("programs = %d, want 1", len(list.Programs))
	}
	listedHref := list.Programs[0].Href
	// This is the shape pkg/sep2srv/handlers/der.DERProgramHref builds for a
	// runtime-created program; the admin create path only accepts a
	// derProgramHref in this shape.
	if want := "/edev/0/fsa/0/derp/0"; listedHref != want {
		t.Fatalf("seeded program's listed href = %q, want %q (the runtime shape)", listedHref, want)
	}

	// Create a control through the admin API on exactly the href the list
	// handed back, not a hand-built one.
	create := fmt.Sprintf(`{"derProgramHref":%q,"type":"maxLimW","maxLimW":6000,"startTime":%d,"durationSeconds":300}`,
		listedHref, sep2time.Now().Unix()+600)
	status, body = adminDo(http.MethodPost, "/api/der/controls", create)
	if status != http.StatusCreated {
		t.Fatalf("create control on listed href: status %d body %s", status, body)
	}
	var created struct {
		MRID string `json:"mRID"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	// The device walks its own FSA to the program's control list and finds
	// the control the admin API just created there.
	proto := ccmHTTPClient(device.tlsConfig, 3*time.Second)
	resp, err := proto.Get("https://" + c.sep2Probe + "/edev/0/fsa/0/derp/0/derc")
	if err != nil {
		t.Fatalf("device GET derc list: %v", err)
	}
	walkBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device GET derc list: status %d body %s", resp.StatusCode, walkBody)
	}
	var derc sep2.DERControlList
	if err := xml.Unmarshal(walkBody, &derc); err != nil {
		t.Fatalf("decode DERControlList: %v\nbody: %s", err, walkBody)
	}
	if len(derc.DERControl) != 1 || derc.DERControl[0].MRID != created.MRID {
		t.Errorf("device's derc walk = %+v, want exactly the control created on the listed href (MRID %s)", derc.DERControl, created.MRID)
	}
}
