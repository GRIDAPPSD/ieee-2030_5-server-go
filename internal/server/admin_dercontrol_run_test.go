package server_test

// #566: the DER control admin API as server.Run wires it. The handler tests
// cover the behavior; this proves Run supplies the PEN, the subscription
// notifier, the Response store and persistence to it, since any of those
// could be dropped from the wiring with every handler test still green.

import (
	"bytes"
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
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

func TestRunWiresTheDERControlAdminAPI(t *testing.T) {
	c := newSplitListenerCerts(t)
	device := newManagementE2EDevice(t, c, "dercontrol-run")

	fixture := filepath.Join(t.TempDir(), "dercontrol-run.yaml")
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
    mrid: "RUN-FSA"
    der_program_list_link:
      href: "/edev/0/fsa/0/derp"
      all: 1
der_programs:
  - end_device_id: "0"
    fsa_id: "0"
    id: "0"
    mrid: "RUN-DERP"
    primacy: 0
    der_control_list_link:
      href: "/edev/0/fsa/0/derp/0/derc"
      all: 0
`, device.sfdi, device.lfdi)
	if err := os.WriteFile(fixture, []byte(fixtureYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	pen := uint32(0xA0B1)
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
	proto := ccmHTTPClient(device.tlsConfig, 3*time.Second)
	protoBase := "https://" + c.sep2Probe

	// The device subscribes to its program list, as an aggregator would.
	rcv := newLoopbackReceiver(t)
	sub := `<Subscription xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>/edev/0/fsa/0/derp</subscribedResource>` +
		`<notificationURI>` + rcv.uri + `</notificationURI><encoding>0</encoding></Subscription>`
	resp, err := proto.Post(protoBase+"/edev/0/sub", "application/sep+xml", strings.NewReader(sub))
	if err != nil {
		t.Fatal(err)
	}
	subBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe: status %d body %s", resp.StatusCode, subBody)
	}

	// PEN wiring: without it the create answers 503.
	create := fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":6000,"startTime":%d,"durationSeconds":300}`, sep2time.Now().Unix()+600)
	status, body := adminDo(http.MethodPost, "/api/der/controls", create)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, body)
	}
	var created struct {
		MRID                  string `json:"mRID"`
		NotificationAttempted bool   `json:"notificationAttempted"`
		Persisted             bool   `json:"persisted"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if !created.Persisted {
		t.Errorf("persisted = false with SEP2_DATA_DIR set, want true")
	}
	if !created.NotificationAttempted {
		t.Errorf("notificationAttempted = false, want true: Run did not give the admin API its notifier")
	}

	// Notifier wiring: the subscriber is told.
	deadline := time.Now().Add(3 * time.Second)
	for rcv.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if rcv.hits.Load() == 0 {
		t.Fatal("no notification reached the program-list subscriber after the create")
	}

	// Response store wiring: the device's response is counted.
	st := sep2.ResponseStatusEventReceived
	rspBody, err := xml.Marshal(&sep2.DERControlResponse{Response: sep2.Response{EndDeviceLFDI: device.lfdi, Status: &st, Subject: created.MRID}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = proto.Post(protoBase+"/rsps/1/rsp", "application/sep+xml", bytes.NewReader(rspBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post response: status %d", resp.StatusCode)
	}
	status, body = adminDo(http.MethodGet, "/api/der/controls?device=0", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"responses":{"total":1,"byStatus":{"1":1}}`) {
		t.Fatalf("list: status %d body %s, want the device's one response counted", status, body)
	}
}
