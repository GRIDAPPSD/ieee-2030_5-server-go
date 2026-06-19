package handler_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

func TestHandleDeviceCapabilityGET(t *testing.T) {
	h := handler.HandleDeviceCapability()
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

	var dcap sep2.DeviceCapability
	if err := xml.Unmarshal(w.Body.Bytes(), &dcap); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if dcap.Href != "/dcap" {
		t.Errorf("Href = %q, want %q", dcap.Href, "/dcap")
	}
	if dcap.PollRate != 900 {
		t.Errorf("PollRate = %d, want %d", dcap.PollRate, 900)
	}
	if dcap.TimeLink == nil || dcap.TimeLink.Href != "/tm" {
		t.Error("TimeLink should point to /tm")
	}
	if dcap.SelfDeviceLink == nil || dcap.SelfDeviceLink.Href != "/sdev" {
		t.Error("SelfDeviceLink should point to /sdev")
	}

	body := w.Body.String()
	if !strings.Contains(body, "urn:ieee:std:2030.5:ns") {
		t.Error("response should contain IEEE 2030.5 namespace")
	}
}

func TestHandleDeviceCapabilityPOST(t *testing.T) {
	h := handler.HandleDeviceCapability()
	req := httptest.NewRequest(http.MethodPost, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleDeviceCapabilityHEAD(t *testing.T) {
	h := handler.HandleDeviceCapability()
	req := httptest.NewRequest(http.MethodHead, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want %d", w.Code, http.StatusOK)
	}
}
