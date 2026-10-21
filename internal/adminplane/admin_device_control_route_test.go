package adminplane

import (
	"net/http"
	"testing"
)

// #970: POST /api/devices/{id}/controls changes what a device does, so it
// needs a real credential even from loopback, and a read-only plane does not
// mount it.

const deviceControlPattern = "POST /api/devices/{id}/controls"

func TestDeviceControlRoute_NeedsARealCredential(t *testing.T) {
	h := buildActions(t, Config{Stores: programEditStores(t), ControlWrites: true})
	if _, exempt := nonSensitiveAdminWrites[deviceControlPattern]; exempt {
		t.Fatalf("%s is on nonSensitiveAdminWrites", deviceControlPattern)
	}
	if r := post(h, "/api/devices/1/controls", `{}`, noCredential); r.Code != http.StatusUnauthorized {
		t.Errorf("loopback with no credential = %d, want 401", r.Code)
	}
	// With the key the request reaches the handler, which refuses the empty
	// body for its missing kind.
	r := post(h, "/api/devices/1/controls", `{}`)
	if r.Code != http.StatusBadRequest {
		t.Errorf("with the key and an empty body = %d %s, want 400 from the handler", r.Code, r.Body)
	}
}

func TestDeviceControlRoute_AbsentWithoutControlWritesOrOnAReadOnlyPlane(t *testing.T) {
	for name, cfg := range map[string]Config{
		"control writes off": {Stores: programEditStores(t)},
		"read-only plane":    {Stores: programEditStores(t), ControlWrites: true, ReadOnly: true},
	} {
		h := buildActions(t, cfg)
		if r := post(h, "/api/devices/1/controls", `{"kind":"default"}`); r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 404 or 405", name, r.Code)
		}
	}
}
