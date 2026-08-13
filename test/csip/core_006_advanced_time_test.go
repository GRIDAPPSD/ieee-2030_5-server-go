//go:build csip_test_hooks

// CSIP V1.2 §5.8 — Advanced Time.
//
// CORE-006 exercises the server's ability to report a *shifted* clock
// after an out-of-band time-management event (e.g. an NTP step). The
// production server has no public endpoint that mutates wall clock —
// adding one would be wrong — so the conformance harness drives the
// shift through the #28 build-tag-gated mutation surface:
//
//	POST /test/mutations/time-advance  {"seconds": 3600}
//
// The mutation:
//   1. Shifts the package-level offset added by `coresep2time.nowFunc` (set up
//      in `coresep2time` (`pkg/sep2srv/handlers/sep2time/time_test_hook.go`)
//      under the same build tag), so the next GET /tm returns wallclock + offset.
//   2. Appends a TM_TIME_ADJUSTED LogEvent to `stores.LogEvents` under
//      the SelfDevice sentinel scope ("sdev"). The production server
//      does not expose /sdev/log over HTTP today; the LogEvent is
//      observable through the store handle that BootServer surfaces on
//      the BootedServer. Wiring SelfDevice's LogEventListLink into /dcap
//      is out of scope for this test (Pike Hard Rule #1 — that is a
//      production-surface change, not a conformance test).
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (boot a CSIP server)                                ────► csiptest.BootServer
//	Step 2 (GET /tm, observe baseline currentTime)             ────► WalkLink /tm, capture t0
//	Step 3 (utility advances clock by +1h via OOB hook)        ────► POST /test/mutations/time-advance {seconds:3600}
//	Step 4 (server returns 200 with an audit envelope)         ────► resp.StatusCode == 200,
//	                                                                offset_seconds in payload == 3600
//	Step 5 (subsequent GET /tm reflects the shift)             ────► WalkLink /tm, capture t1;
//	                                                                t1 - t0 ∈ [3600 - slack, 3600 + slack]
//	Step 6 (a TM_TIME_ADJUSTED LogEvent is recorded on the     ────► srv.Stores.LogEvents.List(ctx, "sdev", ...)
//	         SelfDevice scope)                                       len(items) == 1,
//	                                                                FunctionSet == FunctionSetTime,
//	                                                                LogEventCode != 0,
//	                                                                strings.Contains(Details, "TM_TIME_ADJUSTED"),
//	                                                                Href has "/sdev/log/" prefix
//
// Race / hermeticity notes:
// The clock offset is a package-global atomic.Int64 in
// `coresep2time` (`pkg/sep2srv/handlers/sep2time/time_test_hook.go`); every CORE-006 invocation
// must reset it before and after to keep parallel-test independence.
// `coresep2time.ResetClockOffset()` does both via t.Cleanup. `t.Parallel()`
// is intentionally NOT called: this test mutates that package global,
// so running it serial-against-other-tagged-tests is the safe choice.
// Other tests in this file's package do not touch the offset, so they
// stay parallel; only THIS test foregoes t.Parallel.
//
// Token discipline:
// The mutation surface requires both the csip_test_hooks build tag AND
// a non-empty SEP2_TEST_MUTATION_TOKEN env var matching the
// X-CSIP-Test-Token header (see internal/server/test_mutations.go's
// tokenAuthMiddleware). t.Setenv scopes the token to this test process
// and t.Cleanup restores the prior value.

package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresep2time "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	// core006TestToken is the X-CSIP-Test-Token header value used to
	// authenticate the time-advance mutation request. Matches the env
	// var SEP2_TEST_MUTATION_TOKEN set via t.Setenv.
	core006TestToken = "core-006-mutation-token"

	// core006SelfDeviceScope is the sentinel parent key under which the
	// #28 hook stores SelfDevice-scoped LogEvents. Mirrors
	// internal/server/test_mutations.go's selfDeviceLogScope constant.
	core006SelfDeviceScope = "sdev"

	// core006AdvanceSeconds is the clock shift CSIP V1.2 §5.8 prescribes.
	core006AdvanceSeconds = 3600

	// core006TimeSlackSeconds bounds the tolerance window on the
	// before/after currentTime delta. Two seconds covers handler-clock
	// drift between the two GET /tm calls plus the int64 Unix-second
	// truncation at each boundary. Tightening below 2s starts flaking
	// on shared CI runners; widening past 5s starts to mask real bugs.
	core006TimeSlackSeconds = 2
)

// TestCORE_006_AdvancedTime exercises CSIP V1.2 §5.8.
//
// Build tag: csip_test_hooks (required for the #28 time-advance
// hook to compile and for the mutation surface to be registered).
func TestCORE_006_AdvancedTime(t *testing.T) {
	// No t.Parallel: this test mutates the handler-package's global
	// clock offset. See doc comment "Race / hermeticity notes".

	// Token discipline: set env before BootServer so the router picks
	// it up at construction time inside RegisterMutationHandlers.
	t.Setenv("SEP2_TEST_MUTATION_TOKEN", core006TestToken)

	// Reset the clock offset on both ends. Before-reset guards against
	// pollution from any prior tagged test that didn't clean up; after-
	// reset is t.Cleanup, restoring the global for the next test.
	coresep2time.ResetClockOffset()
	t.Cleanup(coresep2time.ResetClockOffset)

	// Step 1: boot a CSIP server with our own PKI so the test's
	// *http.Client can present a device cert and authenticate the
	// mutation POST through the ACL chain.
	_, caCertFile, deviceCert := mustBuildClientPKI(t)
	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(deviceCert),
	)
	httpClient := buildClient(t, srv.RootCA, deviceCert)
	ctx := context.Background()

	// Step 2: GET /tm via the chained-GET helper. dcap.TimeLink MUST be
	// advertised (CSIP §5.7); CORE-006 builds directly on CORE-005's
	// foundation.
	dcap, err := srv.Client().GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("step 2: GET /dcap: %v", err)
	}
	if dcap.TimeLink == nil || dcap.TimeLink.Href == "" {
		t.Fatal("step 2: dcap.TimeLink missing or empty; cannot walk /tm")
	}
	var tmBefore sep2.Time
	if err := srv.Client().WalkLink(ctx, *dcap.TimeLink, &tmBefore); err != nil {
		t.Fatalf("step 2: WalkLink /tm (before): %v", err)
	}
	if tmBefore.CurrentTime == 0 {
		t.Fatal("step 2: pre-advance Time.CurrentTime is 0; server is not reporting wallclock")
	}

	// Step 3 + 4: POST /test/mutations/time-advance and read the audit
	// envelope. The mutation surface returns 200 with a small JSON body
	// carrying log_event_href, current_time, and offset_seconds. Assert
	// offset_seconds matches our requested shift (sanity check that the
	// hook applied the shift we asked for, not a different value).
	advanceResp := postTimeAdvance(t, ctx, httpClient, srv.BaseURL, core006AdvanceSeconds)
	if advanceResp.OffsetSeconds != core006AdvanceSeconds {
		t.Errorf("step 4: advance audit envelope offset_seconds = %d, want %d",
			advanceResp.OffsetSeconds, core006AdvanceSeconds)
	}
	if advanceResp.LogEventHref == "" {
		t.Error("step 4: advance audit envelope missing log_event_href")
	} else if !strings.HasPrefix(advanceResp.LogEventHref, "/sdev/log/") {
		t.Errorf("step 4: log_event_href = %q, want /sdev/log/ prefix", advanceResp.LogEventHref)
	}

	// Step 5: GET /tm again. The advertised currentTime must now reflect
	// the shift. We assert on the delta (t1 - t0) rather than absolute
	// values so the test is independent of wall-clock drift between the
	// two reads.
	var tmAfter sep2.Time
	if err := srv.Client().WalkLink(ctx, *dcap.TimeLink, &tmAfter); err != nil {
		t.Fatalf("step 5: WalkLink /tm (after): %v", err)
	}
	delta := tmAfter.CurrentTime - tmBefore.CurrentTime
	if delta < core006AdvanceSeconds-core006TimeSlackSeconds {
		t.Errorf("step 5: post-advance Time delta = %ds, want >= %ds (advance %d - slack %d). "+
			"before=%d, after=%d",
			delta, core006AdvanceSeconds-core006TimeSlackSeconds,
			core006AdvanceSeconds, core006TimeSlackSeconds,
			tmBefore.CurrentTime, tmAfter.CurrentTime)
	}
	if delta > core006AdvanceSeconds+core006TimeSlackSeconds {
		t.Errorf("step 5: post-advance Time delta = %ds, want <= %ds (advance %d + slack %d). "+
			"before=%d, after=%d",
			delta, core006AdvanceSeconds+core006TimeSlackSeconds,
			core006AdvanceSeconds, core006TimeSlackSeconds,
			tmBefore.CurrentTime, tmAfter.CurrentTime)
	}

	// Step 6: a TM_TIME_ADJUSTED LogEvent landed in the SelfDevice
	// LogEventList. The SelfDevice LogEventListLink isn't exposed over
	// HTTP today (the production /sdev resource doesn't carry it), so
	// observation goes through the store handle BootServer surfaces.
	// This mirrors the lower-level assertion in
	// internal/server/test_mutations_test.go::TestTimeAdvance_EmitsTimeAdjustedLogEvent.
	logResult, err := srv.Stores.LogEvents.List(ctx, core006SelfDeviceScope, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("step 6: list SelfDevice LogEvents: %v", err)
	}
	if len(logResult.Items) != 1 {
		t.Fatalf("step 6: SelfDevice LogEvent count = %d, want 1", len(logResult.Items))
	}
	got := logResult.Items[0]
	if got.FunctionSet != sep2.FunctionSetTime {
		t.Errorf("step 6: LogEvent.FunctionSet = %d, want %d (FunctionSetTime)",
			got.FunctionSet, sep2.FunctionSetTime)
	}
	if got.LogEventCode == 0 {
		t.Errorf("step 6: LogEvent.LogEventCode = 0, want non-zero TM_TIME_ADJUSTED sentinel")
	}
	if !strings.Contains(got.Details, "TM_TIME_ADJUSTED") {
		t.Errorf("step 6: LogEvent.Details = %q, want to contain TM_TIME_ADJUSTED", got.Details)
	}
	if !strings.HasPrefix(got.Href, "/sdev/log/") {
		t.Errorf("step 6: LogEvent.Href = %q, want /sdev/log/ prefix", got.Href)
	}
}

// timeAdvanceAuditEnvelope models the JSON response body of the
// /test/mutations/time-advance endpoint. Mirrors the inline map[string]any
// the handler in internal/server/test_mutations.go emits; defined as a
// named type so the test reads fields by name rather than by string key.
type timeAdvanceAuditEnvelope struct {
	LogEventHref  string `json:"log_event_href"`
	CurrentTime   int64  `json:"current_time"`
	OffsetSeconds int64  `json:"offset_seconds"`
}

// postTimeAdvance issues a POST against /test/mutations/time-advance with
// the X-CSIP-Test-Token header set and the {"seconds": N} body. Returns
// the parsed audit envelope on 200; t.Fatal on any failure path. Body is
// drained + closed before return — the http.Client is shared with
// downstream steps and must not hold a half-read response open.
func postTimeAdvance(t *testing.T, ctx context.Context, c *http.Client, baseURL string, seconds int64) timeAdvanceAuditEnvelope {
	t.Helper()

	body, err := json.Marshal(map[string]any{"seconds": seconds})
	if err != nil {
		t.Fatalf("postTimeAdvance: marshal body: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/test/mutations/time-advance", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("postTimeAdvance: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSIP-Test-Token", core006TestToken)

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("postTimeAdvance: do: %v", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		// Read up to 1 KiB of the body so a 4xx/5xx surfaces a useful
		// error message instead of bare status code.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.Fatalf("postTimeAdvance: status = %d, want 200; body = %s",
			resp.StatusCode, strings.TrimSpace(string(preview)))
	}

	var env timeAdvanceAuditEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("postTimeAdvance: decode audit envelope: %v", err)
	}
	return env
}
