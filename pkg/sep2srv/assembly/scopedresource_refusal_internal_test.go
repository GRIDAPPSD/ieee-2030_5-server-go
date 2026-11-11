package assembly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A refused scope must answer 404 even when a record sits under the forged
// key "" with the requested id; a missing store hit alone cannot tell the
// refusal from a lookup that merely missed.
func TestScopedResourceHandlerAt_RefusedScopeIs404NotAForgedLookup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const id = "r1"

	rsStore := memory.NewScopedStore[sep2.ReadingSet]()
	if err := rsStore.Create(ctx, "", id, sep2.ReadingSet{}); err != nil {
		t.Fatalf("seed ReadingSet: %v", err)
	}
	rStore := memory.NewScopedStore[sep2.Reading]()
	if err := rStore.Create(ctx, "", id, sep2.Reading{}); err != nil {
		t.Fatalf("seed Reading: %v", err)
	}

	setScope := func(r *http.Request) (string, bool) {
		return coremetering.ReadingSetScopeKey(r.PathValue("uptId"), r.PathValue("mrId"))
	}
	readingScope := func(r *http.Request) (string, bool) {
		return coremetering.ReadingScopeKey(r.PathValue("uptId"), r.PathValue("mrId"), r.PathValue("rsId"))
	}

	handlers := map[string]http.HandlerFunc{
		"ReadingSet": scopedResourceHandlerAt[sep2.ReadingSet](setScope, rsStore, "rsId", nil),
		"Reading":    scopedResourceHandlerAt[sep2.Reading](readingScope, rStore, "rsId", nil),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.SetPathValue("uptId", "u1")
			r.SetPathValue("mrId", "") // empty value: the scope refuses
			r.SetPathValue("rsId", id)
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
		})
	}
}

func TestRequireParent_FailedCheckIs500NotAbsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantNext   bool
	}{
		{"parent present serves the list", nil, http.StatusOK, true},
		{"store.ErrNotFound is a 404", store.ErrNotFound, http.StatusNotFound, false},
		{"any other error is a 500", errors.New("backend down"), http.StatusInternalServerError, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := requireParent(
				func(*http.Request) error { return tc.err },
				func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusOK) },
			)
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if called != tc.wantNext {
				t.Errorf("next called = %v, want %v", called, tc.wantNext)
			}
		})
	}
}
