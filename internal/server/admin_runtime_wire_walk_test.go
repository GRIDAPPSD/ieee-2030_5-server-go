package server_test

// #928: the runtime program chain proven on the wire. Starting with no
// fixture, only admin routes build two FSAs with a template each, a device
// program, a management pair and a control. The aggregator then walks from
// its managed EndDevice to the control, before and after a restart from the
// data dir.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

func TestRuntimeProgramsWalkedByAnAggregator(t *testing.T) {
	c := newSplitListenerCerts(t)
	manager := newManagementE2EDevice(t, c, "walk-928-manager")
	child := newManagementE2EDevice(t, c, "walk-928-child")
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
	stop := bootForTemplateTest(t, c, cfg, manager.tlsConfig)

	admin := &http.Client{Timeout: 3 * time.Second}
	adminJSON := func(method, path, body string, want int, into any) {
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
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, got, want)
		}
		if into != nil {
			if err := json.Unmarshal(got, into); err != nil {
				t.Fatalf("%s %s: decode %s: %v", method, path, got, err)
			}
		}
	}

	devHref := adminMgmtRegisterDevice(t, admin, c.adminProbe, child.sfdi, child.lfdi)
	devID := strings.TrimPrefix(devHref, "/edev/")
	adminMgmtCreatePair(t, admin, c.adminProbe, manager.lfdi, child.lfdi)

	type template struct {
		ID             string `json:"id"`
		MRID           string `json:"mRID"`
		DefaultControl struct {
			MRID string `json:"mRID"`
		} `json:"defaultControl"`
	}
	var tmplA, tmplB template
	for _, f := range []string{"fsa-a", "fsa-b"} {
		adminJSON(http.MethodPost, "/api/fsas", fmt.Sprintf(`{"description":%q,"mRID":%q}`, f, f), http.StatusCreated, nil)
	}
	adminJSON(http.MethodPost, "/api/fsas/fsa-a/der-programs",
		`{"description":"template a","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":8000}}`, http.StatusCreated, &tmplA)
	adminJSON(http.MethodPost, "/api/fsas/fsa-b/der-programs",
		`{"description":"template b","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":9000}}`, http.StatusCreated, &tmplB)
	for _, f := range []string{"fsa-a", "fsa-b"} {
		adminJSON(http.MethodPost, "/api/devices/"+devID+"/fsa-assignment", fmt.Sprintf(`{"fsaHref":"/api/fsas/%s"}`, f), http.StatusOK, nil)
	}

	var devProg struct {
		ID   string `json:"id"`
		MRID string `json:"mRID"`
	}
	adminJSON(http.MethodPost, "/api/devices/"+devID+"/fsas/fsa-b/der-programs",
		`{"description":"device program","primacy":65,"defaultControl":{"type":"maxLimW","maxLimW":7000}}`, http.StatusCreated, &devProg)

	// Edits raise each program to version 1: the template reaches the device
	// through its copy, the device program through its own route.
	adminJSON(http.MethodPut, "/api/fsas/fsa-a/der-programs/"+tmplA.ID,
		`{"description":"template a edited","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":5000}}`, http.StatusOK, nil)
	adminJSON(http.MethodPut, "/api/devices/"+devID+"/der-programs/"+devProg.ID,
		`{"description":"device program edited","primacy":65,"defaultControl":{"type":"maxLimW","maxLimW":6500}}`, http.StatusOK, nil)

	hrefA := "/edev/" + devID + "/fsa/fsa-a/derp/" + tmplA.ID
	hrefB := "/edev/" + devID + "/fsa/fsa-b/derp/" + tmplB.ID
	hrefDev := "/edev/" + devID + "/fsa/fsa-b/derp/" + devProg.ID
	var control struct {
		MRID string `json:"mRID"`
	}
	adminJSON(http.MethodPost, "/api/der/controls", fmt.Sprintf(
		`{"derProgramHref":%q,"type":"maxLimW","maxLimW":4000,"startTime":%d,"durationSeconds":300}`,
		hrefA, sep2time.Now().Unix()+600), http.StatusCreated, &control)
	if control.MRID == "" {
		t.Fatal("control has no mRID")
	}

	want := walkExpectation{
		devID: devID, hrefA: hrefA, hrefB: hrefB, hrefDev: hrefDev, controlMRID: control.MRID,
		mridA: tmplA.MRID, mridB: tmplB.MRID, mridDev: devProg.MRID,
	}
	proto := ccmHTTPClient(manager.tlsConfig, 3*time.Second)
	walkAsAggregator(t, proto, c.sep2Probe, want)

	stop()
	bootForTemplateTest(t, c, cfg, manager.tlsConfig)
	walkAsAggregator(t, proto, c.sep2Probe, want)
}

type walkExpectation struct {
	devID, hrefA, hrefB, hrefDev, controlMRID string
	mridA, mridB, mridDev                     string
}

// walkAsAggregator reads the chain the way the aggregator does, following
// links from /edev rather than building hrefs, and asserts what is served.
func walkAsAggregator(t *testing.T, proto *http.Client, addr string, w walkExpectation) {
	t.Helper()
	var dcap sep2.DeviceCapability
	protoGetXML(t, proto, addr, "/dcap", &dcap)
	if dcap.EndDeviceListLink == nil {
		t.Fatalf("DeviceCapability = %+v, want an EndDeviceListLink", dcap)
	}
	var devs sep2.EndDeviceList
	protoGetXML(t, proto, addr, dcap.EndDeviceListLink.Href, &devs)
	if len(devs.EndDevice) != 1 || devs.EndDevice[0].Href != "/edev/"+w.devID ||
		devs.EndDevice[0].FunctionSetAssignmentsListLink == nil || devs.EndDevice[0].FunctionSetAssignmentsListLink.Href != "/edev/"+w.devID+"/fsa" {
		t.Fatalf("aggregator EndDeviceList = %+v, want only /edev/%s with its FSA list link", devs.EndDevice, w.devID)
	}

	var fsas sep2.FunctionSetAssignmentsList
	protoGetXML(t, proto, addr, devs.EndDevice[0].FunctionSetAssignmentsListLink.Href, &fsas)
	listHref := map[string]string{}
	for _, f := range fsas.FunctionSetAssignments {
		if f.DERProgramListLink != nil {
			listHref[f.MRID] = f.DERProgramListLink.Href
		}
	}
	if len(fsas.FunctionSetAssignments) != 2 || listHref["fsa-a"] != "/edev/"+w.devID+"/fsa/fsa-a/derp" || listHref["fsa-b"] != "/edev/"+w.devID+"/fsa/fsa-b/derp" {
		t.Fatalf("FSA list = %+v, want fsa-a and fsa-b each with its own program list link", fsas.FunctionSetAssignments)
	}

	type served struct {
		href, mrid, description string
		primacy                 uint8
		version                 uint16
		maxLimW                 int16
	}
	controlList := map[string]string{}
	check := func(fsa string, want ...served) {
		t.Helper()
		var list sep2.DERProgramList
		protoGetXML(t, proto, addr, listHref[fsa], &list)
		if len(list.DERProgram) != len(want) {
			t.Fatalf("%s program list holds %d programs (%+v), want %d", fsa, len(list.DERProgram), list.DERProgram, len(want))
		}
		for _, wp := range want {
			controlList[wp.href] = ""
			var got *sep2.DERProgram
			for i := range list.DERProgram {
				if list.DERProgram[i].Href == wp.href {
					got = &list.DERProgram[i]
				}
			}
			if got == nil {
				t.Errorf("%s list has no program at %s: %+v", fsa, wp.href, list.DERProgram)
				continue
			}
			if got.MRID != wp.mrid || got.Description != wp.description || got.Primacy != wp.primacy || got.Version == nil || *got.Version != wp.version {
				t.Errorf("%s served %+v, want mRID %s %q primacy %d version %d", wp.href, got, wp.mrid, wp.description, wp.primacy, wp.version)
			}
			if got.DERControlListLink != nil {
				controlList[wp.href] = got.DERControlListLink.Href
			}
			if got.DefaultDERControlLink == nil {
				t.Errorf("%s serves no DefaultDERControlLink", wp.href)
				continue
			}
			var dflt sep2.DefaultDERControl
			protoGetXML(t, proto, addr, got.DefaultDERControlLink.Href, &dflt)
			if dflt.MRID == "" || dflt.DERControlBase == nil || dflt.DERControlBase.OpModMaxLimW == nil || int16(*dflt.DERControlBase.OpModMaxLimW) != wp.maxLimW {
				t.Errorf("%s default control = %+v, want a mRID and opModMaxLimW %d", wp.href, dflt, wp.maxLimW)
			}
		}
	}
	check("fsa-a", served{w.hrefA, w.mridA, "template a edited", 2, 1, 5000})
	check("fsa-b",
		served{w.hrefB, w.mridB, "template b", 1, 0, 9000},
		served{w.hrefDev, w.mridDev, "device program edited", 65, 1, 6500})

	var controls sep2.DERControlList
	protoGetXML(t, proto, addr, controlList[w.hrefA], &controls)
	var found *sep2.DERControl
	for i := range controls.DERControl {
		if controls.DERControl[i].MRID == w.controlMRID {
			found = &controls.DERControl[i]
		}
	}
	if len(controls.DERControl) != 1 || found == nil || found.DERControlBase == nil ||
		found.DERControlBase.OpModMaxLimW == nil || *found.DERControlBase.OpModMaxLimW != 4000 {
		t.Errorf("fsa-a controls = %+v, want only mRID %s with opModMaxLimW 4000", controls.DERControl, w.controlMRID)
	}
	for _, other := range []string{w.hrefB, w.hrefDev} {
		var none sep2.DERControlList
		protoGetXML(t, proto, addr, controlList[other], &none)
		if len(none.DERControl) != 0 {
			t.Errorf("%s/derc serves %+v, want no controls: the control belongs to fsa-a", other, none.DERControl)
		}
	}
}
