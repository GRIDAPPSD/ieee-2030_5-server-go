package encoding_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
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

// TestNegotiateEncoderUnknownAccept verifies that an unrecognized Accept value
// (including the removed "application/sep-exi") falls back to the XML encoder.
// EXI support was removed in IEEE-002; this is the post-removal contract.
func TestNegotiateEncoderUnknownAccept(t *testing.T) {
	cases := []string{
		"application/sep-exi",
		"text/html",
		"application/json",
		"*/*",
	}
	for _, accept := range cases {
		enc := encoding.NegotiateEncoder(accept)
		if enc == nil {
			t.Errorf("NegotiateEncoder(%q) = nil, want XML encoder", accept)
		}
		if enc != nil && enc.ContentType() != encoding.ContentTypeSEPXML {
			t.Errorf("NegotiateEncoder(%q).ContentType() = %q, want %q", accept, enc.ContentType(), encoding.ContentTypeSEPXML)
		}
	}
}

// unmarshalableType cannot be marshaled to XML (channels are not XML-serializable).
// Used to exercise the WriteXML error path.
type unmarshalableType struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Broken"`
	Ch      chan int  `xml:"ch"`
}

func TestWriteXMLMarshalError(t *testing.T) {
	w := httptest.NewRecorder()
	encoding.WriteXML(w, http.StatusOK, &unmarshalableType{Ch: make(chan int)})

	if w.Code != http.StatusInternalServerError {
		t.Errorf("marshal error should produce 500, got %d", w.Code)
	}
}

func TestGetNamespaceDefaultsTo2018(t *testing.T) {
	// GetNamespace on a plain context (no middleware) should return Namespace2018.
	mode := encoding.GetNamespace(context.Background())
	if mode != encoding.Namespace2018 {
		t.Errorf("GetNamespace on empty context = %d, want Namespace2018 (%d)", mode, encoding.Namespace2018)
	}
}

func TestGetNamespaceFromMiddlewareContext(t *testing.T) {
	// NamespaceMiddleware injects Namespace2013 into the context when the
	// client sends a 2013 Accept header; GetNamespace must recover it.
	var capturedMode encoding.NamespaceMode
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMode = encoding.GetNamespace(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrapped := encoding.NamespaceMiddleware(inner)
	req := httptest.NewRequest("GET", "/dcap", nil)
	req.Header.Set("Accept", "application/sep+xml; level=-S1")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if capturedMode != encoding.Namespace2013 {
		t.Errorf("GetNamespace inside 2013 middleware = %d, want Namespace2013 (%d)", capturedMode, encoding.Namespace2013)
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
