package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	coremetering "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/handlers/metering"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// authLFDIProvider wraps auth.GetIdentity as a coremetering.LFDIProvider.
// Stays server-side because the auth package is server-stay; this wiring
// is exactly what the test exercises (server auth context -> core handler).
func authLFDIProvider(ctx context.Context) (string, bool) {
	id, ok := auth.GetIdentity(ctx)
	return id.LFDI, ok
}

func TestHandleCreateMirrorUsagePoint(t *testing.T) {
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	h := coremetering.HandleCreateMirrorUsagePoint(s, authLFDIProvider)

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

	// Verify DeviceLFDI was set from cert identity (wiring assertion)
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
	mux.HandleFunc("GET /mup/{id}", coremetering.HandleMirrorUsagePoint(s))

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
	mux.HandleFunc("POST /mup/{id}/mr", coremetering.HandlePostMirrorMeterReading(mupStore, mmrStore))

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
	mux.HandleFunc("POST /mup/{id}/mr", coremetering.HandlePostMirrorMeterReading(mupStore, mmrStore))

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

	// ListHandler is still in internal/handler (it is not in D2 scope).
	// Verify BuildMirrorUsagePointList from coremetering works with it.
	// This test exercises the wiring: server ListHandler + core list-builder.
	h := handler.ListHandler[sep2.MirrorUsagePoint, sep2.MirrorUsagePointList](
		s, coremetering.BuildMirrorUsagePointList, 300,
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
	// Inject device identity into context, simulating the IdentityMiddleware.
	// Stays server-side because auth.IdentityContextKey and auth.DeviceIdentity
	// are server-stay types (internal/auth).
	identity := auth.DeviceIdentity{SFDI: sfdi, LFDI: lfdi}
	ctx := context.WithValue(req.Context(), auth.IdentityContextKey(), identity)
	return req.WithContext(ctx)
}
