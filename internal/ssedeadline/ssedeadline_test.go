package ssedeadline

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExtend_ReportsUnsupportedWriter(t *testing.T) {
	var got error
	Extend(http.NewResponseController(httptest.NewRecorder()), time.Second, func(err error) { got = err })
	if got == nil {
		t.Fatal("onErr not called for a writer without deadline support")
	}
}

func TestExtend_SetsDeadlineOnSupportedWriter(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Extend(http.NewResponseController(w), time.Second, func(error) { called = true })
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if called {
		t.Fatal("onErr called on a writer that supports deadlines")
	}
}
