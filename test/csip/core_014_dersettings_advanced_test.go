// CSIP V1.2 Section 6.7 - DER Settings (advanced).
//
// CORE-014 proves that a server stores and returns DERCapability and
// DERSettings with full per-field fidelity on the V1.2 advanced
// payload shape:
//
//   - DERCapability.modesSupported includes the opModMaxLimW bit
//     of the DERControlType bitfield (V1.2 Section 10.10 Table - bit 18).
//   - DERCapability.rtgMaxW is populated.
//   - DERSettings round-trips its reactive-power (setMaxVar) and
//     power-factor (setMaxChargeRateW used as a stand-in for the
//     PF-rate ceiling - DERSettings does not carry a dedicated PF
//     field; the V1.2 procedure asserts the PF-related rates are
//     wire-clean after a PUT).
//
// Mode bit positions: the DERControlType bitfield positions are NOT
// declared as constants in pkg/sep2/der.go today. CORE-014 hardcodes
// the bit positions inline with V1.2 reference comments so the test
// stays self-contained. A follow-up that adds those constants to
// pkg/sep2/ would let this file reference them by name; flagged in
// the PR description for follow-up triage rather than fixed in scope.
//
// V1.2 procedure step -> assertion mapping (per V1.2 Section 6.7 procedure):
//
//	Step 1 (server has EndDevice)                --> fixture load single-edev.yaml
//	Step 2 (PUT DERCapability with modesSupported
//	        including opModMaxLimW, rtgMaxW)     --> putAndGetCapabilityAdvanced
//	Step 3 (GET DERCapability, assert modes bit) --> assertModesSupportedHasMaxLimW
//	Step 4 (PUT DERSettings with setMaxVar
//	        reactive-power)                      --> putAndGetSettingsReactivePower
//	Step 5 (PUT DERSettings with setMaxChargeRateW
//	        as the PF-rate ceiling)              --> putAndGetSettingsPFRate
//
// Run under both GCM and CCM cipher modes to keep the spec-cipher
// path covered (IEEE-001 regression guard surface).
package csip_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// V1.2 DERControlType bitfield positions used by CORE-014. Mirrors
// IEEE 2030.5 V1.2 Table (DERControlType) - these are NOT exported
// from pkg/sep2 today and a follow-up should add them so callers
// stop hardcoding. See PR description.
//
// Typed as sep2.DERControlType rather than uint32: core made the
// modesSupported/modesEnabled bitmap a named hexBinary32 type
// (sep.xsd:3952) so the hexBinary encoding lives in one place, and these
// constants have to be assignable to it.
const (
	derControlTypeOpModFixedPFInjectW sep2.DERControlType = 1 << 3  // bit 3
	derControlTypeOpModFixedW         sep2.DERControlType = 1 << 5  // bit 5
	derControlTypeOpModMaxLimW        sep2.DERControlType = 1 << 18 // bit 18
	derControlTypeOpModTargetW        sep2.DERControlType = 1 << 20 // bit 20
)

// TestCORE_014_DERSettingsAdvanced implements CSIP V1.2 Section 6.7.
func TestCORE_014_DERSettingsAdvanced(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name string
		opts []csiptest.BootOption
	}{
		{name: "GCM", opts: nil},
		{name: "CCM", opts: []csiptest.BootOption{csiptest.WithCCMMode()}},
	} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			runCORE014(t, mode.opts)
		})
	}
}

// runCORE014 executes the Section 6.7 procedure once against a freshly booted
// server seeded with the single-edev.yaml fixture.
func runCORE014(t *testing.T, extraOpts []csiptest.BootOption) {
	t.Helper()

	srv, client := bootCORE014(t, extraOpts)
	ctx := context.Background()

	const (
		edevID = "0"
		derID  = "0"
	)

	// Step 2 + 3: PUT DERCapability with modesSupported bitfield that
	// includes opModMaxLimW (bit 18) and a populated rtgMaxW, then GET
	// and assert wire fidelity on every populated field.
	t.Run("CapabilityAdvanced", func(t *testing.T) {
		putAndGetCapabilityAdvanced(t, ctx, srv, client, edevID, derID)
	})

	// Step 4: PUT DERSettings with a populated setMaxVar (reactive
	// power) and assert it round-trips.
	t.Run("SettingsReactivePower", func(t *testing.T) {
		putAndGetSettingsReactivePower(t, ctx, srv, client, edevID, derID)
	})

	// Step 5: PUT DERSettings with a populated setMaxChargeRateW (the
	// PF-rate ceiling per V1.2 Section 6.7) and assert it round-trips. We
	// PUT the full payload (including the bits from Step 4) because
	// the singleton-PUT handler replaces the resource - partial PUT
	// would zero out the Step 4 fields.
	t.Run("SettingsPFRate", func(t *testing.T) {
		putAndGetSettingsPFRate(t, ctx, srv, client, edevID, derID)
	})
}

// putAndGetCapabilityAdvanced PUTs a DERCapability whose modesSupported
// bitfield includes opModMaxLimW (bit 18) plus three other common bits,
// and whose rtgMaxW carries a non-zero ActivePower. GETs the resource
// back and asserts the modes bitfield carries opModMaxLimW set AND
// rtgMaxW matches what was PUT.
func putAndGetCapabilityAdvanced(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()

	modes := derControlTypeOpModMaxLimW |
		derControlTypeOpModFixedW |
		derControlTypeOpModFixedPFInjectW |
		derControlTypeOpModTargetW
	rtg := sep2.ActivePower{Multiplier: 0, Value: 7500}
	dtype := uint8(83) // PV per IEC 61970-301 DERType

	put := sep2.DERCapability{
		ModesSupported: &modes,
		RTGMaxW:        &rtg,
		Type:           &dtype,
	}

	path := derSingletonPath(edevID, derID, "dercap")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERCapability
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.ModesSupported == nil {
		t.Fatal("DERCapability.ModesSupported is nil")
	}
	if (*got.ModesSupported)&derControlTypeOpModMaxLimW == 0 {
		t.Errorf("DERCapability.ModesSupported = %#x does NOT include opModMaxLimW (bit 18, mask %#x)",
			*got.ModesSupported, derControlTypeOpModMaxLimW)
	}
	if *got.ModesSupported != modes {
		t.Errorf("DERCapability.ModesSupported = %#x, want %#x (full round-trip)",
			*got.ModesSupported, modes)
	}
	if got.RTGMaxW == nil || got.RTGMaxW.Value != rtg.Value || got.RTGMaxW.Multiplier != rtg.Multiplier {
		t.Errorf("DERCapability.RTGMaxW = %+v, want %+v", got.RTGMaxW, rtg)
	}
	if got.Type == nil || *got.Type != dtype {
		t.Errorf("DERCapability.Type = %v, want %d", got.Type, dtype)
	}
}

// putAndGetSettingsReactivePower PUTs a DERSettings with setMaxVar
// (reactive power) populated and verifies it round-trips.
func putAndGetSettingsReactivePower(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()

	modes := derControlTypeOpModMaxLimW
	maxVar := sep2.ReactivePower{Multiplier: 0, Value: 3000}
	const updatedTime int64 = 1_700_000_100

	put := sep2.DERSettings{
		ModesEnabled: &modes,
		SetMaxVar:    &maxVar,
		UpdatedTime:  updatedTime,
	}

	path := derSingletonPath(edevID, derID, "derg")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERSettings
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.ModesEnabled == nil || *got.ModesEnabled != modes {
		t.Errorf("DERSettings.ModesEnabled = %v, want %#x", got.ModesEnabled, modes)
	}
	if got.SetMaxVar == nil {
		t.Fatal("DERSettings.SetMaxVar is nil - reactive power dropped on the wire")
	}
	if got.SetMaxVar.Value != maxVar.Value || got.SetMaxVar.Multiplier != maxVar.Multiplier {
		t.Errorf("DERSettings.SetMaxVar = %+v, want %+v", got.SetMaxVar, maxVar)
	}
	if got.UpdatedTime != updatedTime {
		t.Errorf("DERSettings.UpdatedTime = %d, want %d", got.UpdatedTime, updatedTime)
	}
}

// putAndGetSettingsPFRate PUTs a DERSettings with setMaxChargeRateW
// (the PF-rate ceiling per V1.2 Section 6.7) and setMaxDischargeRateW
// populated, and verifies both round-trip. The handler replaces the
// resource on PUT, so we re-include the Step 4 fields here - the
// procedure proves the PUT does not silently corrupt orthogonal fields.
func putAndGetSettingsPFRate(t *testing.T, ctx context.Context, srv *csiptest.BootedServer, client *http.Client, edevID, derID string) {
	t.Helper()

	modes := derControlTypeOpModMaxLimW | derControlTypeOpModFixedPFInjectW
	maxVar := sep2.ReactivePower{Multiplier: 0, Value: 3000}
	chargeRate := sep2.ActivePower{Multiplier: 0, Value: 5500}
	dischargeRate := sep2.ActivePower{Multiplier: 0, Value: 6000}
	const updatedTime int64 = 1_700_000_200

	put := sep2.DERSettings{
		ModesEnabled:         &modes,
		SetMaxVar:            &maxVar,
		SetMaxChargeRateW:    &chargeRate,
		SetMaxDischargeRateW: &dischargeRate,
		UpdatedTime:          updatedTime,
	}

	path := derSingletonPath(edevID, derID, "derg")
	putXML(t, client, srv.BaseURL+path, &put)

	var got sep2.DERSettings
	getXML(t, ctx, client, srv.BaseURL+path, &got)

	if got.SetMaxChargeRateW == nil {
		t.Fatal("DERSettings.SetMaxChargeRateW is nil - PF-rate ceiling dropped on the wire")
	}
	if got.SetMaxChargeRateW.Value != chargeRate.Value || got.SetMaxChargeRateW.Multiplier != chargeRate.Multiplier {
		t.Errorf("DERSettings.SetMaxChargeRateW = %+v, want %+v", got.SetMaxChargeRateW, chargeRate)
	}
	if got.SetMaxDischargeRateW == nil {
		t.Fatal("DERSettings.SetMaxDischargeRateW is nil")
	}
	if got.SetMaxDischargeRateW.Value != dischargeRate.Value || got.SetMaxDischargeRateW.Multiplier != dischargeRate.Multiplier {
		t.Errorf("DERSettings.SetMaxDischargeRateW = %+v, want %+v",
			got.SetMaxDischargeRateW, dischargeRate)
	}
	// Reactive-power field still present after a fresh PUT.
	if got.SetMaxVar == nil || got.SetMaxVar.Value != maxVar.Value {
		t.Errorf("DERSettings.SetMaxVar = %+v after PF-rate PUT, want preserved", got.SetMaxVar)
	}
	// Modes bitfield still carries the opModMaxLimW bit alongside the
	// PF-inject bit; if it doesn't, the PUT silently corrupted the modes.
	if got.ModesEnabled == nil || *got.ModesEnabled != modes {
		t.Errorf("DERSettings.ModesEnabled = %v, want %#x", got.ModesEnabled, modes)
	}
}

// bootCORE014 loads the single-edev.yaml fixture into a fresh store
// set and returns the booted server plus an *http.Client wired with a
// device cert under our control (raw PUT/GET XML payloads need an
// http.Client outside csiptest.Client's surface).
func bootCORE014(t *testing.T, extraOpts []csiptest.BootOption) (*csiptest.BootedServer, *http.Client) {
	t.Helper()

	_, caCertFile, clientCert := mustBuildClientPKI(t)

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms,
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
