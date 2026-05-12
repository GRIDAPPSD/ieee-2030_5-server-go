// CSIP V1.2 §8.27 — Alarms (LogEvent).
//
// BASIC-027 exercises the LogEvent function set end-to-end. Five
// LogEvent instances are POSTed to /edev/{id}/log with general-software
// classification (logEventCode = 27 == LE_GEN_SOFTWARE per V1.2 §8.27
// Table 8-27); each is assigned a monotonically increasing
// createdDateTime so the test can assert chronological order on the
// subsequent GET. The GET returns a LogEventList which the test parses
// and validates for: total count, exact-match round-trip on each
// posted LogEvent's identifying fields, and ascending-by-createdDateTime
// ordering across the result set.
//
// Ordering choice — note for reviewers:
// V1.2 §8.27 says the LogEventList must be returned in time order but
// does NOT pin which direction. The IEEE-2030.5 server stores LogEvents
// under a 20-digit nanosecond-formatted ID (see internal/handler/log_event.go
// HandlePostLogEvent: id := fmt.Sprintf("%020d", time.Now().UnixNano()))
// and pkg/store/memory iterates by string key, so the natural emission
// order is ASCENDING (oldest first). This test asserts ascending order
// to match the server's observable behaviour; if the spec is ever
// tightened to mandate descending the test fails loudly rather than
// silently accepting either direction.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (boot CSIP server)                          ────► csiptest.BootServer
//	Step 2 (POST 5 LogEvent instances at distinct
//	        createdDateTime values, each LE_GEN_SOFTWARE) ► POST /edev/{id}/log (×5)
//	Step 3 (each POST returns 201 with Location header) ──► assert resp.StatusCode == 201
//	                                                        and resp.Header["Location"] != ""
//	Step 4 (GET /edev/{id}/log returns LogEventList
//	        with all 5 entries)                         ───► GET, parse, len == 5
//	Step 5 (LogEventList entries are returned in time
//	        order — ascending by createdDateTime today)  ──► assert sort.SliceIsSorted ascending
//	Step 6 (every entry round-trips its identifying
//	        fields: functionSet, logEventCode, logEventID,
//	        logEventPEN, profileID, createdDateTime)     ──► per-entry field match
//
// Race notes:
// BASIC-027 uses t.Parallel(). internal/handler/log_event.go
// HandlePostLogEvent and the underlying memory.ScopedStore[T] use a
// per-store sync.RWMutex (see pkg/store/memory). Five sequential POSTs
// from a single goroutine in this test do not contend with any sibling
// test because csiptest.BootServer gives each test its own listener +
// stores. Verified clean under -race at ship time.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// leGenSoftware is the LogEventCode value V1.2 §8.27 specifies for the
// "General Software" alarm classification. Replicated as a test-local
// constant rather than added to pkg/sep2 because (a) the upstream
// repo's LogEventCode constants are not modelled today and (b) Pike's
// hard rule #1 forbids unrelated production changes; introducing a
// new public constant for the test alone is out of scope. If a future
// ticket adds the full LogEventCode enum to pkg/sep2, this file
// becomes a one-line s/leGenSoftware/sep2.LogEventCodeGenSoftware/.
const leGenSoftware uint8 = 27

// TestBASIC_027_Alarms implements CSIP V1.2 §8.27.
func TestBASIC_027_Alarms(t *testing.T) {
	t.Parallel()

	// Step 1: boot a CSIP server with a CA + device cert under our
	// control so the test can issue POST/GET requests through the ACL
	// chain. csiptest.BootServer's default ephemeral device cert is
	// not reachable to the test (no key exposed), so we follow the
	// CORE-001 pattern: bring our own PKI, hand it to BootServer via
	// WithClientCAsFile + WithClientCert, then build a dedicated
	// *http.Client for the direct POST + GET requests below.
	_, caCertFile, deviceCert := mustBuildClientPKI(t)
	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(deviceCert),
	)
	httpClient := buildClient(t, srv.RootCA, deviceCert)
	ctx := context.Background()

	const edevID = "alarmsdev"
	logPath := fmt.Sprintf("/edev/%s/log", edevID)

	// Step 2 + 3: POST 5 LogEvent instances with monotonically
	// increasing createdDateTime. Each carries a distinct LogEventID
	// (1..5) and the LE_GEN_SOFTWARE code (27 per V1.2 §8.27). We
	// stagger createdDateTime by 1s so the time-ordering assertion in
	// step 5 is unambiguous; the FunctionSet field (10 == LogEvent
	// per sep2.FunctionSetLogEvent) and ProfileID (2 == IEEE-2030.5
	// CSIP profile) are constant across the set.
	baseTime := time.Now().Unix()
	const logCount = 5

	posted := make([]sep2.LogEvent, 0, logCount)
	for i := 0; i < logCount; i++ {
		le := sep2.LogEvent{
			CreatedDateTime: baseTime + int64(i),
			FunctionSet:     sep2.FunctionSetLogEvent,
			LogEventCode:    leGenSoftware,
			LogEventID:      uint16(1 + i),
			LogEventPEN:     54465, // SunSpec PEN, deterministic across the set
			ProfileID:       2,     // CSIP profile
		}
		body, err := xml.Marshal(&le)
		if err != nil {
			t.Fatalf("marshal LogEvent[%d]: %v", i, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+logPath, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build POST [%d]: %v", i, err)
		}
		req.Header.Set("Content-Type", "application/sep+xml")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("POST LogEvent[%d]: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST LogEvent[%d]: status = %d, want 201", i, resp.StatusCode)
		}
		if resp.Header.Get("Location") == "" {
			t.Errorf("POST LogEvent[%d]: missing Location header", i)
		}

		posted = append(posted, le)

		// Force distinct nanosecond-resolution storage keys. The server
		// keys LogEvents under fmt.Sprintf("%020d", time.Now().UnixNano())
		// — on a fast machine two POSTs inside the same nanosecond would
		// collide on store.ErrAlreadyExists. The 1ms gap is well below
		// the 1s createdDateTime spacing the assertion uses, and keeps
		// the test's wall-clock cost trivial (~5ms total for the loop).
		time.Sleep(time.Millisecond)
	}

	// Step 4: GET /edev/{edevID}/log returns a LogEventList containing
	// all five posted entries.
	list := getLogEventList(t, httpClient, srv.BaseURL+logPath)
	if got := len(list.LogEvent); got != logCount {
		t.Fatalf("step 4: LogEventList.LogEvent count = %d, want %d", got, logCount)
	}
	if list.All != logCount {
		t.Errorf("step 4: LogEventList.All = %d, want %d", list.All, logCount)
	}
	if list.Results != logCount {
		t.Errorf("step 4: LogEventList.Results = %d, want %d", list.Results, logCount)
	}

	// Step 5: entries are time-ordered (ascending; see doc-comment
	// "Ordering choice" note above). sort.SliceIsSorted is the
	// idiomatic way to express this; failure mode lists the actual
	// createdDateTime sequence so a reviewer can read it directly.
	sorted := sort.SliceIsSorted(list.LogEvent, func(a, b int) bool {
		return list.LogEvent[a].CreatedDateTime < list.LogEvent[b].CreatedDateTime
	})
	if !sorted {
		times := make([]int64, len(list.LogEvent))
		for i, le := range list.LogEvent {
			times[i] = le.CreatedDateTime
		}
		t.Fatalf("step 5: LogEventList not ascending by createdDateTime; got %v", times)
	}

	// Step 6: every posted LogEvent round-trips its identifying
	// fields. Match by LogEventID (unique across the set by
	// construction) so an ordering-bug doesn't fan out into 5
	// confusing field-mismatch failures.
	byID := make(map[uint16]sep2.LogEvent, logCount)
	for _, le := range list.LogEvent {
		byID[le.LogEventID] = le
	}
	wantHrefPrefix := fmt.Sprintf("/edev/%s/log/", edevID)
	for _, want := range posted {
		got, ok := byID[want.LogEventID]
		if !ok {
			t.Errorf("step 6: LogEventID=%d not present in response", want.LogEventID)
			continue
		}
		if got.CreatedDateTime != want.CreatedDateTime {
			t.Errorf("step 6: LogEventID=%d createdDateTime = %d, want %d", want.LogEventID, got.CreatedDateTime, want.CreatedDateTime)
		}
		if got.FunctionSet != want.FunctionSet {
			t.Errorf("step 6: LogEventID=%d functionSet = %d, want %d", want.LogEventID, got.FunctionSet, want.FunctionSet)
		}
		if got.LogEventCode != want.LogEventCode {
			t.Errorf("step 6: LogEventID=%d logEventCode = %d, want %d (LE_GEN_SOFTWARE)", want.LogEventID, got.LogEventCode, want.LogEventCode)
		}
		if got.LogEventPEN != want.LogEventPEN {
			t.Errorf("step 6: LogEventID=%d logEventPEN = %d, want %d", want.LogEventID, got.LogEventPEN, want.LogEventPEN)
		}
		if got.ProfileID != want.ProfileID {
			t.Errorf("step 6: LogEventID=%d profileID = %d, want %d", want.LogEventID, got.ProfileID, want.ProfileID)
		}
		// Href is server-assigned. We do not assert its exact value
		// (timestamp-suffixed) but we do require it points at this
		// EndDevice's /log scope so a future store-key-format change
		// doesn't silently break clients walking the link.
		if len(got.Href) < len(wantHrefPrefix) || got.Href[:len(wantHrefPrefix)] != wantHrefPrefix {
			t.Errorf("step 6: LogEventID=%d href = %q, want prefix %q", want.LogEventID, got.Href, wantHrefPrefix)
		}
	}
}

// getLogEventList issues GET against url and unmarshals the body as a
// sep2.LogEventList. Inline rather than via csiptest.Client.WalkLink so
// the test can drive the same *http.Client it used for POSTs in step 2
// (csiptest.Client does not expose its underlying *http.Client, and
// we deliberately do not touch csiptest internals — see Pike scope rule).
func getLogEventList(t *testing.T, client *http.Client, url string) sep2.LogEventList {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body for GET %s: %v", url, err)
	}
	var list sep2.LogEventList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal LogEventList from GET %s: %v", url, err)
	}
	return list
}
