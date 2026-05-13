// CSIP V1.2 §8.7 — Inverter Control: Ramp Rates.
//
// BASIC-007 is the only V1.2 BASIC procedure that targets
// DefaultDERControl directly (no DERControl event). The spec calls
// for setGradW and setSoftGradW on the DefaultDERControl per Figure 7.
// Neither field exists on pkg/sep2.DefaultDERControl today — see
// IEEE-092. The fixture seeds RampTms (the closest existing field)
// on DERControlBase so the procedure's default-walk leg renders;
// the per-field assertion for setGradW/setSoftGradW is t.Skip'd
// against the follow-up ticket.
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.7):
//
//	Step 1 (server has DERProgram + DefaultDERControl carrying
//	         the ramp parameters, NO DERControl events)         ──► fixture load
//	Step 2 (client walks /dcap → /edev → /fsa → DERProgram →
//	         DefaultDERControl)                                  ──► basicModeWalkDefault
//	Step 3 (DefaultDERControl.RampTms renders — proxy for the
//	         ramp-rate-class fields that BASIC-007 would assert) ──► assertRampTms
//	Step 4 (DefaultDERControl carries setGradW and setSoftGradW) ──► t.Skip (IEEE-092)
//
// Pinned by IEEE-092 — implementation gap.
package csip_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// basic007RampTms is the fixture's RampTms value (30 s).
const basic007RampTms uint16 = 30

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
			if dderc.DERControlBase == nil {
				t.Fatalf("[%s] DefaultDERControl.DERControlBase is nil", cipher)
			}
			// Proxy assertion: RampTms is the closest existing ramp-
			// related field; assert it round-trips so the
			// DefaultDERControl wire shape is at least partially
			// regression-guarded.
			if dderc.DERControlBase.RampTms == nil {
				t.Fatalf("[%s] DefaultDERControl.RampTms is nil — fixture dropped", cipher)
			}
			if got := *dderc.DERControlBase.RampTms; got != basic007RampTms {
				t.Errorf("[%s] RampTms = %d, want %d", cipher, got, basic007RampTms)
			}

			t.Skip(formatGap("BASIC-007 (Ramp Rates)",
				"setGradW / setSoftGradW on DefaultDERControl"))
		})
}
