package assembly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The declared method set on scopedResourceHandler.
//
// # Why these assertions are at the handler and not only at the route
//
// http.ServeMux answers a method it has no pattern for with its own 405, so a
// request with an unregistered method never reaches the handler through
// BuildProtocolRouter, and a route-level test cannot tell the handler's gate
// apart from the mux's. Both layers matter and they check different things:
//
//   - the mux's 405 is what a client sees today, and the route-level tests in
//     assembly_test.go pin that;
//   - the handler's gate is what stops a FUTURE registration from silently
//     enabling a write. Mount a Put:false handler on a "PUT ..." pattern and the
//     mux is satisfied while the handler still refuses. That is fail-closed
//     behaviour with no test coverage from the route level at all, which is why
//     it is tested here, directly, against the unexported handler.
//
// The equivalence this file exists to pin: an empty itemMethods must behave
// exactly as the hardcoded "GET, HEAD" the read-only handler carried before
// the method set became declarative (the DERProgram member route).

func TestItemMethodsAllow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		methods itemMethods
		want    string
	}{
		{"the zero value is read-only", itemMethods{}, "GET, HEAD"},
		{"Put widens the set by exactly one verb", itemMethods{Put: true}, "GET, HEAD, PUT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.methods.allow(); got != tc.want {
				t.Errorf("allow() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestScopedResourceHandler_MethodSetGatesTheWrite drives the handler directly,
// bypassing the mux, so the gate itself is what answers.
func TestScopedResourceHandler_MethodSetGatesTheWrite(t *testing.T) {
	t.Parallel()

	const parentID, id = "e1", "p1"
	const path = "/edev/e1/fsa/f1/derp/p1"

	tests := []struct {
		name       string
		methods    itemMethods
		wantStatus int
		wantAllow  string
		wantStored bool
	}{
		{
			name:       "a read-only handler refuses PUT and stores nothing",
			methods:    itemMethods{},
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  "GET, HEAD",
			wantStored: false,
		},
		{
			name:       "a handler declared writable accepts PUT",
			methods:    itemMethods{Put: true},
			wantStatus: http.StatusNoContent,
			wantAllow:  "",
			wantStored: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scoped := memory.NewScopedStore[sep2.DERProgram]()
			h := scopedResourceHandler[sep2.DERProgram](scoped, "id", "derpId", tc.methods, nil)

			r := httptest.NewRequest(http.MethodPut, path,
				strings.NewReader(`<DERProgram xmlns="urn:ieee:std:2030.5:ns"/>`))
			r.SetPathValue("id", parentID)
			r.SetPathValue("derpId", id)
			w := httptest.NewRecorder()
			h(w, r)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := w.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}

			_, err := scoped.Get(context.Background(), parentID, id)
			if stored := err == nil; stored != tc.wantStored {
				t.Errorf("resource stored = %v, want %v (err=%v)", stored, tc.wantStored, err)
			}
		})
	}
}

// TestScopedResourceHandler_ReadPathIsUnchangedByTheMethodSet asserts the gate
// touches writes only: a read-only handler and a writable one serve GET
// identically, so declaring PUT cannot quietly change what a reader sees.
func TestScopedResourceHandler_ReadPathIsUnchangedByTheMethodSet(t *testing.T) {
	t.Parallel()

	const parentID, id = "e1", "p1"
	const href = "/edev/e1/fsa/f1/derp/p1"

	bodies := make(map[bool]string)
	for _, put := range []bool{false, true} {
		scoped := memory.NewScopedStore[sep2.DERProgram]()
		program := sep2.DERProgram{SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: href},
		}}
		if err := scoped.Create(context.Background(), parentID, id, program); err != nil {
			t.Fatalf("seed: %v", err)
		}

		h := scopedResourceHandler[sep2.DERProgram](scoped, "id", "derpId", itemMethods{Put: put}, nil)
		r := httptest.NewRequest(http.MethodGet, href, nil)
		r.SetPathValue("id", parentID)
		r.SetPathValue("derpId", id)
		w := httptest.NewRecorder()
		h(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("Put=%v: status = %d, want 200", put, w.Code)
		}
		bodies[put] = w.Body.String()
	}

	if bodies[false] != bodies[true] {
		t.Errorf("declaring PUT changed the GET response.\n read-only: %s\n  writable: %s", bodies[false], bodies[true])
	}
}
