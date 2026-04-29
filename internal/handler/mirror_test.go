package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestHandleCreateMirrorUsagePoint(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	h := handler.HandleCreateMirrorUsagePoint(s)

	mup := sep2.MirrorUsagePoint{
		MRID:                "INV001",
		Description:         "Inverter 1",
		ServiceCategoryKind: 0,
		Status:              1,
	}
	body, _ := xml.Marshal(&mup)

	req := httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body))
	req = addIdentity(req, "TEST_SFDI_12", "TEST_LFDI_40CHARS_AABBCCDD00112233445566")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/mup/") {
		t.Errorf("Location = %q, want /mup/...", loc)
	}

	// Verify DeviceLFDI was set from cert identity
	var result sep2.MirrorUsagePoint
	_ = xml.Unmarshal(w.Body.Bytes(), &result)
	if result.DeviceLFDI != "TEST_LFDI_40CHARS_AABBCCDD00112233445566" {
		t.Errorf("DeviceLFDI = %q, want cert value", result.DeviceLFDI)
	}
}

func TestHandleMirrorUsagePointGet(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	_ = s.Create(context.Background(), "test1", sep2.MirrorUsagePoint{
		Resource: sep2.Resource{Href: "/mup/test1"},
		MRID:     "TEST1",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", handler.HandleMirrorUsagePoint(s))

	req := httptest.NewRequest(http.MethodGet, "/mup/test1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlePostMirrorMeterReading(t *testing.T) {
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	_ = mupStore.Create(context.Background(), "inv1", sep2.MirrorUsagePoint{
		Resource: sep2.Resource{Href: "/mup/inv1"},
		MRID:     "INV1",
	})

	val := int64(5000)
	uom := sep2.UomWatts
	mmr := sep2.MirrorMeterReading{
		MRID:        "MMR01",
		Description: "Active Power",
		ReadingType: &sep2.ReadingType{Uom: &uom},
		Reading:     &sep2.Reading{Value: &val},
	}
	body, _ := xml.Marshal(&mmr)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", handler.HandlePostMirrorMeterReading(mupStore, mmrStore))

	req := httptest.NewRequest(http.MethodPost, "/mup/inv1/mr", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	// Verify stored
	count, _ := mmrStore.Count(context.Background(), "inv1")
	if count != 1 {
		t.Errorf("mirror meter readings count = %d, want 1", count)
	}
}

func TestHandlePostMirrorMeterReadingNotFoundParent(t *testing.T) {
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", handler.HandlePostMirrorMeterReading(mupStore, mmrStore))

	req := httptest.NewRequest(http.MethodPost, "/mup/nonexistent/mr", bytes.NewBufferString("<MirrorMeterReading/>"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleMirrorListHandler(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	_ = s.Create(context.Background(), "a", sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/a"}, MRID: "A"})
	_ = s.Create(context.Background(), "b", sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/b"}, MRID: "B"})

	h := handler.ListHandler[sep2.MirrorUsagePoint, sep2.MirrorUsagePointList](
		s, handler.BuildMirrorUsagePointList, 300,
	)

	req := httptest.NewRequest(http.MethodGet, "/mup", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var list sep2.MirrorUsagePointList
	_ = xml.Unmarshal(w.Body.Bytes(), &list)

	if list.All != 2 {
		t.Errorf("All = %d, want 2", list.All)
	}
}

// helpers

func addIdentity(req *http.Request, sfdi, lfdi string) *http.Request {
	// Simulate identity middleware by adding TLS state and using the middleware
	// For unit tests, we inject directly into context
	identity := auth.DeviceIdentity{SFDI: sfdi, LFDI: lfdi}
	ctx := context.WithValue(req.Context(), auth.IdentityContextKey(), identity)
	return req.WithContext(ctx)
}
