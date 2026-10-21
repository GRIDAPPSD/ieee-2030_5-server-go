package server_test

// #922 end to end: an operator edit of a boot fixture program is served with
// a raised version, and at restart it is kept with a data dir and replaced by
// the fixture without one. A fixture program deleted with a data dir stays
// deleted.

import (
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
)

func TestEditedFixtureProgramAcrossARestart(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("data dir %v", persistent), func(t *testing.T) {
			c := newSplitListenerCerts(t)
			device := newManagementE2EDevice(t, c, fmt.Sprintf("edit-922-%v", persistent))
			fixture := filepath.Join(t.TempDir(), "edit-922.yaml")
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
    mrid: "EDIT-922-FSA"
    der_program_list_link:
      href: "/edev/0/fsa/0/derp"
      all: 1
der_programs:
  - end_device_id: "0"
    fsa_id: "0"
    id: "0"
    mrid: "0000000000000000000000000000E922"
    description: "fixture program"
    primacy: 1
    default_der_control_link: "/edev/0/fsa/0/derp/0/dderc"
    der_control_list_link:
      href: "/edev/0/fsa/0/derp/0/derc"
      all: 0
default_der_controls:
  - end_device_id: "0"
    fsa_id: "0"
    der_program_id: "0"
    mrid: "0000000000000000000000000000D922"
    der_control_base:
      op_mod_max_lim_w: 6000
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
				NotificationAllowLoopback: true,
				BootFixtureFile:           fixture,
			}
			if persistent {
				cfg.DataDir = t.TempDir()
			}
			stop := bootForTemplateTest(t, c, cfg, device.tlsConfig)
			proto := ccmHTTPClient(device.tlsConfig, readyProbeTimeout)
			admin := &http.Client{Timeout: 3 * time.Second}
			adminDo := func(method, path, body string) (int, string) {
				t.Helper()
				req, _ := http.NewRequest(method, "http://"+c.adminProbe+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+adminTestKey)
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				resp, err := admin.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				got, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return resp.StatusCode, string(got)
			}

			assertServed(t, proto, c.sep2Probe, "fixture program", 1, -1, 6000, -1)
			status, body := adminDo(http.MethodPut, "/api/devices/0/der-programs/0",
				`{"description":"operator edit","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":4000}}`)
			if status != http.StatusOK || !strings.Contains(body, `"version":1`) {
				t.Fatalf("PUT: %d %s, want 200 at version 1", status, body)
			}
			assertServed(t, proto, c.sep2Probe, "operator edit", 2, 1, 4000, 1)

			stop()
			stop = bootForTemplateTest(t, c, cfg, device.tlsConfig)
			if persistent {
				assertServed(t, proto, c.sep2Probe, "operator edit", 2, 1, 4000, 1)
			} else {
				assertServed(t, proto, c.sep2Probe, "fixture program", 1, -1, 6000, -1)
			}
			if !persistent {
				return
			}

			if status, body := adminDo(http.MethodDelete, "/api/devices/0/der-programs/0", ""); status != http.StatusNoContent {
				t.Fatalf("DELETE: %d %s", status, body)
			}
			assertProgramGone(t, proto, c.sep2Probe)
			stop()
			bootForTemplateTest(t, c, cfg, device.tlsConfig)
			assertProgramGone(t, proto, c.sep2Probe)
		})
	}
}

// assertServed reads program 0 the way the device does: from its FSA's list,
// then its default control. A version of -1 means the element is absent.
func assertServed(t *testing.T, proto *http.Client, addr, description string, primacy uint8, version, maxLimW, defaultVersion int) {
	t.Helper()
	var list sep2.DERProgramList
	raw := string(protoGetXML(t, proto, addr, "/edev/0/fsa/0/derp", &list))
	if len(list.DERProgram) != 1 {
		t.Fatalf("program list = %+v, want one program", list.DERProgram)
	}
	p := list.DERProgram[0]
	if p.Href != "/edev/0/fsa/0/derp/0" || p.MRID != "0000000000000000000000000000E922" || p.Description != description || p.Primacy != primacy {
		t.Errorf("served program = %+v, want %q primacy %d", p, description, primacy)
	}
	wantVersion := fmt.Sprintf("<version>%d</version>", version)
	if (version < 0 && strings.Contains(raw, "<version>")) || (version >= 0 && !strings.Contains(raw, wantVersion)) {
		t.Errorf("served list carries the wrong version, want %d:\n%s", version, raw)
	}
	if p.DERControlListLink == nil || p.DERControlListLink.Href != "/edev/0/fsa/0/derp/0/derc" ||
		p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != "/edev/0/fsa/0/derp/0/dderc" {
		t.Errorf("served links = %+v %+v", p.DERControlListLink, p.DefaultDERControlLink)
	}
	var d sep2.DefaultDERControl
	rawD := string(protoGetXML(t, proto, addr, "/edev/0/fsa/0/derp/0/dderc", &d))
	if d.MRID != "0000000000000000000000000000D922" || d.DERControlBase == nil || d.DERControlBase.OpModMaxLimW == nil || int(*d.DERControlBase.OpModMaxLimW) != maxLimW {
		t.Errorf("served default = %+v, want opModMaxLimW %d", d, maxLimW)
	}
	wantDV := fmt.Sprintf("<version>%d</version>", defaultVersion)
	if (defaultVersion < 0 && strings.Contains(rawD, "<version>")) || (defaultVersion >= 0 && !strings.Contains(rawD, wantDV)) {
		t.Errorf("served default carries the wrong version, want %d:\n%s", defaultVersion, rawD)
	}
}

func assertProgramGone(t *testing.T, proto *http.Client, addr string) {
	t.Helper()
	var list sep2.DERProgramList
	protoGetXML(t, proto, addr, "/edev/0/fsa/0/derp", &list)
	if len(list.DERProgram) != 0 {
		t.Errorf("program list = %+v, want empty", list.DERProgram)
	}
	if code := adminMgmtProtoGET(t, proto, "https://"+addr+"/edev/0/fsa/0/derp/0"); code != http.StatusNotFound {
		t.Errorf("device GET of the deleted program: %d, want 404", code)
	}
}
