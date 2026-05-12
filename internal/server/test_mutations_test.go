//go:build csip_test_hooks

// Tests for the build-tag-gated test-only mutation surface. These run
// only when the csip_test_hooks tag is set; the companion file
// test_mutations_notag_test.go (no tag predicate) verifies that the
// surface is absent from a default build.

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
)

const (
	tmTestToken    = "test-mutation-token-xyz"
	tmTokenEnv     = "SEP2_TEST_MUTATION_TOKEN"
	tmTokenHdr     = "X-CSIP-Test-Token"
	tmEdevDelete   = "/test/mutations/edev-delete-oob"
	tmPrimacy      = "/test/mutations/derprog-primacy"
	tmDERCtlAdd    = "/test/mutations/derctl-add"
	tmTimeAdvance  = "/test/mutations/time-advance"
	tmSelfDevScope = "sdev"
)

// newRouterWithTokenAndStores builds a router with the token env set so
// the mutation surface is registered. Returns the handler and the stores
// for assertion access.
func newRouterWithTokenAndStores(t *testing.T) (http.Handler, *server.Stores) {
	t.Helper()
	t.Setenv(tmTokenEnv, tmTestToken)
	stores := newTestStores()
	cfg := &config.Config{}
	return server.NewRouter(cfg, stores, nil, "", "", nil), stores
}

func postJSON(t *testing.T, h http.Handler, path string, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	if token != "" {
		req.Header.Set(tmTokenHdr, token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// --- Auth / scaffolding ---

func TestMutationSurface_MissingToken_Unauthorized(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, "", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestMutationSurface_WrongToken_Unauthorized(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, "not-the-token", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestMutationSurface_EmptyEnvDisablesSurface(t *testing.T) {
	// Tag is set but the env is empty — RegisterMutationHandlers should
	// log a warning and register no routes. A request to a mutation path
	// must miss the mux and return 404.
	t.Setenv(tmTokenEnv, "")
	stores := newTestStores()
	cfg := &config.Config{}
	h := server.NewRouter(cfg, stores, nil, "", "", nil)
	rr := postJSON(t, h, tmEdevDelete, "anything", map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (surface should be off)", rr.Code)
	}
}

// --- /test/mutations/edev-delete-oob ---

func TestEdevDeleteOOB_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.EndDevices.Create(context.Background(), "edev-1", sep2.EndDevice{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{"end_device_id": "edev-1"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if _, err := stores.EndDevices.Get(context.Background(), "edev-1"); err == nil {
		t.Fatal("EndDevice still present after OOB delete")
	}
}

func TestEdevDeleteOOB_NotFound(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{"end_device_id": "missing"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestEdevDeleteOOB_MissingID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]string{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_EmptyBody(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmEdevDelete, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_MalformedJSON(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmEdevDelete, strings.NewReader("{not json"))
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestEdevDeleteOOB_UnknownField_Rejected(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmEdevDelete, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"extra_garbage":  true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (DisallowUnknownFields)", rr.Code)
	}
}

// --- /test/mutations/derprog-primacy ---

func TestDERProgPrimacy_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{Primacy: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	primacy := uint8(42)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"program_id":    "prog-1",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	got, err := stores.DERPrograms.Get(ctx, "edev-1", "prog-1")
	if err != nil {
		t.Fatalf("post-mutation get: %v", err)
	}
	if got.Primacy != 42 {
		t.Fatalf("primacy = %d, want 42", got.Primacy)
	}
}

func TestDERProgPrimacy_ProgramNotFound(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(5)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-X",
		"program_id":    "prog-X",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestDERProgPrimacy_MissingPrimacy(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"program_id":    "prog-1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERProgPrimacy_MissingEdevID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(1)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"program_id": "prog-1",
		"primacy":    primacy,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERProgPrimacy_MissingProgramID(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	primacy := uint8(1)
	rr := postJSON(t, h, tmPrimacy, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"primacy":       primacy,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// --- /test/mutations/derctl-add ---

func TestDERControlAdd_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	// Verify the control landed under the deep composite key
	// used by the production router.
	got, err := stores.DERControls.Get(ctx, "edev-1/fsa-1/prog-1", "ctl-1")
	if err != nil {
		t.Fatalf("post-mutation get: %v", err)
	}
	_ = got
}

func TestDERControlAdd_ParentProgramMissing(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-X",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestDERControlAdd_DuplicateID_Conflict(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	// First add: should succeed
	first := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first add status = %d, want 201", first.Code)
	}
	// Second add with same ID: 409 Conflict
	second := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("second add status = %d, want 409", second.Code)
	}
}

func TestDERControlAdd_MissingScopeFields(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	// Missing fsa_id
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDERControlAdd_MissingControlID(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// --- /test/mutations/time-advance (IEEE-025, CORE-006) ---

// setupTimeAdvance resets the handler clock offset, builds a tag-enabled
// router, and registers a cleanup that re-zeroes the offset so the next
// test starts from wall-clock baseline. Time-advance tests must NOT run
// in parallel (they share package state in handler.clockOffsetNanos).
func setupTimeAdvance(t *testing.T) (http.Handler, *server.Stores) {
	t.Helper()
	handler.ResetClockOffset()
	t.Cleanup(handler.ResetClockOffset)
	return newRouterWithTokenAndStores(t)
}

func TestTimeAdvance_ForwardShiftsClock(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	before := time.Now().Add(handler.ClockOffset())
	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 3600})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	after := time.Now().Add(handler.ClockOffset())
	delta := after.Sub(before)
	// Allow a small wall-clock fudge for the postJSON call itself.
	if delta < 59*time.Minute+59*time.Second || delta > 1*time.Hour+1*time.Second {
		t.Fatalf("clock delta = %v, want ~1h", delta)
	}
}

// readTmDirect invokes handler.HandleTime against an httptest recorder,
// bypassing the production /tm route's TLS-client-cert auth chain. We're
// asserting that the package-level nowFunc seam reflects the mutation
// handler's offset; the auth chain is exercised by its own tests
// (internal/auth/...).
func readTmDirect(t *testing.T) sep2.Time {
	t.Helper()
	h := handler.HandleTime(&config.Config{})
	req := httptest.NewRequest(http.MethodGet, "/tm", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/tm direct status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var tm sep2.Time
	if err := xml.Unmarshal(rec.Body.Bytes(), &tm); err != nil {
		t.Fatalf("decode /tm xml: %v", err)
	}
	return tm
}

func TestTimeAdvance_PositiveSeconds_TmEndpointReflectsAdvance(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	wallBefore := time.Now().Unix()

	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 3600})
	if rr.Code != http.StatusOK {
		t.Fatalf("advance status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	tm := readTmDirect(t)
	advance := tm.CurrentTime - wallBefore
	if advance < 3599 || advance > 3601 {
		t.Fatalf("/tm advance = %ds, want ~3600s", advance)
	}
}

func TestTimeAdvance_NegativeSeconds_TmEndpointReflectsRewind(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	wallBefore := time.Now().Unix()

	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": -120})
	if rr.Code != http.StatusOK {
		t.Fatalf("advance status = %d, want 200", rr.Code)
	}
	tm := readTmDirect(t)
	delta := tm.CurrentTime - wallBefore
	// Expect -120s with a small fudge.
	if delta < -121 || delta > -119 {
		t.Fatalf("/tm rewind = %ds, want ~-120s", delta)
	}
}

func TestTimeAdvance_PersistsAcrossReads(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	if rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 600}); rr.Code != http.StatusOK {
		t.Fatalf("advance status = %d", rr.Code)
	}
	// Two successive reads ~ 50ms apart should both reflect the offset;
	// the offset is not one-shot.
	t0 := readTmDirect(t).CurrentTime
	time.Sleep(50 * time.Millisecond)
	t1 := readTmDirect(t).CurrentTime
	wall := time.Now().Unix()
	if t0-wall < 599 || t0-wall > 601 {
		t.Fatalf("first read offset = %d, want ~600s", t0-wall)
	}
	if t1 < t0 {
		t.Fatalf("second read %d earlier than first %d", t1, t0)
	}
}

func TestTimeAdvance_EmitsTimeAdjustedLogEvent(t *testing.T) {
	h, stores := setupTimeAdvance(t)
	if rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 3600}); rr.Code != http.StatusOK {
		t.Fatalf("advance status = %d", rr.Code)
	}
	result, err := stores.LogEvents.List(context.Background(), tmSelfDevScope, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list SelfDevice log events: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("log event count = %d, want 1", len(result.Items))
	}
	got := result.Items[0]
	if got.FunctionSet != sep2.FunctionSetTime {
		t.Errorf("functionSet = %d, want %d (Time)", got.FunctionSet, sep2.FunctionSetTime)
	}
	if got.LogEventCode == 0 {
		t.Errorf("logEventCode = 0, want non-zero TM_TIME_ADJUSTED sentinel")
	}
	if !strings.HasPrefix(got.Href, "/sdev/log/") {
		t.Errorf("href = %q, want /sdev/log/ prefix", got.Href)
	}
	if !strings.Contains(got.Details, "TM_TIME_ADJUSTED") {
		t.Errorf("details = %q, want to contain TM_TIME_ADJUSTED", got.Details)
	}
}

func TestTimeAdvance_CumulativeShifts(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	if rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 100}); rr.Code != http.StatusOK {
		t.Fatalf("first advance status = %d", rr.Code)
	}
	if rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 200}); rr.Code != http.StatusOK {
		t.Fatalf("second advance status = %d", rr.Code)
	}
	got := handler.ClockOffset()
	want := 300 * time.Second
	if got < want-1*time.Millisecond || got > want+1*time.Millisecond {
		t.Fatalf("cumulative offset = %v, want %v", got, want)
	}
}

func TestTimeAdvance_MissingSeconds_BadRequest(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	// And the clock must not have moved.
	if got := handler.ClockOffset(); got != 0 {
		t.Fatalf("clock advanced on bad request: offset = %v", got)
	}
}

func TestTimeAdvance_MalformedJSON_BadRequest(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	req := httptest.NewRequest(http.MethodPost, tmTimeAdvance, strings.NewReader("{not json"))
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if got := handler.ClockOffset(); got != 0 {
		t.Fatalf("clock advanced on malformed body: offset = %v", got)
	}
}

func TestTimeAdvance_UnknownField_Rejected(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{
		"seconds": 60,
		"extra":   "garbage",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (DisallowUnknownFields)", rr.Code)
	}
}

func TestTimeAdvance_MissingToken_Unauthorized(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	rr := postJSON(t, h, tmTimeAdvance, "", map[string]any{"seconds": 60})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := handler.ClockOffset(); got != 0 {
		t.Fatalf("clock advanced without token: offset = %v", got)
	}
}

func TestTimeAdvance_WrongToken_Unauthorized(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	rr := postJSON(t, h, tmTimeAdvance, "not-the-token", map[string]any{"seconds": 60})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := handler.ClockOffset(); got != 0 {
		t.Fatalf("clock advanced on wrong token: offset = %v", got)
	}
}

func TestTimeAdvance_ResponseBodyShape(t *testing.T) {
	h, _ := setupTimeAdvance(t)
	rr := postJSON(t, h, tmTimeAdvance, tmTestToken, map[string]any{"seconds": 3600})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		LogEventHref  string `json:"log_event_href"`
		CurrentTime   int64  `json:"current_time"`
		OffsetSeconds int64  `json:"offset_seconds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v: %s", err, rr.Body.String())
	}
	if !strings.HasPrefix(body.LogEventHref, "/sdev/log/") {
		t.Errorf("log_event_href = %q, want /sdev/log/ prefix", body.LogEventHref)
	}
	if body.OffsetSeconds != 3600 {
		t.Errorf("offset_seconds = %d, want 3600", body.OffsetSeconds)
	}
	if body.CurrentTime < time.Now().Unix()+3500 {
		t.Errorf("current_time = %d, want ~now+3600s", body.CurrentTime)
	}
}

// --- Method-not-POST sanity ---

func TestMutationSurface_GET_NotAllowed(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodGet, tmEdevDelete, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	// http.ServeMux returns 405 when the method-qualified pattern misses.
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
