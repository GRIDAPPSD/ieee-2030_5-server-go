package handler_test

// Tests for IEEE-009: log before returning 5xx in handlers.
//
// Test pattern: table-driven example tests per handler (one table per handler file).
// Each row forces the underlying error path via a mock store that returns a
// non-sentinel error, then captures the standard log output and asserts:
//   1. HTTP response code is 500.
//   2. The captured log contains the error string.
//
// The tests are RED before the fix because the 5xx paths in edev.go and mirror.go
// currently call http.Error without a preceding log.Printf.
// After the fix (log.Printf before each http.Error(..., 500)), the captured log
// will contain the error string and the tests go GREEN.
//
// singleton.go: HandleSingletonGetPut takes *memory.ScopedStore[T] (concrete, not
// an interface), so a failing-store cannot be injected without a handler-signature
// change. The log lines are added in the fix and verified manually; in-band test
// coverage waits for a future refactor to accept an interface.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// captureLog redirects the standard logger to a buffer for the duration of f,
// then restores the original output. Returns the captured log text.
func captureLog(f func()) string {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	f()
	return buf.String()
}

// ─── errEndDeviceStore ────────────────────────────────────────────────────────

// errEndDeviceStore wraps memory.EndDeviceStore and injects configurable errors
// on Get, Create, Update, and Delete to drive the handler 5xx paths.
type errEndDeviceStore struct {
	inner     *memory.EndDeviceStore
	getErr    error
	createErr error
	updateErr error
	deleteErr error
}

func newErrEndDeviceStore() *errEndDeviceStore {
	return &errEndDeviceStore{inner: memory.NewEndDeviceStore()}
}

func (s *errEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	if s.getErr != nil {
		return sep2.EndDevice{}, s.getErr
	}
	return s.inner.Get(ctx, id)
}

func (s *errEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	return s.inner.GetBySFDI(ctx, sfdi)
}

func (s *errEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	return s.inner.GetByLFDI(ctx, lfdi)
}

func (s *errEndDeviceStore) Create(ctx context.Context, id string, dev sep2.EndDevice) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.inner.Create(ctx, id, dev)
}

func (s *errEndDeviceStore) Update(ctx context.Context, id string, dev sep2.EndDevice) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.inner.Update(ctx, id, dev)
}

func (s *errEndDeviceStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.inner.Delete(ctx, id)
}

func (s *errEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return s.inner.List(ctx, opts)
}

func (s *errEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.inner.Count(ctx)
}

// ─── errMUPStore ──────────────────────────────────────────────────────────────

// errMUPStore wraps memory.Store[sep2.MirrorUsagePoint] and injects errors.
type errMUPStore struct {
	base      *memory.Store[sep2.MirrorUsagePoint]
	getErr    error
	createErr error
}

func (s *errMUPStore) Get(ctx context.Context, id string) (sep2.MirrorUsagePoint, error) {
	if s.getErr != nil {
		return sep2.MirrorUsagePoint{}, s.getErr
	}
	return s.base.Get(ctx, id)
}

func (s *errMUPStore) Create(ctx context.Context, id string, v sep2.MirrorUsagePoint) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.base.Create(ctx, id, v)
}

func (s *errMUPStore) Update(ctx context.Context, id string, v sep2.MirrorUsagePoint) error {
	return s.base.Update(ctx, id, v)
}

func (s *errMUPStore) Delete(ctx context.Context, id string) error {
	return s.base.Delete(ctx, id)
}

func (s *errMUPStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.MirrorUsagePoint], error) {
	return s.base.List(ctx, opts)
}

func (s *errMUPStore) Count(ctx context.Context) (uint32, error) {
	return s.base.Count(ctx)
}

// ─── edev.go table ───────────────────────────────────────────────────────────

func TestHandleEndDevice5xxLogged(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*errEndDeviceStore)
		makeReq func(*errEndDeviceStore) *http.Request
		handler func(*errEndDeviceStore) http.Handler
	}{
		{
			name:  "HandleEndDevice/store.Get fails",
			setup: func(s *errEndDeviceStore) { s.getErr = fmt.Errorf("disk I/O: simulate 5xx") },
			handler: func(s *errEndDeviceStore) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(s))
				return mux
			},
			makeReq: func(_ *errEndDeviceStore) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/edev/somedev", nil)
			},
		},
		{
			name: "HandleCreateEndDevice/store.Create fails",
			setup: func(s *errEndDeviceStore) {
				s.createErr = fmt.Errorf("create I/O: simulate 5xx")
			},
			handler: func(s *errEndDeviceStore) http.Handler {
				return handler.HandleCreateEndDevice(s)
			},
			makeReq: func(_ *errEndDeviceStore) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/edev", nil)
				return addIdentity(req, "123456789012", "AABBCCDD00112233445566778899AABBCCDDEEFF")
			},
		},
		{
			name: "HandleUpdateEndDevice/store.Update fails",
			setup: func(s *errEndDeviceStore) {
				s.updateErr = fmt.Errorf("update I/O: simulate 5xx")
				// pre-seed so not-found is bypassed
				_ = s.inner.Create(context.Background(), "somedev", sep2.EndDevice{})
			},
			handler: func(s *errEndDeviceStore) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(s))
				return mux
			},
			makeReq: func(_ *errEndDeviceStore) *http.Request {
				body := bytes.NewReader([]byte(`<EndDevice xmlns="urn:ieee:std:2030.5:ns"><SFDI>123456789012</SFDI></EndDevice>`))
				return httptest.NewRequest(http.MethodPut, "/edev/somedev", body)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newErrEndDeviceStore()
			tc.setup(s)
			h := tc.handler(s)
			req := tc.makeReq(s)
			w := httptest.NewRecorder()

			logOutput := captureLog(func() { h.ServeHTTP(w, req) })

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			// After fix: log output must contain the injected error string.
			var errStr string
			if s.getErr != nil {
				errStr = s.getErr.Error()
			} else if s.createErr != nil {
				errStr = s.createErr.Error()
			} else if s.updateErr != nil {
				errStr = s.updateErr.Error()
			}
			if !strings.Contains(logOutput, errStr) {
				t.Fatalf("log output does not contain error string %q\ngot: %q", errStr, logOutput)
			}
		})
	}
}

// ─── mirror.go table ─────────────────────────────────────────────────────────

func TestHandleMirror5xxLogged(t *testing.T) {
	cases := []struct {
		name    string
		mupErr  error
		handler func(*errMUPStore) http.Handler
		makeReq func() *http.Request
	}{
		{
			name:   "HandleMirrorUsagePoint/store.Get fails",
			mupErr: fmt.Errorf("mirror get I/O: simulate 5xx"),
			handler: func(s *errMUPStore) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("GET /mup/{id}", handler.HandleMirrorUsagePoint(s))
				return mux
			},
			makeReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/mup/somemup", nil)
			},
		},
		{
			name:   "HandleCreateMirrorUsagePoint/store.Create fails",
			mupErr: fmt.Errorf("mirror create I/O: simulate 5xx"),
			handler: func(s *errMUPStore) http.Handler {
				return handler.HandleCreateMirrorUsagePoint(s)
			},
			makeReq: func() *http.Request {
				body := bytes.NewReader([]byte(`<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>test-mrid</mRID></MirrorUsagePoint>`))
				req := httptest.NewRequest(http.MethodPost, "/mup", body)
				return addIdentity(req, "123456789012", "AABBCCDD00112233445566778899AABBCCDDEEFF")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := memory.NewStore[sep2.MirrorUsagePoint]()
			var s *errMUPStore
			if strings.Contains(tc.name, "Get") {
				s = &errMUPStore{base: base, getErr: tc.mupErr}
			} else {
				s = &errMUPStore{base: base, createErr: tc.mupErr}
			}
			h := tc.handler(s)
			req := tc.makeReq()
			w := httptest.NewRecorder()

			logOutput := captureLog(func() { h.ServeHTTP(w, req) })

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			if !strings.Contains(logOutput, tc.mupErr.Error()) {
				t.Fatalf("log output does not contain error %q\ngot: %q", tc.mupErr.Error(), logOutput)
			}
		})
	}
}
