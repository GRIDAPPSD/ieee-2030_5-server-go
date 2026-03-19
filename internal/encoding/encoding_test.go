package encoding_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
)

type testResource struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns TestResource"`
	Name    string   `xml:"name"`
}

func TestWriteXML(t *testing.T) {
	w := httptest.NewRecorder()
	encoding.WriteXML(w, http.StatusOK, &testResource{Name: "hello"})

	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/sep+xml" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "urn:ieee:std:2030.5:ns") {
		t.Error("missing namespace in body")
	}
	if !strings.Contains(w.Body.String(), "<name>hello</name>") {
		t.Error("missing name element")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	encoding.MethodNotAllowed(w, "GET, HEAD")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d", w.Code)
	}
	if w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow = %q", w.Header().Get("Allow"))
	}
}

func TestNegotiateEncoderDefaultsToXML(t *testing.T) {
	enc := encoding.NegotiateEncoder("")
	if enc.ContentType() != "application/sep+xml" {
		t.Errorf("default encoder = %q", enc.ContentType())
	}
}

func TestNegotiateEncoderXMLExplicit(t *testing.T) {
	enc := encoding.NegotiateEncoder("application/sep+xml")
	if enc.ContentType() != "application/sep+xml" {
		t.Errorf("XML encoder = %q", enc.ContentType())
	}
}

func TestXMLEncoderRoundTrip(t *testing.T) {
	enc := encoding.NewXMLEncoder()

	data, err := enc.Marshal(&testResource{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	var parsed testResource
	if err := enc.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "test" {
		t.Errorf("Name = %q", parsed.Name)
	}
}
