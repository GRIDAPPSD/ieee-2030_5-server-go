package metering_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Log-reading tests for the two /mup refusal lines (#720 round 2, items 3
// and 4). None of these runs in parallel: each swaps the process-wide log
// output, the same discipline the /edev denial-log tests already use
// (denial_log_test.go).

func captureMirrorLog(t *testing.T) *bytes.Buffer {
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

// TestHandleMirrorUsagePoint_DenialLogIsRateLimitedAndNamesTheCaller
// reproduces the round-2 finding directly: "one outsider sent 1000 refused
// GETs and got 1000 lines." At mirrorDenialPerCaller lines per caller
// (5), 995 of these 1000 refusals must be silent, and every line that IS
// written must name the caller, not just the mirror id, so an operator
// reading the log can tell who to block.
func TestHandleMirrorUsagePoint_DenialLogIsRateLimitedAndNamesTheCaller(t *testing.T) {
	const owner = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"
	const outsider = "9999888877776666555544443333222211110000"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	if err := s.Create(context.Background(), "target", sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/target"},
		MRID:       "TARGET",
		DeviceLFDI: owner,
	}); err != nil {
		t.Fatalf("seed MirrorUsagePoint: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(s, nil, identityProvider(outsider)))
	buf := captureMirrorLog(t)

	const attempts = 1000
	for i := 0; i < attempts; i++ {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/target", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want 403", i, w.Code)
		}
	}

	written := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "mup: denied") {
			continue
		}
		written++
		if !strings.Contains(line, `caller="`+outsider+`"`) {
			t.Errorf("denial line does not name the caller; line = %q", line)
		}
	}
	if written != 5 {
		t.Errorf("%d attempts wrote %d denial lines, want 5 (mirrorDenialPerCaller): the log is not rate-limited", attempts, written)
	}
}

// TestHandleCreateMirrorUsagePoint_DenialLogIsRateLimitedAndNamesTheCaller
// is the create-path twin: a different call site (HandleCreateMirrorUsagePoint's
// own device-claim refusal), a different mirrorDenialLog instance, so the
// budget has to be proven there separately.
func TestHandleCreateMirrorUsagePoint_DenialLogIsRateLimitedAndNamesTheCaller(t *testing.T) {
	const device = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"
	const outsider = "9999888877776666555544443333222211110000"

	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(outsider), nil))
	buf := captureMirrorLog(t)

	body := mupWireBody("REPEATED_CLAIM", "x", device)
	const attempts = 1000
	for i := 0; i < attempts; i++ {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want 403", i, w.Code)
		}
	}

	written := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "mup: create refused") {
			continue
		}
		written++
		if !strings.Contains(line, `caller="`+outsider+`"`) {
			t.Errorf("denial line does not name the caller; line = %q", line)
		}
	}
	if written != 5 {
		t.Errorf("%d attempts wrote %d denial lines, want 5 (mirrorDenialPerCaller): the log is not rate-limited", attempts, written)
	}
	if count, _ := s.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0: a refused claim, rate-limited or not, must never be stored", count)
	}
}
