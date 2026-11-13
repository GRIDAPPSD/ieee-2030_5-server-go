package singleton_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const parentKey = "edev1/der1"

var errDeleteBroken = errors.New("DELETEBROKEN backend down")

// failingDelete forwards everything to the embedded store except Delete.
type failingDelete struct {
	store.ScopedStore[sep2.DERSettings]
}

func (failingDelete) Delete(context.Context, string, string) error { return errDeleteBroken }

// parentSequence answers each successive existence check with the next error
// (nil for present); the last answer repeats.
func parentSequence(answers ...error) (check func(*http.Request) error, calls *int) {
	calls = new(int)
	return func(*http.Request) error {
		i := *calls
		*calls++
		if i >= len(answers) {
			i = len(answers) - 1
		}
		return answers[i]
	}, calls
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func putSettings(h http.Handler, method string) *httptest.ResponseRecorder {
	maxW := sep2.ActivePower{Value: 5000}
	body, _ := xml.Marshal(&sep2.DERSettings{SetMaxW: &maxW})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "/edev/1/der/1/derg", bytes.NewReader(body)))
	return w
}

func newHandler(s store.ScopedStore[sep2.DERSettings], check func(*http.Request) error) http.Handler {
	return singleton.HandleSingletonGetPut[sep2.DERSettings](
		s,
		func(*http.Request) string { return parentKey },
		func(*http.Request) sep2.DERSettings { return sep2.DERSettings{} },
		singleton.WithParent[sep2.DERSettings](check),
	)
}

func TestWithParent(t *testing.T) {
	boom := errors.New("parent lookup failed")
	tests := []struct {
		name       string
		method     string
		answers    []error
		wantStatus int
		wantStored bool
	}{
		{"PUT parent missing before write stores nothing", http.MethodPut, []error{store.ErrNotFound}, http.StatusNotFound, false},
		{"PUT parent removed after write removes the record", http.MethodPut, []error{nil, store.ErrNotFound}, http.StatusNotFound, false},
		{"PUT parent present stores the record", http.MethodPut, []error{nil}, http.StatusNoContent, true},
		{"PUT re-check failure removes the record and answers 500", http.MethodPut, []error{nil, boom}, http.StatusInternalServerError, false},
		{"GET parent missing answers 404", http.MethodGet, []error{store.ErrNotFound}, http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captureLog(t)
			s := memory.NewScopedStore[sep2.DERSettings]()
			check, _ := parentSequence(tt.answers...)
			w := putSettings(newHandler(s, check), tt.method)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			got, err := s.Get(context.Background(), parentKey, singleton.SingletonKey)
			if tt.wantStored {
				if err != nil || got.SetMaxW == nil || got.SetMaxW.Value != 5000 {
					t.Errorf("stored = %+v, err %v; want SetMaxW 5000", got, err)
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Get err = %v, want ErrNotFound (record must not remain)", err)
			}
		})
	}
}

// A failed compensating Delete leaves an orphan, so the answer is a single 500
// that is logged once, not a 404 with a second status written after it.
func TestWithParentFailedCompensationAnswers500(t *testing.T) {
	buf := captureLog(t)
	inner := memory.NewScopedStore[sep2.DERSettings]()
	check, _ := parentSequence(nil, store.ErrNotFound)

	w := putSettings(newHandler(failingDelete{inner}, check), http.MethodPut)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "not found") || strings.Contains(body, "DELETEBROKEN") {
		t.Errorf("body = %q; want the generic 500 message only", body)
	}
	if n := strings.Count(buf.String(), "DELETEBROKEN"); n != 1 {
		t.Errorf("failure logged %d times, want 1: %q", n, buf.String())
	}
	if _, err := inner.Get(context.Background(), parentKey, singleton.SingletonKey); err != nil {
		t.Errorf("orphan Get err = %v; the record is expected to remain after the failed Delete", err)
	}
}
