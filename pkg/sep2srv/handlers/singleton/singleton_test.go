package singleton_test

import (
	"bytes"
	"encoding/xml"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestSingletonGetReturnsDefault(t *testing.T) {
	s := memory.NewScopedStore[sep2.DERCapability]()

	h := singleton.HandleSingletonGetPut[sep2.DERCapability](
		s,
		func(r *http.Request) string { return "edev1/der1" },
		func(r *http.Request) sep2.DERCapability {
			return sep2.DERCapability{Resource: sep2.Resource{Href: "/edev/1/der/1/dercap"}}
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/edev/1/der/1/dercap", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (default)", w.Code)
	}

	var cap sep2.DERCapability
	_ = xml.Unmarshal(w.Body.Bytes(), &cap)
	if cap.Href != "/edev/1/der/1/dercap" {
		t.Errorf("default href = %q", cap.Href)
	}
}

func TestSingletonPutThenGet(t *testing.T) {
	s := memory.NewScopedStore[sep2.DERSettings]()

	h := singleton.HandleSingletonGetPut[sep2.DERSettings](
		s,
		func(r *http.Request) string { return "edev1/der1" },
		func(r *http.Request) sep2.DERSettings {
			return sep2.DERSettings{}
		},
	)

	// PUT settings
	maxW := sep2.ActivePower{Value: 5000}
	settings := sep2.DERSettings{SetMaxW: &maxW, UpdatedTime: 1604963587}
	body, _ := xml.Marshal(&settings)

	putReq := httptest.NewRequest(http.MethodPut, "/edev/1/der/1/derg", bytes.NewReader(body))
	putW := httptest.NewRecorder()
	h.ServeHTTP(putW, putReq)

	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putW.Code)
	}

	// GET should return the stored value
	getReq := httptest.NewRequest(http.MethodGet, "/edev/1/der/1/derg", nil)
	getW := httptest.NewRecorder()
	h.ServeHTTP(getW, getReq)

	if getW.Code != 200 {
		t.Fatalf("GET status = %d", getW.Code)
	}

	var got sep2.DERSettings
	_ = xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.SetMaxW == nil || got.SetMaxW.Value != 5000 {
		t.Errorf("SetMaxW = %v, want 5000", got.SetMaxW)
	}
}

func TestSingletonPutUpdateExisting(t *testing.T) {
	s := memory.NewScopedStore[sep2.DERStatus]()

	h := singleton.HandleSingletonGetPut[sep2.DERStatus](
		s,
		func(r *http.Request) string { return "edev1/der1" },
		func(r *http.Request) sep2.DERStatus { return sep2.DERStatus{} },
	)

	// First PUT
	s1 := sep2.DERStatus{ReadingTime: 100}
	body1, _ := xml.Marshal(&s1)
	req1 := httptest.NewRequest(http.MethodPut, "/path", bytes.NewReader(body1))
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)

	if w1.Code != 204 {
		t.Fatalf("first PUT status = %d", w1.Code)
	}

	// Second PUT (update)
	s2 := sep2.DERStatus{ReadingTime: 200}
	body2, _ := xml.Marshal(&s2)
	req2 := httptest.NewRequest(http.MethodPut, "/path", bytes.NewReader(body2))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)

	if w2.Code != 204 {
		t.Fatalf("second PUT status = %d", w2.Code)
	}

	// Verify updated value
	getReq := httptest.NewRequest(http.MethodGet, "/path", nil)
	getW := httptest.NewRecorder()
	h.ServeHTTP(getW, getReq)

	var got sep2.DERStatus
	_ = xml.Unmarshal(getW.Body.Bytes(), &got)
	if got.ReadingTime != 200 {
		t.Errorf("ReadingTime = %d, want 200 (updated)", got.ReadingTime)
	}
}

func TestSingletonPutInvalidXML(t *testing.T) {
	s := memory.NewScopedStore[sep2.DERCapability]()

	h := singleton.HandleSingletonGetPut[sep2.DERCapability](
		s,
		func(r *http.Request) string { return "key" },
		func(r *http.Request) sep2.DERCapability { return sep2.DERCapability{} },
	)

	req := httptest.NewRequest(http.MethodPut, "/path", bytes.NewBufferString("{bad xml"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestSingletonPutInvalidXMLDoesNotLeakDecoderDetail pins the 400 path
// convention (#360): a fixed body, decoder detail (which can quote
// attacker-supplied content) left to the operator-facing log.
func TestSingletonPutInvalidXMLDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	s := memory.NewScopedStore[sep2.DERCapability]()
	h := singleton.HandleSingletonGetPut[sep2.DERCapability](
		s,
		func(r *http.Request) string { return "key" },
		func(r *http.Request) sep2.DERCapability { return sep2.DERCapability{} },
	)

	req := httptest.NewRequest(http.MethodPut, "/path", strings.NewReader("<"+marker+">bar</"+marker+">"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", body)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

func TestSingletonMethodNotAllowed(t *testing.T) {
	s := memory.NewScopedStore[sep2.DERCapability]()

	h := singleton.HandleSingletonGetPut[sep2.DERCapability](
		s,
		func(r *http.Request) string { return "key" },
		func(r *http.Request) sep2.DERCapability { return sep2.DERCapability{} },
	)

	req := httptest.NewRequest(http.MethodDelete, "/path", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
