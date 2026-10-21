package adminplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// #1026: GET /api/devices/{id}/default-control is a collection of the device
// read route, so it needs the admin key off loopback and answers what the
// send set.

func TestDeviceDefaultControlRoute_ServesTheSentDefaultAndRefusesWithoutCredential(t *testing.T) {
	h := buildActions(t, Config{Stores: programEditStores(t), ControlWrites: true})
	const path = "/api/devices/1/default-control"
	if r := get(h, path, true); r.Code != http.StatusNotFound {
		t.Fatalf("before any send = %d %s, want 404", r.Code, r.Body)
	}
	if r := post(h, "/api/devices/1/controls", `{"kind":"default","type":"maxLimW","maxLimW":4000}`); r.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", r.Code, r.Body)
	}
	r := get(h, path, true)
	if r.Code != http.StatusOK {
		t.Fatalf("with the key = %d %s, want 200", r.Code, r.Body)
	}
	var got struct {
		Type           string `json:"type"`
		DERControlBase struct {
			OpModMaxLimW *uint16 `json:"opModMaxLimW"`
		} `json:"derControlBase"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || got.Type != "maxLimW" || got.DERControlBase.OpModMaxLimW == nil || *got.DERControlBase.OpModMaxLimW != 4000 {
		t.Errorf("body = %s (%v), want a maxLimW 4000 default", r.Body, err)
	}
	if r := get(h, "/api/devices/nope/default-control", true); r.Code != http.StatusNotFound {
		t.Errorf("unknown device = %d, want 404", r.Code)
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.9:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("off loopback with no credential = %d, want 401", rec.Code)
	}
}
