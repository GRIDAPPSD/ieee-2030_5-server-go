package handler

import (
	"net/http"
	"reflect"
	"strings"
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

// Every path a control value member can take in a body has a wrong-type
// refusal, so none falls through to "invalid JSON", and each reads as it did
// when the table spelled it out.
func TestWrongTypeRefusals_CoverEveryValueMember(t *testing.T) {
	var paths []string
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			path := prefix + name
			paths = append(paths, path)
			if ft := f.Type; ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct {
				walk(path+".", ft.Elem())
			}
		}
	}
	walk("", reflect.TypeFor[dercontrol.LegacyValues]())
	want := map[string]derControlRefusal{
		"maxLimW":                  {http.StatusBadRequest, "max_lim_w_type", "maxLimW: must be an integer"},
		"powerFactor":              {http.StatusBadRequest, "power_factor_type", "powerFactor: must be an object"},
		"powerFactor.displacement": {http.StatusBadRequest, "displacement_type", "powerFactor.displacement: must be an integer"},
		"powerFactor.excitation":   {http.StatusBadRequest, "excitation_type", "powerFactor.excitation: must be a boolean"},
		"targetW":                  {http.StatusBadRequest, "target_w_type", "targetW: must be an object"},
		"targetW.value":            {http.StatusBadRequest, "target_w_value_type", "targetW.value: must be an integer"},
		"targetW.multiplier":       {http.StatusBadRequest, "target_w_multiplier_type", "targetW.multiplier: must be an integer"},
	}
	if len(paths) != len(want) {
		t.Errorf("value member paths = %v, want the %d pinned here", paths, len(want))
	}
	for _, p := range paths {
		got, ok := wrongTypeRefusals[p]
		if !ok {
			t.Errorf("%s: no wrong-type refusal", p)
			continue
		}
		if got != want[p] {
			t.Errorf("%s: %+v, want %+v", p, got, want[p])
		}
	}
}
