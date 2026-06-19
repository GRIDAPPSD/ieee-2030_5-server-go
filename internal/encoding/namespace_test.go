package encoding_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

func TestDetectNamespace2013(t *testing.T) {
	req := httptest.NewRequest("GET", "/dcap", nil)
	req.Header.Set("Accept", "application/sep+xml; level=-S1")

	mode := encoding.DetectNamespace(req)
	if mode != encoding.Namespace2013 {
		t.Errorf("S1 header should detect 2013, got %d", mode)
	}
}

func TestDetectNamespace2018Default(t *testing.T) {
	req := httptest.NewRequest("GET", "/dcap", nil)

	mode := encoding.DetectNamespace(req)
	if mode != encoding.Namespace2018 {
		t.Errorf("no header should default to 2018, got %d", mode)
	}
}

func TestDetectNamespace2018Explicit(t *testing.T) {
	req := httptest.NewRequest("GET", "/dcap", nil)
	req.Header.Set("Accept", "application/sep+xml")

	mode := encoding.DetectNamespace(req)
	if mode != encoding.Namespace2018 {
		t.Errorf("plain accept should be 2018, got %d", mode)
	}
}

func TestRewriteNamespaceTo2013(t *testing.T) {
	data := []byte(`<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap"/>`)
	rewritten := encoding.RewriteNamespace(data, encoding.Namespace2013)

	if !bytes.Contains(rewritten, []byte("http://ieee.org/2030.5")) {
		t.Errorf("should contain 2013 namespace, got: %s", rewritten)
	}
	if bytes.Contains(rewritten, []byte("urn:ieee:std:2030.5:ns")) {
		t.Error("should NOT contain 2018 namespace after rewrite")
	}
}

func TestRewriteNamespace2018NoOp(t *testing.T) {
	data := []byte(`<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap"/>`)
	rewritten := encoding.RewriteNamespace(data, encoding.Namespace2018)

	if !bytes.Equal(data, rewritten) {
		t.Error("2018 mode should not modify data")
	}
}

func TestNamespaceMiddleware2013(t *testing.T) {
	// Handler that writes XML with 2018 namespace
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding.WriteXML(w, 200, &sep2.Time{
			Resource:    sep2.Resource{Href: "/tm"},
			CurrentTime: 1000,
			Quality:     7,
		})
	})

	wrapped := encoding.NamespaceMiddleware(inner)

	req := httptest.NewRequest("GET", "/tm", nil)
	req.Header.Set("Accept", "application/sep+xml; level=-S1")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "http://ieee.org/2030.5") {
		t.Errorf("2013 client should get 2013 namespace, got: %s", body)
	}
	if strings.Contains(body, "urn:ieee:std:2030.5:ns") {
		t.Errorf("2013 client should NOT get 2018 namespace")
	}
}

func TestNamespaceMiddleware2013NoExplicitWriteHeader(t *testing.T) {
	// Inner handler writes body without calling WriteHeader explicitly.
	// nsBufferedWriter.flush() must default w.status to 200 when it is zero.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately no WriteHeader call — exercises the w.status==0 default branch.
		if _, err := w.Write([]byte(`<T xmlns="urn:ieee:std:2030.5:ns"/>`)); err != nil {
			t.Errorf("inner Write: %v", err)
		}
	})

	wrapped := encoding.NamespaceMiddleware(inner)
	req := httptest.NewRequest("GET", "/tm", nil)
	req.Header.Set("Accept", "application/sep+xml; level=-S1")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("flush default status should be 200, got %d", w.Code)
	}
}

func TestNamespaceMiddleware2018PassThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding.WriteXML(w, 200, &sep2.Time{
			Resource:    sep2.Resource{Href: "/tm"},
			CurrentTime: 1000,
			Quality:     7,
		})
	})

	wrapped := encoding.NamespaceMiddleware(inner)

	req := httptest.NewRequest("GET", "/tm", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "urn:ieee:std:2030.5:ns") {
		t.Errorf("2018 client should get 2018 namespace, got: %s", body)
	}
}
