//go:build csip_test_hooks

// Tests for the build-tag-gated test-only mutation surface. These run
// only when the csip_test_hooks tag is set; the companion file
// test_mutations_notag_test.go (no tag predicate) verifies that the
// surface is absent from a default build.

package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	tmFSASwap      = "/test/mutations/fsa-swap"
	tmSubCancel    = "/test/mutations/subscription-cancel"
	tmSubIDHdr     = "X-CSIP-Test-Subscription-ID"
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
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", nil)
	return h, stores
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
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", nil)
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
		"end_device_id": "edev-1",
		"extra_garbage": true,
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

// recordingNotifier is a handler.ResourceNotifier stub that captures every
// Notify call. Used by IEEE-093 to verify the derctl-add hook fans out
// on success and stays silent on failure paths.
//
// The mutex protects the slice; the production code path calls Notify
// from the same goroutine that serves the request, but a future Manager
// rewiring could fan out asynchronously and this keeps the test safe.
type recordingNotifier struct {
	mu    sync.Mutex
	calls []recordedNotify
}

type recordedNotify struct {
	resourceHref string
	status       uint8
}

func (r *recordingNotifier) Notify(_ context.Context, resourceHref string, status uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedNotify{resourceHref: resourceHref, status: status})
}

func (r *recordingNotifier) snapshot() []recordedNotify {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedNotify, len(r.calls))
	copy(out, r.calls)
	return out
}

// newRouterWithNotifier builds a router with the token env set AND a
// recording notifier wired through. Returns the handler, the stores,
// and the notifier so tests can assert the Notify call sequence.
func newRouterWithNotifier(t *testing.T) (http.Handler, *server.Stores, *recordingNotifier) {
	t.Helper()
	t.Setenv(tmTokenEnv, tmTestToken)
	stores := newTestStores()
	cfg := &config.Config{}
	n := &recordingNotifier{}
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", n)
	return h, stores, n
}

// TestDERControlAdd_FiresNotification — IEEE-093. On successful Create
// the derctl-add hook calls notifier.Notify with the DERProgramList
// href and NotificationStatusChanged. Aggregators subscribe to the
// DERProgramList href (UTIL-003 pattern); a new DERControl under one
// of its programs is the change event.
func TestDERControlAdd_FiresNotification(t *testing.T) {
	h, stores, n := newRouterWithNotifier(t)
	if err := stores.DERPrograms.Create(context.Background(), "edev-7", "prog-9", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-7",
		"fsa_id":         "fsa-3",
		"der_program_id": "prog-9",
		"control_id":     "ctl-42",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	calls := n.snapshot()
	if len(calls) != 1 {
		t.Fatalf("Notify calls = %d, want 1: %+v", len(calls), calls)
	}
	wantHref := "/edev/edev-7/fsa/fsa-3/derp"
	if calls[0].resourceHref != wantHref {
		t.Errorf("Notify href = %q, want %q", calls[0].resourceHref, wantHref)
	}
	if calls[0].status != sep2.NotificationStatusChanged {
		t.Errorf("Notify status = %d, want %d (NotificationStatusChanged)", calls[0].status, sep2.NotificationStatusChanged)
	}
}

// TestDERControlAdd_NoNotificationOnParentMissing — IEEE-093. A 404
// from the parent-program lookup must NOT fan out. Notification fires
// only when the store Create commits.
func TestDERControlAdd_NoNotificationOnParentMissing(t *testing.T) {
	h, _, n := newRouterWithNotifier(t)
	rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-missing",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if got := n.snapshot(); len(got) != 0 {
		t.Errorf("Notify called on 404 path: %+v", got)
	}
}

// TestDERControlAdd_NoNotificationOnDuplicate — IEEE-093. A 409 from
// the Create path must NOT fan out — the store was not changed on the
// second call.
func TestDERControlAdd_NoNotificationOnDuplicate(t *testing.T) {
	h, stores, n := newRouterWithNotifier(t)
	ctx := context.Background()
	if err := stores.DERPrograms.Create(ctx, "edev-1", "prog-1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	body := map[string]any{
		"end_device_id":  "edev-1",
		"fsa_id":         "fsa-1",
		"der_program_id": "prog-1",
		"control_id":     "ctl-1",
		"control":        sep2.DERControl{},
	}
	if rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, body); rr.Code != http.StatusCreated {
		t.Fatalf("first add status = %d, want 201", rr.Code)
	}
	// First Create fired one Notification.
	if got := n.snapshot(); len(got) != 1 {
		t.Fatalf("after first add Notify calls = %d, want 1", len(got))
	}
	// Second is 409 and must not fire again.
	if rr := postJSON(t, h, tmDERCtlAdd, tmTestToken, body); rr.Code != http.StatusConflict {
		t.Fatalf("second add status = %d, want 409", rr.Code)
	}
	if got := n.snapshot(); len(got) != 1 {
		t.Errorf("Notify fired on 409 path; calls = %+v", got)
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

// --- /test/mutations/fsa-swap (IEEE-078) ---

// seedFSA installs an FSA under (edevID, fsaID) with Href and a
// DERProgramListLink stamped at the canonical path so the swap's re-stamp
// can be verified after the fact.
func seedFSA(t *testing.T, stores *server.Stores, edevID, fsaID string) sep2.FunctionSetAssignments {
	t.Helper()
	fsa := sep2.FunctionSetAssignments{
		Resource:    sep2.Resource{Href: "/edev/" + edevID + "/fsa/" + fsaID},
		MRID:        "MRID-" + fsaID,
		Description: "seeded fsa " + fsaID,
		DERProgramListLink: &sep2.ListLink{
			Href: "/edev/" + edevID + "/fsa/" + fsaID + "/derp",
		},
	}
	if err := stores.FSAs.Create(context.Background(), edevID, fsaID, fsa); err != nil {
		t.Fatalf("seed fsa %s: %v", fsaID, err)
	}
	return fsa
}

// seedEndDevice installs an EndDevice id so the FSA-swap parent-check
// passes. Body shape doesn't matter — only the existence of the record.
func seedEndDevice(t *testing.T, stores *server.Stores, edevID string) {
	t.Helper()
	if err := stores.EndDevices.Create(context.Background(), edevID, sep2.EndDevice{}); err != nil {
		t.Fatalf("seed end device %s: %v", edevID, err)
	}
}

func TestFSASwap_Success(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	seedEndDevice(t, stores, "edev-1")
	original := seedFSA(t, stores, "edev-1", "fsa-old")

	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}

	// Source key must no longer resolve.
	if _, err := stores.FSAs.Get(ctx, "edev-1", "fsa-old"); err == nil {
		t.Fatal("source fsa still present after swap")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("source fsa Get err = %v, want ErrNotFound", err)
	}

	// Target key must hold the swapped record with re-stamped Hrefs and
	// preserved content fields.
	got, err := stores.FSAs.Get(ctx, "edev-1", "fsa-new")
	if err != nil {
		t.Fatalf("target fsa missing after swap: %v", err)
	}
	if got.Href != "/edev/edev-1/fsa/fsa-new" {
		t.Errorf("Href = %q, want %q", got.Href, "/edev/edev-1/fsa/fsa-new")
	}
	if got.DERProgramListLink == nil {
		t.Fatal("DERProgramListLink lost during swap")
	}
	if got.DERProgramListLink.Href != "/edev/edev-1/fsa/fsa-new/derp" {
		t.Errorf("DERProgramListLink.Href = %q, want %q",
			got.DERProgramListLink.Href, "/edev/edev-1/fsa/fsa-new/derp")
	}
	if got.MRID != original.MRID {
		t.Errorf("MRID = %q, want %q (content must be preserved)", got.MRID, original.MRID)
	}
	if got.Description != original.Description {
		t.Errorf("Description = %q, want %q", got.Description, original.Description)
	}
}

// TestFSASwap_ListReflectsNewAssociation verifies BASIC-003's
// observable behavior at the store layer that drives GET /edev/{id}/fsa:
// after the swap, the per-EndDevice FSA list contains only the new id
// (with the seeded content) and no entry for the old id. This is the
// data side of what the handler then renders to the harness — the GET
// path itself is exercised by the integration-level admin tests under
// real TLS chains.
func TestFSASwap_ListReflectsNewAssociation(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	ctx := context.Background()
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")

	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("swap status = %d, want 204: %s", rr.Code, rr.Body.String())
	}

	list, err := stores.FSAs.List(ctx, "edev-1", store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list fsas: %v", err)
	}
	if list.All != 1 {
		t.Fatalf("list.All = %d, want 1 (one fsa after swap)", list.All)
	}
	got := list.Items[0]
	if got.Href != "/edev/edev-1/fsa/fsa-new" {
		t.Errorf("Href = %q, want %q", got.Href, "/edev/edev-1/fsa/fsa-new")
	}
	if got.MRID != "MRID-fsa-old" {
		t.Errorf("MRID = %q, want %q (content preserved across swap)", got.MRID, "MRID-fsa-old")
	}
}

func TestFSASwap_MissingToken_Unauthorized(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	rr := postJSON(t, h, tmFSASwap, "", map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestFSASwap_WrongToken_Unauthorized(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	rr := postJSON(t, h, tmFSASwap, "not-the-token", map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestFSASwap_UnknownEndDevice_NotFound(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "missing-edev",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestFSASwap_UnknownFromFSA_NotFound(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	// No FSA seeded under fsa-old.
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestFSASwap_TargetExists_Conflict(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	seedFSA(t, stores, "edev-1", "fsa-new")
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	// Source must still resolve — Create-before-Delete preserved it.
	if _, err := stores.FSAs.Get(context.Background(), "edev-1", "fsa-old"); err != nil {
		t.Errorf("source fsa wiped after conflict: %v", err)
	}
}

func TestFSASwap_SameFromAndTo_BadRequest(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-old",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestFSASwap_MissingFields_BadRequest(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing end_device_id", map[string]any{"from_fsa": "a", "to_fsa": "b"}},
		{"missing from_fsa", map[string]any{"end_device_id": "edev-1", "to_fsa": "b"}},
		{"missing to_fsa", map[string]any{"end_device_id": "edev-1", "from_fsa": "a"}},
		{"empty body", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := postJSON(t, h, tmFSASwap, tmTestToken, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rr.Code)
			}
		})
	}
}

func TestFSASwap_MalformedJSON_BadRequest(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmFSASwap, strings.NewReader("{not json"))
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestFSASwap_NoBody_BadRequest(t *testing.T) {
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmFSASwap, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestFSASwap_NilFSAStore_InternalError exercises the defensive guard
// against an unconfigured FSA store. The production Stores always wires
// stores.FSAs, but a misconfigured test setup or future deployment shape
// could omit it — the handler must refuse instead of NPE'ing.
func TestFSASwap_NilFSAStore_InternalError(t *testing.T) {
	t.Setenv(tmTokenEnv, tmTestToken)
	stores := newTestStores()
	stores.FSAs = nil
	cfg := &config.Config{}
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", nil)

	seedEndDevice(t, stores, "edev-1")
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestFSASwap_UnknownField_Rejected(t *testing.T) {
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedFSA(t, stores, "edev-1", "fsa-old")
	rr := postJSON(t, h, tmFSASwap, tmTestToken, map[string]any{
		"end_device_id": "edev-1",
		"from_fsa":      "fsa-old",
		"to_fsa":        "fsa-new",
		"extra_garbage": true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (DisallowUnknownFields)", rr.Code)
	}
}

// --- /test/mutations/subscription-cancel (IEEE-079, MAINT-006) ---

// seedSubscription installs a subscription with the given id into the
// store. The tombstone set is independent of the store, so this leaves it
// in whatever state the test arranged.
func seedSubscription(t *testing.T, stores *server.Stores, edevID, subID string) {
	t.Helper()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/sub/" + subID},
		},
		SubscribedResource: "/edev/" + edevID,
		NotificationURI:    "https://example.test/notify",
	}
	if err := stores.Subscriptions.Create(context.Background(), subID, sub); err != nil {
		t.Fatalf("seed subscription %s: %v", subID, err)
	}
}

// createSubscriptionViaAPI exercises the production POST /edev/{id}/sub
// path with the test-only X-CSIP-Test-Subscription-ID header so the
// caller can drive a deterministic ID. Returns the recorder for status
// assertions. The override header is honored only because the
// csip_test_hooks build tag is set; production builds ignore it.
//
// Stubs a minimal *tls.ConnectionState with a single peer certificate so
// auth.IdentityMiddleware admits the request. Mirrors the stubbing used
// in internal/auth/acl_test.go and elsewhere.
func createSubscriptionViaAPI(t *testing.T, h http.Handler, edevID, subID string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`<Subscription xmlns="urn:ieee:std:2030.5:ns">` +
		`<subscribedResource>/edev/` + edevID + `</subscribedResource>` +
		`<notificationURI>https://example.test/notify</notificationURI>` +
		`<encoding>0</encoding>` +
		`</Subscription>`)
	req := httptest.NewRequest(http.MethodPost, "/edev/"+edevID+"/sub", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	if subID != "" {
		req.Header.Set(tmSubIDHdr, subID)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// getWithClientCert is the GET equivalent of createSubscriptionViaAPI:
// stubs a client cert so the ACL chain admits the request.
func getWithClientCert(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestSubscriptionCancel_Success(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedSubscription(t, stores, "edev-1", "sub-A")

	rr := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	// Subscription removed from store.
	if _, err := stores.Subscriptions.Get(context.Background(), "sub-A"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-cancel Get: err = %v, want ErrNotFound", err)
	}
	// Subscription ID is now tombstoned.
	if !handler.IsSubscriptionCanceled("sub-A") {
		t.Fatal("sub-A not in canceled set after cancel")
	}
}

func TestSubscriptionCancel_MissingToken_Unauthorized(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedSubscription(t, stores, "edev-1", "sub-A")

	rr := postJSON(t, h, tmSubCancel, "", map[string]string{"subscription_id": "sub-A"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	// Store untouched.
	if _, err := stores.Subscriptions.Get(context.Background(), "sub-A"); err != nil {
		t.Fatalf("subscription removed by unauthorized request: %v", err)
	}
}

func TestSubscriptionCancel_WrongToken_Unauthorized(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmSubCancel, "not-the-token", map[string]string{"subscription_id": "sub-A"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestSubscriptionCancel_UnknownID_NotFound(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-missing",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	// A 404 must NOT poison the tombstone — a never-existing ID being
	// recorded as canceled would block legitimate future creates.
	if handler.IsSubscriptionCanceled("sub-missing") {
		t.Fatal("unknown id tombstoned on 404; tombstone must only follow a successful delete")
	}
}

func TestSubscriptionCancel_MissingID_BadRequest(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestSubscriptionCancel_MalformedBody_BadRequest(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmSubCancel, strings.NewReader("{not json"))
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestSubscriptionCancel_EmptyBody_BadRequest(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodPost, tmSubCancel, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestSubscriptionCancel_UnknownField_Rejected(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	rr := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
		"extra_garbage":   true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (DisallowUnknownFields)", rr.Code)
	}
}

func TestSubscriptionCancel_StoreNil_InternalError(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	t.Setenv(tmTokenEnv, tmTestToken)
	stores := newTestStores()
	stores.Subscriptions = nil
	cfg := &config.Config{}
	h, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", nil)

	rr := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestSubscriptionCancel_ReSubscribeRefused_Conflict(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedSubscription(t, stores, "edev-1", "sub-A")

	// Cancel the subscription via the mutation hook.
	cancel := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", cancel.Code)
	}

	// Drive the production POST /edev/{id}/sub path with the canceled ID
	// via the test-only header. The tombstone hook should reject it with
	// 409 Conflict before the subscription store is touched.
	resub := createSubscriptionViaAPI(t, h, "edev-1", "sub-A")
	if resub.Code != http.StatusConflict {
		t.Fatalf("re-subscribe status = %d, want 409: %s", resub.Code, resub.Body.String())
	}
	// Subscription is still absent from the store after the refusal.
	if _, err := stores.Subscriptions.Get(context.Background(), "sub-A"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-refusal Get: err = %v, want ErrNotFound", err)
	}
}

func TestSubscriptionCancel_DifferentIDAfterCancel_Allowed(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedSubscription(t, stores, "edev-1", "sub-A")

	cancel := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", cancel.Code)
	}

	// A different ID is not tombstoned and the production create path
	// must produce a 201.
	create := createSubscriptionViaAPI(t, h, "edev-1", "sub-B")
	if create.Code != http.StatusCreated {
		t.Fatalf("create sub-B status = %d, want 201: %s", create.Code, create.Body.String())
	}
	got, err := stores.Subscriptions.Get(context.Background(), "sub-B")
	if err != nil {
		t.Fatalf("sub-B not in store after create: %v", err)
	}
	if !strings.HasSuffix(got.Href, "/edev/edev-1/sub/sub-B") {
		t.Fatalf("sub-B Href = %q, want suffix /edev/edev-1/sub/sub-B", got.Href)
	}
	// Sanity: the original ID stays refused even after a different ID
	// succeeds.
	if !handler.IsSubscriptionCanceled("sub-A") {
		t.Fatal("sub-A tombstone cleared by unrelated successful create")
	}
}

func TestSubscriptionCancel_DoubleCancel_SecondIsNotFound(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedSubscription(t, stores, "edev-1", "sub-A")

	first := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if first.Code != http.StatusNoContent {
		t.Fatalf("first cancel status = %d, want 204", first.Code)
	}
	// Second cancel: store no longer has it, so 404 — the tombstone is
	// already set from the first call and is not re-marked.
	second := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if second.Code != http.StatusNotFound {
		t.Fatalf("second cancel status = %d, want 404", second.Code)
	}
	// Tombstone still in place.
	if !handler.IsSubscriptionCanceled("sub-A") {
		t.Fatal("tombstone cleared between first and second cancel")
	}
}

// TestSubscriptionCancel_NoOverrideHeader_AutoIDStillWorks verifies that
// the production create path with no override header generates an
// auto-id (sub-<unixnano>) and is unaffected by the tombstone set when
// the new id is not in it. Ensures the override hook does not regress
// the default path.
func TestSubscriptionCancel_NoOverrideHeader_AutoIDStillWorks(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	handler.MarkSubscriptionCanceled("sub-tombstoned")

	// No X-CSIP-Test-Subscription-ID header: the create path generates
	// "sub-<unixnano>" which will not collide with "sub-tombstoned".
	rr := createSubscriptionViaAPI(t, h, "edev-1", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("create with auto-id status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
}

// TestSubscriptionCancel_ListGet_Excludes verifies the GET /edev/{id}/sub
// list excludes the canceled subscription so the harness can confirm
// removal via the public read path, not just the store API.
func TestSubscriptionCancel_ListGet_Excludes(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	seedSubscription(t, stores, "edev-1", "sub-A")
	seedSubscription(t, stores, "edev-1", "sub-B")

	cancel := postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
		"subscription_id": "sub-A",
	})
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", cancel.Code)
	}

	rr := getWithClientCert(t, h, "/edev/edev-1/sub")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var list sep2.SubscriptionList
	if err := xml.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, s := range list.Subscription {
		if strings.HasSuffix(s.Href, "/sub-A") {
			t.Fatalf("canceled subscription still in list: %+v", s)
		}
	}
	// sub-B must still be there.
	found := false
	for _, s := range list.Subscription {
		if strings.HasSuffix(s.Href, "/sub-B") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("sibling subscription sub-B missing from list after cancel of sub-A")
	}
}

// TestSubscriptionCancel_GET_NotAllowed verifies method gating on the
// mutation path. Mirrors TestMutationSurface_GET_NotAllowed but scoped
// to the new endpoint.
func TestSubscriptionCancel_GET_NotAllowed(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, _ := newRouterWithTokenAndStores(t)
	req := httptest.NewRequest(http.MethodGet, tmSubCancel, nil)
	req.Header.Set(tmTokenHdr, tmTestToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestSubscriptionCancel_Race exercises concurrent cancel + create on
// distinct IDs. Run with -race to assert the canceled-id set's mutex is
// honored. Distinct IDs avoid cross-test ordering noise.
func TestSubscriptionCancel_Race(t *testing.T) {
	defer handler.ResetCanceledSubscriptions()
	h, stores := newRouterWithTokenAndStores(t)
	seedEndDevice(t, stores, "edev-1")
	const n = 32
	for i := 0; i < n; i++ {
		id := "sub-race-" + strconv.Itoa(i)
		if err := stores.Subscriptions.Create(context.Background(), id, sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/edev-1/sub/" + id},
			},
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			id := "sub-race-" + strconv.Itoa(i)
			_ = postJSON(t, h, tmSubCancel, tmTestToken, map[string]any{
				"subscription_id": id,
			})
		}
	}()
	for i := 0; i < n; i++ {
		id := "sub-new-" + strconv.Itoa(i)
		_ = createSubscriptionViaAPI(t, h, "edev-1", id)
	}
	<-done

	for i := 0; i < n; i++ {
		id := "sub-race-" + strconv.Itoa(i)
		// Re-resolve the value separately for the post-race assertion.
		if !handler.IsSubscriptionCanceled(id) {
			t.Fatalf("%s not tombstoned after race", id)
		}
	}
}
