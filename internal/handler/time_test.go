// Wiring tests for HandleTime: verifies that the server correctly maps
// *config.Config fields onto coresep2time.TimeParams and that the core
// handler returns the expected IEEE 2030.5 Time resource.
// time.go moved to core (Phase D3a); internal/config is server-stay.
// This test stays server-side and exercises the wiring, not the internals.
package handler_test

import (
	"encoding/xml"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	coresep2time "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/handlers/sep2time"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// buildTimeHandlerFromConfig mirrors the wiring in server/router.go:
// map *config.Config fields onto coresep2time.TimeParams.
func buildTimeHandlerFromConfig(cfg *config.Config) http.HandlerFunc {
	return coresep2time.HandleTime(coresep2time.TimeParams{
		TZOffset:    cfg.TZOffset,
		DSTOffset:   cfg.DSTOffset,
		DSTStart:    cfg.DSTStart,
		DSTEnd:      cfg.DSTEnd,
		TimeQuality: cfg.TimeQuality,
	})
}

func TestHandleTimeGET(t *testing.T) {
	cfg := &config.Config{
		TZOffset:    -28800,
		DSTOffset:   3600,
		DSTStart:    1583661600,
		DSTEnd:      1583661600,
		TimeQuality: sep2.TimeQualityNTP,
	}

	h := buildTimeHandlerFromConfig(cfg)
	req := httptest.NewRequest(http.MethodGet, "/tm", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/sep+xml" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/sep+xml")
	}

	var tm sep2.Time
	if err := xml.Unmarshal(w.Body.Bytes(), &tm); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	now := time.Now().Unix()
	diff := math.Abs(float64(tm.CurrentTime - now))
	if diff > 2 {
		t.Errorf("CurrentTime %d is more than 2 seconds from now %d", tm.CurrentTime, now)
	}

	if tm.TzOffset != -28800 {
		t.Errorf("TzOffset = %d, want %d (config wiring)", tm.TzOffset, -28800)
	}
	if tm.Quality != sep2.TimeQualityNTP {
		t.Errorf("Quality = %d, want %d (config wiring)", tm.Quality, sep2.TimeQualityNTP)
	}
	if tm.Href != "/tm" {
		t.Errorf("Href = %q, want %q", tm.Href, "/tm")
	}
}

func TestHandleTimePOST(t *testing.T) {
	cfg := &config.Config{}
	h := buildTimeHandlerFromConfig(cfg)
	req := httptest.NewRequest(http.MethodPost, "/tm", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
