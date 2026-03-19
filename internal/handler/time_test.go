package handler_test

import (
	"encoding/xml"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

func TestHandleTimeGET(t *testing.T) {
	cfg := &config.Config{
		TZOffset:    -28800,
		DSTOffset:   3600,
		DSTStart:    1583661600,
		DSTEnd:      1583661600,
		TimeQuality: sep2.TimeQualityNTP,
	}

	h := handler.HandleTime(cfg)
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
		t.Errorf("TzOffset = %d, want %d", tm.TzOffset, -28800)
	}
	if tm.Quality != sep2.TimeQualityNTP {
		t.Errorf("Quality = %d, want %d", tm.Quality, sep2.TimeQualityNTP)
	}
	if tm.Href != "/tm" {
		t.Errorf("Href = %q, want %q", tm.Href, "/tm")
	}
}

func TestHandleTimePOST(t *testing.T) {
	cfg := &config.Config{}
	h := handler.HandleTime(cfg)
	req := httptest.NewRequest(http.MethodPost, "/tm", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
