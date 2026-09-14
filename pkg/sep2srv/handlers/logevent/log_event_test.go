package logevent_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/logevent"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestHandlePostLogEvent_Created(t *testing.T) {
	t.Parallel()
	s := memory.NewScopedStore[sep2.LogEvent]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/lel", logevent.HandlePostLogEvent(s))

	evt := sep2.LogEvent{
		LogEventCode: 1,
		LogEventID:   42,
	}
	body, _ := xml.Marshal(&evt)

	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/lel", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// The Location is asserted for SHAPE, not merely for presence. It is the
	// only address the server ever gives a client for the event it just
	// created, and it has to be the WADL one (/edev/{id1}/lel/{id2},
	// sep_wadl.xml:1404); a non-empty header pointing at an undeclared path is
	// the defect this route closes, not evidence against it.
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/edev/dev1/lel/") || loc == "/edev/dev1/lel/" {
		t.Errorf("Location = %q, want an id under /edev/dev1/lel/", loc)
	}

	// The stored document carries the same href the client was handed, so the
	// instance route serves back the URI the client was told to follow.
	stored, err := s.Get(context.Background(), "dev1", strings.TrimPrefix(loc, "/edev/dev1/lel/"))
	if err != nil {
		t.Fatalf("the id in the Location does not address the stored event: %v", err)
	}
	if stored.Href != loc {
		t.Errorf("stored href = %q, want %q", stored.Href, loc)
	}
	if stored.LogEventID != 42 {
		t.Errorf("stored logEventID = %d, want 42", stored.LogEventID)
	}

	count, _ := s.Count(context.Background(), "dev1")
	if count != 1 {
		t.Errorf("stored count = %d, want 1", count)
	}
}

func TestHandlePostLogEvent_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := memory.NewScopedStore[sep2.LogEvent]()
	h := logevent.HandlePostLogEvent(s)

	req := httptest.NewRequest(http.MethodGet, "/edev/dev1/lel", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandlePostLogEvent_BadXML(t *testing.T) {
	t.Parallel()
	s := memory.NewScopedStore[sep2.LogEvent]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/lel", logevent.HandlePostLogEvent(s))

	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/lel", bytes.NewBufferString("not xml"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandlePostLogEvent_InvalidXMLDoesNotLeakDecoderDetail pins the 400 path
// convention (#360): a fixed body, decoder detail (which can quote
// attacker-supplied content) left to the operator-facing log.
func TestHandlePostLogEvent_InvalidXMLDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	s := memory.NewScopedStore[sep2.LogEvent]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/lel", logevent.HandlePostLogEvent(s))

	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/lel", strings.NewReader("<"+marker+">bar</"+marker+">"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

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

func TestBuildLogEventList(t *testing.T) {
	t.Parallel()
	items := []sep2.LogEvent{
		{LogEventCode: 1},
		{LogEventCode: 2},
	}
	result := logevent.BuildLogEventList("/edev/1/lel", store.ListResult[sep2.LogEvent]{
		Items:   items,
		All:     5,
		Results: uint32(len(items)),
	}, 900)

	if result.Href != "/edev/1/lel" {
		t.Errorf("Href = %q, want /edev/1/lel", result.Href)
	}
	if result.All != 5 {
		t.Errorf("All = %d, want 5", result.All)
	}
	if int(result.Results) != len(items) {
		t.Errorf("Results = %d, want %d", result.Results, len(items))
	}
	if len(result.LogEvent) != len(items) {
		t.Errorf("len(LogEvent) = %d, want %d", len(result.LogEvent), len(items))
	}
	if result.LogEvent[0].LogEventCode != 1 {
		t.Errorf("LogEvent[0].LogEventCode = %d, want 1", result.LogEvent[0].LogEventCode)
	}
}
