package handler

import (
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// A refusal code the issuer can return but issuerRefusals lacks would answer
// 500 with an unreviewed cause, so every code has a wire refusal that is a
// client error with a code and a message of its own.
func TestIssuerRefusals_CoverEveryRefusalCode(t *testing.T) {
	if len(dercontrol.RefusalCodes) == 0 {
		t.Fatal("dercontrol.RefusalCodes is empty")
	}
	for _, code := range dercontrol.RefusalCodes {
		ref, ok := issuerRefusals[code]
		if !ok {
			t.Errorf("issuerRefusals has no wire refusal for %q", code)
			continue
		}
		if ref.status < http.StatusBadRequest || ref.code == "" || ref.message == "" {
			t.Errorf("issuerRefusals[%q] = %+v, want an error status with a code and a message", code, ref)
		}
	}
	for code := range issuerRefusals {
		found := false
		for _, c := range dercontrol.RefusalCodes {
			found = found || c == code
		}
		if !found {
			t.Errorf("issuerRefusals maps %q, which dercontrol.RefusalCodes does not list", code)
		}
	}
}

// The primacy refusal text and the device path's reason text come from the
// bounds, and read as they did when the numbers were written inline.
func TestPrimacyTexts_AreFormattedFromTheConstants(t *testing.T) {
	if got, want := primacyRefusalText, "primacy: must be 0, 1, 2 or 65 to 191"; got != want {
		t.Errorf("primacyRefusalText = %q, want %q", got, want)
	}
	if userPrimacyLow != 65 || userPrimacyHigh != 191 || definedPrimacyHigh != 2 {
		t.Errorf("primacy bounds = %d, %d, %d, want 2, 65, 191", definedPrimacyHigh, userPrimacyLow, userPrimacyHigh)
	}
}
