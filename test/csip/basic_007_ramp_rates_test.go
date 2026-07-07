// CSIP V1.2 §8.7 — Inverter Control: Ramp Rates.
//
// BASIC-007 is the only V1.2 BASIC procedure that targets
// DefaultDERControl directly (no DERControl event). The spec calls for
// setGradW and setSoftGradW on the DefaultDERControl per Figure 7.
// IEEE-092 added both fields to pkg/sep2.DefaultDERControl per
// IEEE 2030.5 §10.11 (Unsigned16, hundredths of percent per second).
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.7):
//
//	Step 1 (server has DERProgram + DefaultDERControl carrying
//	         the ramp parameters, NO DERControl events)         ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram →
//	         DefaultDERControl)                                  ──► basicModeWalkDefault
//	Step 3 (DefaultDERControl.SetGradW and SetSoftGradW
//	         survive wire roundtrip)                             ──► per-field assertions
//	Step 4 (DefaultDERControl.DERControlBase.RampTms also
//	         renders — per-event ramp window)                    ──► assertRampTms
package csip_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic007 fixture seeds.
const (
	basic007RampTms      uint16 = 30   // seconds
	basic007SetGradW     uint16 = 1000 // 10%/s in hundredths of percent
	basic007SetSoftGradW uint16 = 500  // 5%/s
)

// TestBASIC_007_RampRates implements CSIP V1.2 §8.7.
func TestBASIC_007_RampRates(t *testing.T) {
	t.Parallel()
	basicModeWalkDefault(t, "basic-007-ramp-rates.yaml",
		func(t *testing.T, cipher string, c *csiptest.Client, prog sep2.DERProgram, dderc sep2.DefaultDERControl) {
			t.Helper()

			if dderc.MRID != "BASIC-007-DDERC" {
				t.Errorf("[%s] DefaultDERControl.MRID = %q, want BASIC-007-DDERC",
					cipher, dderc.MRID)
			}

			// Step 3: setGradW and setSoftGradW on DefaultDERControl
			// (per IEEE 2030.5 §10.11 — device-level default ramp rates).
			if dderc.SetGradW == nil {
				t.Fatalf("[%s] DefaultDERControl.SetGradW is nil — fixture dropped", cipher)
			}
			if got := *dderc.SetGradW; got != basic007SetGradW {
				t.Errorf("[%s] DefaultDERControl.SetGradW = %d, want %d",
					cipher, got, basic007SetGradW)
			}
			if dderc.SetSoftGradW == nil {
				t.Fatalf("[%s] DefaultDERControl.SetSoftGradW is nil — fixture dropped", cipher)
			}
			if got := *dderc.SetSoftGradW; got != basic007SetSoftGradW {
				t.Errorf("[%s] DefaultDERControl.SetSoftGradW = %d, want %d",
					cipher, got, basic007SetSoftGradW)
			}

			// Step 4: per-event ramp window also renders.
			if dderc.DERControlBase == nil {
				t.Fatalf("[%s] DefaultDERControl.DERControlBase is nil", cipher)
			}
			if dderc.DERControlBase.RampTms == nil {
				t.Fatalf("[%s] DefaultDERControl.RampTms is nil — fixture dropped", cipher)
			}
			if got := *dderc.DERControlBase.RampTms; got != basic007RampTms {
				t.Errorf("[%s] RampTms = %d, want %d", cipher, got, basic007RampTms)
			}
		})
}
