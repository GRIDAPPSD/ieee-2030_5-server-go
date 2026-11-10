package assembly_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// dcapLinkHrefs returns every link DeviceCapability carries, by element name.
func dcapLinkHrefs(dc sep2.DeviceCapability) map[string]string {
	out := map[string]string{}
	add := func(name, href string) {
		if href != "" {
			out[name] = href
		}
	}
	if l := dc.MessagingProgramListLink; l != nil {
		add("MessagingProgramListLink", l.Href)
	}
	if l := dc.ResponseSetListLink; l != nil {
		add("ResponseSetListLink", l.Href)
	}
	if l := dc.TimeLink; l != nil {
		add("TimeLink", l.Href)
	}
	if l := dc.UsagePointListLink; l != nil {
		add("UsagePointListLink", l.Href)
	}
	if l := dc.EndDeviceListLink; l != nil {
		add("EndDeviceListLink", l.Href)
	}
	if l := dc.MirrorUsagePointListLink; l != nil {
		add("MirrorUsagePointListLink", l.Href)
	}
	if l := dc.SelfDeviceLink; l != nil {
		add("SelfDeviceLink", l.Href)
	}
	return out
}

func getDeviceCapability(t *testing.T, srv *httptest.Server, asLFDI string) sep2.DeviceCapability {
	t.Helper()
	status, raw := gateRequest(t, srv, http.MethodGet, "/dcap", asLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /dcap: status %d; body=%s", status, raw)
	}
	var dc sep2.DeviceCapability
	if err := xml.Unmarshal(raw, &dc); err != nil {
		t.Fatalf("decode DeviceCapability: %v; body=%s", err, raw)
	}
	return dc
}

// TestDeviceCapabilityLinksExactlyTheMountedFamilies clears one anchor store at
// a time and proves, against the router's own pattern list, that DeviceCapability
// links a family if and only if its GET route is mounted.
func TestDeviceCapabilityLinksExactlyTheMountedFamilies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		clear   func(*assembly.Stores)
		dropped map[string]bool
	}{
		{"everything wired", func(*assembly.Stores) {}, nil},
		{"no mirror usage points", func(s *assembly.Stores) { s.MirrorUsagePoints = nil },
			map[string]bool{"MirrorUsagePointListLink": true}},
		{"no usage points", func(s *assembly.Stores) { s.UsagePoints = nil },
			map[string]bool{"UsagePointListLink": true}},
		{"no messaging programs", func(s *assembly.Stores) { s.MessagingPrograms = nil },
			map[string]bool{"MessagingProgramListLink": true}},
		{"no response sets", func(s *assembly.Stores) { s.ResponseSets = nil },
			map[string]bool{"ResponseSetListLink": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := seededGateStores(t)
			tc.clear(stores)
			handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			links := dcapLinkHrefs(getDeviceCapability(t, srv, callerLFDI))
			for _, name := range []string{"TimeLink", "EndDeviceListLink", "SelfDeviceLink"} {
				if links[name] == "" {
					t.Errorf("%s is missing; the always-served families must stay linked", name)
				}
			}
			for name := range tc.dropped {
				if href, ok := links[name]; ok {
					t.Errorf("%s = %q is advertised although its family is not wired", name, href)
				}
			}
			if len(tc.dropped) == 0 && len(links) != 7 {
				t.Errorf("with every family wired DeviceCapability carries %d links, want 7: %v", len(links), links)
			}
			for name, href := range links {
				mounted := false
				for _, p := range patterns {
					if p == "GET "+href {
						mounted = true
					}
				}
				if !mounted {
					t.Errorf("%s links %q but no GET %s route is mounted", name, href, href)
				}
			}
		})
	}
}

// TestDeviceCapabilityAllIsPerCaller proves the all on EndDeviceListLink is the
// count GET /edev lists for the caller, not the store's total of two.
func TestDeviceCapabilityAllIsPerCaller(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	for name, lfdi := range map[string]string{"caller": callerLFDI, "victim": victimLFDI} {
		dc := getDeviceCapability(t, srv, lfdi)
		if dc.EndDeviceListLink == nil || dc.EndDeviceListLink.All != 1 {
			t.Errorf("%s: EndDeviceListLink = %+v, want all 1 (the caller's own device, store holds 2)", name, dc.EndDeviceListLink)
		}
	}

	status, raw := gateRequest(t, srv, http.MethodGet, "/dcap", "00112233445566778899AABBCCDDEEFF00112233", "")
	if status != http.StatusOK {
		t.Fatalf("stranger GET /dcap: status %d", status)
	}
	if strings.Contains(string(raw), `all="1"`) || strings.Contains(string(raw), `all="2"`) {
		t.Errorf("a caller with no device is served a count that is not its own; body=%s", raw)
	}

	_, anon := gateRequest(t, srv, http.MethodGet, "/dcap", "", "")
	var dc sep2.DeviceCapability
	if err := xml.Unmarshal(anon, &dc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dc.EndDeviceListLink == nil || dc.EndDeviceListLink.All != 0 {
		t.Errorf("no identity: EndDeviceListLink = %+v, want the link with no all", dc.EndDeviceListLink)
	}
}

// TestEndDeviceLinksServedSingletons proves an EndDevice links Configuration,
// DeviceStatus and PowerStatus exactly when each is mounted.
func TestEndDeviceLinksServedSingletons(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                          string
		clear                         func(*assembly.Stores)
		wantCfg, wantDstat, wantPower bool
	}{
		{"all wired", func(*assembly.Stores) {}, true, true, true},
		{"no configuration", func(s *assembly.Stores) { s.Configurations = nil }, false, true, true},
		{"no device status", func(s *assembly.Stores) { s.DeviceStatuses = nil }, true, false, true},
		{"no power status", func(s *assembly.Stores) { s.PowerStatuses = nil }, true, true, false},
		{"none wired", func(s *assembly.Stores) {
			s.Configurations, s.DeviceStatuses, s.PowerStatuses = nil, nil, nil
		}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := seededGateStores(t)
			tc.clear(stores)
			handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+callerID, callerLFDI, "")
			if status != http.StatusOK {
				t.Fatalf("GET /edev/%s: status %d; body=%s", callerID, status, raw)
			}
			var dev sep2.EndDevice
			if err := xml.Unmarshal(raw, &dev); err != nil {
				t.Fatalf("decode EndDevice: %v; body=%s", err, raw)
			}
			for _, l := range []struct {
				name string
				link *sep2.Link
				want bool
				href string
			}{
				{"ConfigurationLink", dev.ConfigurationLink, tc.wantCfg, "/edev/" + callerID + "/cfg"},
				{"DeviceStatusLink", dev.DeviceStatusLink, tc.wantDstat, "/edev/" + callerID + "/dstat"},
				{"PowerStatusLink", dev.PowerStatusLink, tc.wantPower, "/edev/" + callerID + "/ps"},
			} {
				switch {
				case l.want && (l.link == nil || l.link.Href != l.href):
					t.Errorf("%s = %+v, want href %q", l.name, l.link, l.href)
				case !l.want && l.link != nil:
					t.Errorf("%s = %+v, want it omitted", l.name, l.link)
				}
				if l.want {
					found := false
					for _, p := range patterns {
						found = found || p == "GET /edev/{id}/"+l.href[strings.LastIndex(l.href, "/")+1:]
					}
					if !found {
						t.Errorf("%s is advertised but its GET route is not mounted", l.name)
					}
				}
			}
		})
	}
}

// TestFSALinksDERProgramsOnlyWhenServed proves the FSA instance links its
// DERProgram list only when the DER family, which mounts it, is wired.
func TestFSALinksDERProgramsOnlyWhenServed(t *testing.T) {
	t.Parallel()

	for _, wired := range []bool{true, false} {
		stores := seededGateStores(t)
		if !wired {
			stores.DERs = nil
		}
		srv := gateServer(t, stores, gateTestPolicy())
		status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+callerID+"/fsa/f1", callerLFDI, "")
		if status != http.StatusOK {
			t.Fatalf("wired=%v: GET fsa: status %d; body=%s", wired, status, raw)
		}
		var fsa sep2.FunctionSetAssignments
		if err := xml.Unmarshal(raw, &fsa); err != nil {
			t.Fatalf("decode FSA: %v", err)
		}
		if wired && (fsa.DERProgramListLink == nil || fsa.DERProgramListLink.Href != "/edev/"+callerID+"/fsa/f1/derp") {
			t.Errorf("DER wired: DERProgramListLink = %+v, want /edev/%s/fsa/f1/derp", fsa.DERProgramListLink, callerID)
		}
		if !wired && fsa.DERProgramListLink != nil {
			t.Errorf("DER not wired: DERProgramListLink = %+v, want it omitted", fsa.DERProgramListLink)
		}
	}
}
