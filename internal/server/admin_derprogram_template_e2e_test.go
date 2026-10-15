package server_test

// #920 end to end, with no boot fixture: an operator creates an FSA and a
// program template, assigns a device, and issues a control on the device's
// copy. The device then reads the copy, its default control and the control
// over the protocol listener. A restart that lost the program snapshot gets
// the copy back from the template.

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
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

func TestProgramTemplateWithNoFixtureCarriesAControlToTheDevice(t *testing.T) {
	c := newSplitListenerCerts(t)
	device := newManagementE2EDevice(t, c, "template-920")
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
	}
	stop := bootForTemplateTest(t, c, cfg, device.tlsConfig)

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

	devHref := adminMgmtRegisterDevice(t, admin, c.adminProbe, device.sfdi, device.lfdi)
	devID := strings.TrimPrefix(devHref, "/edev/")

	if status, body := adminDo(http.MethodPost, "/api/fsas", `{"description":"roof","mRID":"fsa-roof"}`); status != http.StatusCreated {
		t.Fatalf("create FSA: %d %s", status, body)
	}
	status, body := adminDo(http.MethodPost, "/api/fsas/fsa-roof/der-programs",
		`{"description":"volt support","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":8000}}`)
	if status != http.StatusCreated {
		t.Fatalf("create template: %d %s", status, body)
	}
	var tmpl struct {
		ID             string   `json:"id"`
		MRID           string   `json:"mRID"`
		Copies         []string `json:"copies"`
		DefaultControl struct {
			MRID string `json:"mRID"`
		} `json:"defaultControl"`
	}
	if err := json.Unmarshal(body, &tmpl); err != nil || tmpl.ID == "" || len(tmpl.Copies) != 0 {
		t.Fatalf("template body %s: %v, want an id and no copies before assignment", body, err)
	}

	if status, body := adminDo(http.MethodPost, "/api/devices/"+devID+"/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-roof"}`); status != http.StatusOK {
		t.Fatalf("assign: %d %s", status, body)
	}

	programHref := "/edev/" + devID + "/fsa/fsa-roof/derp/" + tmpl.ID
	create := fmt.Sprintf(`{"derProgramHref":%q,"type":"maxLimW","maxLimW":6000,"startTime":%d,"durationSeconds":300}`,
		programHref, sep2time.Now().Unix()+600)
	status, body = adminDo(http.MethodPost, "/api/der/controls", create)
	if status != http.StatusCreated {
		t.Fatalf("control on the copy: %d %s", status, body)
	}
	var control struct {
		MRID string `json:"mRID"`
	}
	if err := json.Unmarshal(body, &control); err != nil || control.MRID == "" {
		t.Fatalf("control body %s: %v", body, err)
	}

	proto := ccmHTTPClient(device.tlsConfig, 3*time.Second)
	assertDeviceReadsCopy(t, proto, c.sep2Probe, devID, programHref, tmpl.MRID, tmpl.DefaultControl.MRID, control.MRID)

	// Restart having lost the program snapshot: the boot rebuild writes the
	// copy back from the persisted template and origin index.
	stop()
	if err := os.Remove(filepath.Join(cfg.DataDir, "derprograms.json")); err != nil {
		t.Fatalf("remove program snapshot: %v", err)
	}
	bootForTemplateTest(t, c, cfg, device.tlsConfig)
	assertDeviceReadsCopy(t, proto, c.sep2Probe, devID, programHref, tmpl.MRID, tmpl.DefaultControl.MRID, control.MRID)

	// Unassigning removes the copy from the wire and from the control API.
	if status, body := adminDo(http.MethodDelete, "/api/devices/"+devID+"/fsa-assignment?fsaHref=/api/fsas/fsa-roof", ""); status != http.StatusNoContent {
		t.Fatalf("unassign: %d %s", status, body)
	}
	var after sep2.DERProgramList
	protoGetXML(t, proto, c.sep2Probe, "/edev/"+devID+"/fsa/fsa-roof/derp", &after)
	if len(after.DERProgram) != 0 {
		t.Errorf("program list after unassign = %+v, want empty", after.DERProgram)
	}
	if code := adminMgmtProtoGET(t, proto, "https://"+c.sep2Probe+programHref); code != http.StatusNotFound {
		t.Errorf("device GET %s after unassign: status %d, want 404", programHref, code)
	}
	// The dderc route answers every path with an empty default when none is
	// stored, so the stored one being gone shows as no mRID and no base.
	var dflt sep2.DefaultDERControl
	protoGetXML(t, proto, c.sep2Probe, programHref+"/dderc", &dflt)
	if dflt.MRID != "" || dflt.DERControlBase != nil {
		t.Errorf("default after unassign = %+v, want the route's empty default", dflt)
	}
	if status, body := adminDo(http.MethodPost, "/api/der/controls", strings.Replace(create, "6000", "5000", 1)); status != http.StatusNotFound {
		t.Errorf("control on the removed copy: %d %s, want 404", status, body)
	}
}

// bootForTemplateTest runs the server until the returned stop is called or the
// test ends.
func bootForTemplateTest(t *testing.T, c *splitListenerCerts, cfg *config.Config, deviceTLS *gotls.Config) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx, cfg, c.svc) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	}
	t.Cleanup(stop)
	if msg, ok := waitForListenersReady(c.sep2Probe, c.adminProbe, 5*time.Second, deviceTLS, runErr); !ok {
		t.Fatal(msg)
	}
	return stop
}

func protoGetXML(t *testing.T, client *http.Client, addr, path string, into any) []byte {
	t.Helper()
	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err = client.Get("https://" + addr + path)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("device GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device GET %s: status %d body %s", path, resp.StatusCode, body)
	}
	if err := xml.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v\nbody: %s", path, err, body)
	}
	return body
}

func assertDeviceReadsCopy(t *testing.T, proto *http.Client, addr, devID, programHref, programMRID, defaultMRID, controlMRID string) {
	t.Helper()
	var fsas sep2.FunctionSetAssignmentsList
	protoGetXML(t, proto, addr, "/edev/"+devID+"/fsa", &fsas)
	found := false
	for _, f := range fsas.FunctionSetAssignments {
		if f.Href == "/edev/"+devID+"/fsa/fsa-roof" && f.DERProgramListLink != nil && f.DERProgramListLink.Href == "/edev/"+devID+"/fsa/fsa-roof/derp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("device FSA list %+v does not name fsa-roof and its program list", fsas.FunctionSetAssignments)
	}

	var programs sep2.DERProgramList
	raw := protoGetXML(t, proto, addr, "/edev/"+devID+"/fsa/fsa-roof/derp", &programs)
	if len(programs.DERProgram) != 1 {
		t.Fatalf("program list = %+v, want the one copy", programs.DERProgram)
	}
	p := programs.DERProgram[0]
	if p.Href != programHref || p.MRID != programMRID || p.Description != "volt support" || p.Primacy != 2 ||
		p.Version == nil || *p.Version != 0 || !strings.Contains(string(raw), "<version>0</version>") {
		t.Errorf("served program = %+v, want the template copy at %s with version 0\nbody: %s", p, programHref, raw)
	}
	if p.DERControlListLink == nil || p.DERControlListLink.Href != programHref+"/derc" || p.DERControlListLink.All != 1 {
		t.Errorf("DERControlListLink = %+v, want %s/derc with all 1", p.DERControlListLink, programHref)
	}
	if p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != programHref+"/dderc" {
		t.Errorf("DefaultDERControlLink = %+v, want %s/dderc", p.DefaultDERControlLink, programHref)
	}

	var dflt sep2.DefaultDERControl
	protoGetXML(t, proto, addr, programHref+"/dderc", &dflt)
	if dflt.MRID != defaultMRID || dflt.DERControlBase == nil || dflt.DERControlBase.OpModMaxLimW == nil || *dflt.DERControlBase.OpModMaxLimW != 8000 {
		t.Errorf("served default = %+v, want mRID %s opModMaxLimW 8000", dflt, defaultMRID)
	}

	var controls sep2.DERControlList
	protoGetXML(t, proto, addr, programHref+"/derc", &controls)
	if len(controls.DERControl) != 1 || controls.DERControl[0].MRID != controlMRID {
		t.Errorf("served controls = %+v, want the one issued (mRID %s)", controls.DERControl, controlMRID)
	}
}
