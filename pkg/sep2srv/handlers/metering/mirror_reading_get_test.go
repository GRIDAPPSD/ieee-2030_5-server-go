package metering_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// readingGetMux mounts the POST and GET reading routes for one caller.
func readingGetMux(
	mupStore *memory.Store[sep2.MirrorUsagePoint],
	mmrStore *memory.ScopedStore[sep2.MirrorMeterReading],
	caller string,
) *http.ServeMux {
	provider := identityProvider(caller)
	mux := http.NewServeMux()
	post := metering.HandlePostMirrorMeterReading(mupStore, mmrStore, nil, provider)
	mux.HandleFunc("POST /mup/{id}", post)
	mux.HandleFunc("POST /mup/{id}/mr", post)
	mux.HandleFunc("GET /mup/{id}/mr/{mrId}", metering.HandleMirrorMeterReading(mupStore, mmrStore, nil, provider))
	return mux
}

// TestHandleMirrorMeterReading_LocationResolves follows the Location of a
// reading POST on both POST routes and asserts the GET body is the stored record.
func TestHandleMirrorMeterReading_LocationResolves(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := readingGetMux(mupStore, mmrStore, ownerLFDI)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "MMR_1", 4242))))
			if w.Code != http.StatusCreated {
				t.Fatalf("POST %s status = %d, want 201", path, w.Code)
			}
			loc := w.Header().Get("Location")
			if !strings.HasPrefix(loc, "/mup/device-a/mr/") {
				t.Fatalf("Location = %q, want /mup/device-a/mr/<id>", loc)
			}

			g := httptest.NewRecorder()
			mux.ServeHTTP(g, httptest.NewRequest(http.MethodGet, loc, nil))
			if g.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body = %s", loc, g.Code, g.Body.String())
			}
			var got sep2.MirrorMeterReading
			if err := xml.Unmarshal(g.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal GET body: %v", err)
			}
			if got.Href != loc {
				t.Errorf("GET body href = %q, want %q", got.Href, loc)
			}
			if got.MRID != "MMR_1" {
				t.Errorf("GET body mRID = %q, want MMR_1", got.MRID)
			}
			if got.Reading == nil || got.Reading.Value == nil || *got.Reading.Value != 4242 {
				t.Errorf("GET body Reading = %+v, want value 4242", got.Reading)
			}
			if got.LastUpdateTime == 0 {
				t.Error("GET body lastUpdateTime = 0, want the server-stamped time")
			}
		})
	}
}

// TestHandleMirrorMeterReading_OwnerScoped asserts another device is denied
// with the same 403 as the mirror itself, whether or not the reading exists.
func TestHandleMirrorMeterReading_OwnerScoped(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)

	w := httptest.NewRecorder()
	readingGetMux(mupStore, mmrStore, ownerLFDI).ServeHTTP(w,
		httptest.NewRequest(http.MethodPost, "/mup/device-a", bytes.NewReader(readingBody(t, "MMR_1", 1))))
	loc := w.Header().Get("Location")
	if w.Code != http.StatusCreated || loc == "" {
		t.Fatalf("seed POST status = %d Location = %q", w.Code, loc)
	}

	other := readingGetMux(mupStore, mmrStore, "DEVICE_B_LFDI")
	for _, target := range []string{loc, "/mup/device-a/mr/00000000000000000000"} {
		g := httptest.NewRecorder()
		other.ServeHTTP(g, httptest.NewRequest(http.MethodGet, target, nil))
		if g.Code != http.StatusForbidden {
			t.Errorf("GET %s as another device: status = %d, want 403", target, g.Code)
		}
		if strings.Contains(g.Body.String(), "MMR_1") {
			t.Errorf("denial body leaks the reading: %q", g.Body.String())
		}
	}
}

// TestHandleMirrorMeterReading_Misses covers an unknown reading, an unknown
// parent, and a reading stored under a different parent.
func TestHandleMirrorMeterReading_Misses(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
	seedOwnedMirror(t, mupStore, "device-a2", "DEVICE_A2", ownerLFDI)
	if err := mmrStore.Create(context.Background(), "device-a2", "R1", sep2.MirrorMeterReading{}); err != nil {
		t.Fatalf("seed reading: %v", err)
	}
	mux := readingGetMux(mupStore, mmrStore, ownerLFDI)

	for _, target := range []string{
		"/mup/device-a/mr/NOPE",
		"/mup/nope/mr/R1",
		"/mup/device-a/mr/R1",
	} {
		g := httptest.NewRecorder()
		mux.ServeHTTP(g, httptest.NewRequest(http.MethodGet, target, nil))
		if g.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", target, g.Code)
		}
	}
}
