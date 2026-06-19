// CSIP V1.2 §6.2 — Advanced End Device.
//
// CORE-009 proves that a CSIP server stores and returns the four
// per-DER singleton resources advertised under
// /edev/{id}/der/{derId}/{dercap,derg,ders,dera} with full roundtrip
// fidelity. The test PUTs each resource in turn, then GETs each back
// and asserts the populated fields survive the wire roundtrip.
//
// Coverage:
//
//   - DERCapability (rated nameplate)   via /edev/{id}/der/{derId}/dercap
//   - DERSettings   (current setpoints) via /edev/{id}/der/{derId}/derg
//   - DERStatus     (operational state) via /edev/{id}/der/{derId}/ders
//   - DERAvailability                   via /edev/{id}/der/{derId}/dera
//
// The DER singleton handlers in internal/handler/der.go key by the
// (edev, derId) tuple from path values, so an EndDevice must exist for
// the path to be reachable through the ACL chain. We seed it from the
// single-edev.yaml fixture (IEEE-057) — same fixture BASIC-001 and the
// other §6.x tests share.
//
// V1.2 procedure step → assertion mapping (per V1.2 §6.2 procedure):
//
//	Step 1 (server registers EndDevice) ──────► fixture load: edev id "0"
//	Step 2 (PUT DERCapability)          ──────► putAndGetCapability subtest
//	Step 3 (PUT DERSettings)            ──────► putAndGetSettings subtest
//	Step 4 (PUT DERStatus)              ──────► putAndGetStatus subtest
//	Step 5 (PUT DERAvailability)        ──────► putAndGetAvailability subtest
//	Step 6 (GET each, assert roundtrip) ──────► assertions inside each subtest
//
// Run under both GCM and CCM cipher modes. IEEE-001 (server identity
// derivation under both modes) is the standing regression guard for
// the CCM path: a regression that perturbed routing or body
// serialization under CCM would surface here as a roundtrip miss.
//
// Router fix bundled in this PR: PUT /edev/{id}/der/{derId}/dera was
// missing from internal/server/router.go (only the GET was wired). The
// singleton handler already supports PUT — this was a router wiring
// oversight, not a handler gap. See PR description for details.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestCORE_009_AdvancedEndDevice implements CSIP V1.2 §6.2.
func TestCORE_009_AdvancedEndDevice(t *testing.T) {
	t.Parallel()

	// Each cipher mode boots its own server with its own in-memory
	// stores, so the GCM and CCM subtrees do not share roundtrip
	// state. Both modes exercise the same store keys (the singleton
	// parent key is the URL path `{id}/{derId}`, not the device
	// identity), so CCM verifies that the spec cipher path does not
	// perturb routing or body serialization end-to-end.
	for _, mode := range []struct {
		name string
		opts []csiptest.BootOption
	}{
		{name: "GCM", opts: nil},
		{name: "CCM", opts: []csiptest.BootOption{csiptest.WithCCMMode()}},
	} {
		mode := mode
		t.Run(mode.name+"/RoundtripAllFourResources", func(t *testing.T) {
			t.Parallel()
			runRoundtripAllFourResources(t, mode.opts)
		})
	}
}

// runRoundtripAllFourResources boots a server seeded with the
// single-edev fixture and exercises PUT-then-GET on each of the four
// DER singleton resources advertised under /edev/0/der/0/.
//
// The derId path segment is "0" — the fixture does not seed a DER
// record (single-edev.yaml carries der_list_link metadata but no DER
// children), and the singleton handler does not require one: it keys
// only on the path values `{id}/{derId}`. Tests that walk the DER
// list itself live in different CSIP procedures (CORE-010+).
func runRoundtripAllFourResources(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv, httpClient := bootWithSingleEdev(t, extraOpts)
	ctx := context.Background()

	const (
		edevID = "0"
		derID  = "0"
	)

	t.Run("DERCapability", func(t *testing.T) {
		putAndGetCapability(t, ctx, srv, httpClient, edevID, derID)
	})
	t.Run("DERSettings", func(t *testing.T) {
		putAndGetSettings(t, ctx, srv, httpClient, edevID, derID)
	})
	t.Run("DERStatus", func(t *testing.T) {
		putAndGetStatus(t, ctx, srv, httpClient, edevID, derID)
	})
	t.Run("DERAvailability", func(t *testing.T) {
		putAndGetAvailability(t, ctx, srv, httpClient, edevID, derID)
	})
}

// bootWithSingleEdev returns a BootedServer with single-edev.yaml
// applied, plus an *http.Client wired with a device cert the test
// controls. The csiptest.Client returned by srv.Client() handles GETs
// for chained-link walks, but we PUT raw XML payloads outside that
// helper's surface — so we bring our own http.Client. This is the same
// pattern CORE-001 uses (see core_001_http_request_test.go).
func bootWithSingleEdev(t *testing.T, extraOpts []csiptest.BootOption) (*csiptest.BootedServer, *http.Client) {
	t.Helper()

	// Build a CA + device cert under our control. We hand the CA path
	// to BootServer via WithClientCAsFile and the cert to WithClientCert
	// so the http.Client we build below presents an identity the server
	// validates.
	_, caCertFile, clientCert := mustBuildClientPKI(t)

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	fixture := filepath.Join("fixtures", "single-edev.yaml")
	if err := csiptest.Load(context.Background(), target, fixture); err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}

	allOpts := append([]csiptest.BootOption{
		csiptest.WithStores(stores),
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(clientCert),
	}, extraOpts...)
	srv := csiptest.BootServer(t, allOpts...)

	return srv, buildClient(t, srv.RootCA, clientCert)
}

// putAndGetCapability PUTs a DERCapability with three populated fields
// (modesSupported, rtgMaxW, type), GETs it back, and asserts every
// populated field round-tripped. The test does not assert empty fields
// — encoding/xml elides omitempty pointer fields on the wire, and the
// handler injects a non-empty Href on GET; equality against the put
// payload would therefore fail on those fields.
func putAndGetCapability(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()
	modes := uint32(0x0000_0F00)
	rtg := sep2.ActivePower{Multiplier: 0, Value: 5000}
	dtype := uint8(83) // PV per IEC 61970-301 DERType (informational only)

	put := sep2.DERCapability{
		ModesSupported: &modes,
		RTGMaxW:        &rtg,
		Type:           &dtype,
	}

	path := derSingletonPath(edevID, derID, "dercap")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERCapability
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.ModesSupported == nil || *got.ModesSupported != modes {
		t.Errorf("DERCapability.ModesSupported = %v, want %d", got.ModesSupported, modes)
	}
	if got.RTGMaxW == nil || got.RTGMaxW.Value != rtg.Value || got.RTGMaxW.Multiplier != rtg.Multiplier {
		t.Errorf("DERCapability.RTGMaxW = %+v, want %+v", got.RTGMaxW, rtg)
	}
	if got.Type == nil || *got.Type != dtype {
		t.Errorf("DERCapability.Type = %v, want %d", got.Type, dtype)
	}
}

// putAndGetSettings PUTs a DERSettings with three populated fields
// (modesEnabled, setMaxW, updatedTime).
func putAndGetSettings(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()
	modes := uint32(0x0000_0500)
	maxW := sep2.ActivePower{Multiplier: 0, Value: 4500}
	const updatedTime int64 = 1_700_000_000

	put := sep2.DERSettings{
		ModesEnabled: &modes,
		SetMaxW:      &maxW,
		UpdatedTime:  updatedTime,
	}

	path := derSingletonPath(edevID, derID, "derg")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERSettings
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.ModesEnabled == nil || *got.ModesEnabled != modes {
		t.Errorf("DERSettings.ModesEnabled = %v, want %d", got.ModesEnabled, modes)
	}
	if got.SetMaxW == nil || got.SetMaxW.Value != maxW.Value || got.SetMaxW.Multiplier != maxW.Multiplier {
		t.Errorf("DERSettings.SetMaxW = %+v, want %+v", got.SetMaxW, maxW)
	}
	if got.UpdatedTime != updatedTime {
		t.Errorf("DERSettings.UpdatedTime = %d, want %d", got.UpdatedTime, updatedTime)
	}
}

// putAndGetStatus PUTs a DERStatus populating the three sub-status
// timestamps the V1.2 procedure calls out (genConnectStatus,
// inverterStatus, operationalModeStatus) plus readingTime.
func putAndGetStatus(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()
	const readingTime int64 = 1_700_000_100
	gen := sep2.ConnectStatusType{DateTime: readingTime, Value: 0x01}            // connected
	inv := sep2.InverterStatusType{DateTime: readingTime, Value: 0x04}           // running
	opMode := sep2.OperationalModeStatusType{DateTime: readingTime, Value: 0x02} // operational mode 2

	put := sep2.DERStatus{
		GenConnectStatus:      &gen,
		InverterStatus:        &inv,
		OperationalModeStatus: &opMode,
		ReadingTime:           readingTime,
	}

	path := derSingletonPath(edevID, derID, "ders")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERStatus
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.GenConnectStatus == nil || got.GenConnectStatus.Value != gen.Value || got.GenConnectStatus.DateTime != gen.DateTime {
		t.Errorf("DERStatus.GenConnectStatus = %+v, want %+v", got.GenConnectStatus, gen)
	}
	if got.InverterStatus == nil || got.InverterStatus.Value != inv.Value || got.InverterStatus.DateTime != inv.DateTime {
		t.Errorf("DERStatus.InverterStatus = %+v, want %+v", got.InverterStatus, inv)
	}
	if got.OperationalModeStatus == nil || got.OperationalModeStatus.Value != opMode.Value || got.OperationalModeStatus.DateTime != opMode.DateTime {
		t.Errorf("DERStatus.OperationalModeStatus = %+v, want %+v", got.OperationalModeStatus, opMode)
	}
	if got.ReadingTime != readingTime {
		t.Errorf("DERStatus.ReadingTime = %d, want %d", got.ReadingTime, readingTime)
	}
}

// putAndGetAvailability PUTs a DERAvailability with three populated
// fields (availabilityDuration, statWAvail, readingTime). The PUT
// route for /dera was missing from internal/server/router.go until
// this PR; the singleton handler already supported PUT — this was a
// router wiring oversight.
func putAndGetAvailability(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()
	avail := uint32(3600)
	const readingTime int64 = 1_700_000_200
	statW := sep2.ActivePower{Multiplier: 0, Value: 4200}

	put := sep2.DERAvailability{
		AvailabilityDuration: &avail,
		StatWAvail:           &statW,
		ReadingTime:          readingTime,
	}

	path := derSingletonPath(edevID, derID, "dera")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERAvailability
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.AvailabilityDuration == nil || *got.AvailabilityDuration != avail {
		t.Errorf("DERAvailability.AvailabilityDuration = %v, want %d", got.AvailabilityDuration, avail)
	}
	if got.StatWAvail == nil || got.StatWAvail.Value != statW.Value || got.StatWAvail.Multiplier != statW.Multiplier {
		t.Errorf("DERAvailability.StatWAvail = %+v, want %+v", got.StatWAvail, statW)
	}
	if got.ReadingTime != readingTime {
		t.Errorf("DERAvailability.ReadingTime = %d, want %d", got.ReadingTime, readingTime)
	}
}

// derSingletonPath builds the absolute URL path for the DER singleton
// at suffix (one of: dercap, derg, ders, dera) under the given
// (edevID, derID) tuple.
func derSingletonPath(edevID, derID, suffix string) string {
	return "/edev/" + edevID + "/der/" + derID + "/" + suffix
}

// putXML marshals payload to XML and PUTs it to url via the test's
// http.Client. Asserts 204 No Content (the singleton handler's success
// response per internal/handler/singleton.go HandleSingletonGetPut).
func putXML(t *testing.T, client *http.Client, url string, payload any) {
	t.Helper()
	body, err := xml.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal PUT %s payload: %v", url, err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s: status = %d, want %d", url, resp.StatusCode, http.StatusNoContent)
	}
}

// getXML issues a GET against url and unmarshals the XML body into
// dest. Asserts 200 OK.
func getXML(t *testing.T, ctx context.Context, client *http.Client, url string, dest any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d", url, resp.StatusCode, http.StatusOK)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", url, err)
	}
	if err := xml.Unmarshal(raw, dest); err != nil {
		t.Fatalf("unmarshal GET %s: %v", url, err)
	}
}
