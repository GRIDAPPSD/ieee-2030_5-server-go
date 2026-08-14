package dcap_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/dcap"
)

func TestHandleDeviceCapabilityGET(t *testing.T) {
	h := dcap.HandleDeviceCapability()
	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/sep+xml" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/sep+xml")
	}

	var dc sep2.DeviceCapability
	if err := xml.Unmarshal(w.Body.Bytes(), &dc); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if dc.Href != "/dcap" {
		t.Errorf("Href = %q, want %q", dc.Href, "/dcap")
	}
	if dc.PollRate != 900 {
		t.Errorf("PollRate = %d, want %d", dc.PollRate, 900)
	}
	if dc.TimeLink == nil || dc.TimeLink.Href != "/tm" {
		t.Error("TimeLink should point to /tm")
	}
	if dc.SelfDeviceLink == nil || dc.SelfDeviceLink.Href != "/sdev" {
		t.Error("SelfDeviceLink should point to /sdev")
	}

	body := w.Body.String()
	if !strings.Contains(body, "urn:ieee:std:2030.5:ns") {
		t.Error("response should contain IEEE 2030.5 namespace")
	}
}

func TestHandleDeviceCapabilityPOST(t *testing.T) {
	h := dcap.HandleDeviceCapability()
	req := httptest.NewRequest(http.MethodPost, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleDeviceCapabilityHEAD(t *testing.T) {
	h := dcap.HandleDeviceCapability()
	req := httptest.NewRequest(http.MethodHead, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want %d", w.Code, http.StatusOK)
	}
}
