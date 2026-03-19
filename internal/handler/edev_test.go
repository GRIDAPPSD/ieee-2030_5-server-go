package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestHandleEndDeviceGet(t *testing.T) {
	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: "123456789012", LFDI: "AABB"}
	dev.Href = "/edev/1"
	s.Create(context.Background(), "1", dev)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(s))

	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var got sep2.EndDevice
	xml.Unmarshal(w.Body.Bytes(), &got)
	if got.SFDI != "123456789012" {
		t.Errorf("SFDI = %q", got.SFDI)
	}
}

func TestHandleEndDeviceGetNotFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(s))

	req := httptest.NewRequest(http.MethodGet, "/edev/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleCreateEndDevice(t *testing.T) {
	s := memory.NewEndDeviceStore()
	h := handler.HandleCreateEndDevice(s)

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	req = addIdentity(req, "123456789012", "AABBCCDD00112233445566778899AABBCCDDEEFF")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Error("missing Location header")
	}

	var got sep2.EndDevice
	xml.Unmarshal(w.Body.Bytes(), &got)
	if got.SFDI != "123456789012" {
		t.Errorf("SFDI = %q, want cert identity", got.SFDI)
	}
}

func TestHandleCreateEndDeviceDuplicate(t *testing.T) {
	s := memory.NewEndDeviceStore()
	h := handler.HandleCreateEndDevice(s)

	// First POST
	req1 := httptest.NewRequest(http.MethodPost, "/edev", nil)
	req1 = addIdentity(req1, "123456789012", "AABB")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)

	if w1.Code != 201 {
		t.Fatalf("first POST status = %d", w1.Code)
	}

	// Second POST with same identity — should return 200 (existing)
	req2 := httptest.NewRequest(http.MethodPost, "/edev", nil)
	req2 = addIdentity(req2, "123456789012", "AABB")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Errorf("duplicate POST status = %d, want 200", w2.Code)
	}
}

func TestHandleCreateEndDeviceNoIdentity(t *testing.T) {
	s := memory.NewEndDeviceStore()
	h := handler.HandleCreateEndDevice(s)

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Errorf("no identity status = %d, want 403", w.Code)
	}
}

func TestHandleUpdateEndDevice(t *testing.T) {
	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: "111", LFDI: "AAA"}
	dev.Href = "/edev/1"
	s.Create(context.Background(), "1", dev)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(s))

	updated := sep2.EndDevice{SFDI: "222", LFDI: "BBB"}
	body, _ := xml.Marshal(&updated)

	req := httptest.NewRequest(http.MethodPut, "/edev/1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("status = %d, want 204", w.Code)
	}

	got, _ := s.Get(context.Background(), "1")
	if got.SFDI != "222" {
		t.Errorf("SFDI after update = %q", got.SFDI)
	}
}

func TestHandleUpdateEndDeviceNotFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(s))

	body, _ := xml.Marshal(&sep2.EndDevice{})
	req := httptest.NewRequest(http.MethodPut, "/edev/missing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleDeleteEndDevice(t *testing.T) {
	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: "111"}
	s.Create(context.Background(), "1", dev)

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(s))

	req := httptest.NewRequest(http.MethodDelete, "/edev/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

func TestHandleDeleteEndDeviceNotFound(t *testing.T) {
	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(s))

	req := httptest.NewRequest(http.MethodDelete, "/edev/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleSelfDevice(t *testing.T) {
	h := handler.HandleSelfDevice("123456789012", "AABBCCDD00112233445566778899AABBCCDDEEFF")

	req := httptest.NewRequest(http.MethodGet, "/sdev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var sdev sep2.SelfDevice
	xml.Unmarshal(w.Body.Bytes(), &sdev)
	if sdev.SFDI != "123456789012" {
		t.Errorf("SFDI = %q", sdev.SFDI)
	}
	if sdev.Href != "/sdev" {
		t.Errorf("Href = %q", sdev.Href)
	}
}

func TestHandleSelfDeviceMethodNotAllowed(t *testing.T) {
	h := handler.HandleSelfDevice("", "")

	req := httptest.NewRequest(http.MethodPost, "/sdev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 405 {
		t.Errorf("POST status = %d, want 405", w.Code)
	}
}

// addIdentity helper is defined in mirror_test.go (same package)
