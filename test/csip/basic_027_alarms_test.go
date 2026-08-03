// CSIP V1.2 section 8.27 -- Alarms (LogEvent).
//
// Five LogEvent instances are POSTed to the LogEventListLink DISCOVERED off
// the registered EndDevice, with general-software classification
// (logEventCode = 27 == LE_GEN_SOFTWARE per V1.2 section 8.27 Table 8-27);
// each is assigned a monotonically increasing createdDateTime so the test
// can assert chronological order on the subsequent GET. The GET returns a
// LogEventList which the test parses and validates for: total count,
// exact-match round-trip on each posted LogEvent's identifying fields, and
// ascending-by-createdDateTime ordering across the result set.
//
// Link-walk note (IEEESRV-034):
// No step in this file hardcodes the LogEvent list address. V1.2 section
// 8.27 step 2 of the BASIC-027 procedure is "Using the EndDevice instance,
// find the LogEventListLink", with pass criteria "Client was able to
// successfully find the LogEventListLink in its EndDevice". Earlier
// revisions of this test built the address as fmt.Sprintf("/edev/%s/log",
// edevID): a literal that happened to match wherever the server served the
// list at the time. That is precisely the shape of test that cannot detect
// a server advertising a different address than it serves (IEEECORE-084:
// the WADL-declared address was /lel, the server served /log, and nothing
// advertised either). This test now registers an EndDevice, re-reads it,
// and reads LogEventListLink.Href off the response -- the same discipline
// as the core-side walk in
// pkg/sep2srv/assembly/basic_027_alarms_test.go.
//
// Ordering choice -- note for reviewers:
// V1.2 section 8.27 says the LogEventList must be returned in time order
// but does NOT pin which direction. The IEEE-2030.5 server stores
// LogEvents under a 20-digit nanosecond-formatted ID (see
// pkg/sep2srv/handlers/logevent HandlePostLogEvent:
// id := fmt.Sprintf("%020d", time.Now().UnixNano())) and pkg/store/memory
// iterates by string key, so the natural emission order is ASCENDING
// (oldest first). This test asserts ascending order to match the server's
// observable behaviour; if the spec is ever tightened to mandate
// descending the test fails loudly rather than silently accepting either
// direction.
//
// V1.2 procedure step -> assertion mapping:
//
//	Step 1 (boot CSIP server)                          ----> csiptest.BootServer
//	Step 2 (register an EndDevice, re-read it, find
//	        the LogEventListLink)                       ----> POST EndDeviceListLink.Href,
//	                                                          GET edevHref, read LogEventListLink
//	Step 3 (POST 5 LogEvent instances at distinct
//	        createdDateTime values, each LE_GEN_SOFTWARE) -> POST logListHref (x5)
//	Step 4 (each POST returns 201 with Location header) --> assert resp.StatusCode == 201
//	                                                        and resp.Header["Location"] != ""
//	Step 5 (GET logListHref returns LogEventList
//	        with all 5 entries)                          ---> GET, parse, len == 5
//	Step 6 (LogEventList entries are returned in time
//	        order -- ascending by createdDateTime today)  --> assert sort.SliceIsSorted ascending
//	Step 7 (every entry round-trips its identifying
//	        fields: functionSet, logEventCode, logEventID,
//	        logEventPEN, profileID, createdDateTime)      ---> per-entry field match
//
// Race notes:
// BASIC-027 uses t.Parallel(). pkg/sep2srv/handlers/logevent
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

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// leGenSoftware is the LogEventCode value V1.2 section 8.27 specifies for
// the "General Software" alarm classification. Replicated as a test-local
// constant rather than added to pkg/sep2 because (a) the upstream repo's
// LogEventCode constants are not modelled today and (b) Pike's hard rule
// #1 forbids unrelated production changes; introducing a new public
// constant for the test alone is out of scope. If a future ticket adds
// the full LogEventCode enum to pkg/sep2, this file becomes a one-line
// s/leGenSoftware/sep2.LogEventCodeGenSoftware/.
const leGenSoftware uint8 = 27

// TestBASIC_027_Alarms implements CSIP V1.2 section 8.27.
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

	// Step 2: register an EndDevice, then re-read it and find the
	// LogEventListLink. No address below this point is a literal --
	// every one is read off a response the server itself produced.
	var dcap sep2.DeviceCapability
	if err := getAndUnmarshal(ctx, t, httpClient, srv.BaseURL+"/dcap", &dcap); err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatal("DeviceCapability carries no EndDeviceListLink; the walk cannot start")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+dcap.EndDeviceListLink.Href, nil)
	if err != nil {
		t.Fatalf("build POST %s: %v", dcap.EndDeviceListLink.Href, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", dcap.EndDeviceListLink.Href, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: status = %d, want 201", dcap.EndDeviceListLink.Href, resp.StatusCode)
	}
	edevHref := resp.Header.Get("Location")
	if edevHref == "" {
		t.Fatal("POST EndDeviceListLink returned no Location")
	}

	// Re-read rather than trust the 201 body: a client that restarts
	// arrives here with nothing but its own href, and the pass
	// criterion is finding the link in a GET response.
	var edev sep2.EndDevice
	if err := getAndUnmarshal(ctx, t, httpClient, srv.BaseURL+edevHref, &edev); err != nil {
		t.Fatalf("GET %s: %v", edevHref, err)
	}
	if edev.LogEventListLink == nil || edev.LogEventListLink.Href == "" {
		t.Fatal("BASIC-027 step 2 FAILS: the EndDevice carries no LogEventListLink, " +
			"so a client has no way to discover where to report an alarm")
	}
	logListHref := edev.LogEventListLink.Href

	// Step 3 + 4: POST 5 LogEvent instances with monotonically
	// increasing createdDateTime. Each carries a distinct LogEventID
	// (1..5) and the LE_GEN_SOFTWARE code (27 per V1.2 section 8.27). We
	// stagger createdDateTime by 1s so the time-ordering assertion in
	// step 6 is unambiguous; the FunctionSet field (10 == LogEvent
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

		postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+logListHref, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build POST [%d]: %v", i, err)
		}
		postReq.Header.Set("Content-Type", "application/sep+xml")

		postResp, err := httpClient.Do(postReq)
		if err != nil {
			t.Fatalf("POST LogEvent[%d]: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, postResp.Body)
		_ = postResp.Body.Close()

		if postResp.StatusCode != http.StatusCreated {
			t.Fatalf("POST LogEvent[%d]: status = %d, want 201", i, postResp.StatusCode)
		}
		if postResp.Header.Get("Location") == "" {
			t.Errorf("POST LogEvent[%d]: missing Location header", i)
		}

		posted = append(posted, le)

		// Force distinct nanosecond-resolution storage keys. The server
		// keys LogEvents under fmt.Sprintf("%020d", time.Now().UnixNano())
		// -- on a fast machine two POSTs inside the same nanosecond would
		// collide on store.ErrAlreadyExists. The 1ms gap is well below
		// the 1s createdDateTime spacing the assertion uses, and keeps
		// the test's wall-clock cost trivial (~5ms total for the loop).
		time.Sleep(time.Millisecond)
	}

	// Step 5: GET logListHref returns a LogEventList containing all
	// five posted entries.
	var list sep2.LogEventList
	if err := getAndUnmarshal(ctx, t, httpClient, srv.BaseURL+logListHref, &list); err != nil {
		t.Fatalf("step 5: GET %s: %v", logListHref, err)
	}
	if got := len(list.LogEvent); got != logCount {
		t.Fatalf("step 5: LogEventList.LogEvent count = %d, want %d", got, logCount)
	}
	if list.All != logCount {
		t.Errorf("step 5: LogEventList.All = %d, want %d", list.All, logCount)
	}
	if list.Results != logCount {
		t.Errorf("step 5: LogEventList.Results = %d, want %d", list.Results, logCount)
	}

	// Step 6: entries are time-ordered (ascending; see doc-comment
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
		t.Fatalf("step 6: LogEventList not ascending by createdDateTime; got %v", times)
	}

	// Step 7: every posted LogEvent round-trips its identifying
	// fields. Match by LogEventID (unique across the set by
	// construction) so an ordering-bug doesn't fan out into 5
	// confusing field-mismatch failures.
	byID := make(map[uint16]sep2.LogEvent, logCount)
	for _, le := range list.LogEvent {
		byID[le.LogEventID] = le
	}
	wantHrefPrefix := logListHref + "/"
	for _, want := range posted {
		got, ok := byID[want.LogEventID]
		if !ok {
			t.Errorf("step 7: LogEventID=%d not present in response", want.LogEventID)
			continue
		}
		if got.CreatedDateTime != want.CreatedDateTime {
			t.Errorf("step 7: LogEventID=%d createdDateTime = %d, want %d", want.LogEventID, got.CreatedDateTime, want.CreatedDateTime)
		}
		if got.FunctionSet != want.FunctionSet {
			t.Errorf("step 7: LogEventID=%d functionSet = %d, want %d", want.LogEventID, got.FunctionSet, want.FunctionSet)
		}
		if got.LogEventCode != want.LogEventCode {
			t.Errorf("step 7: LogEventID=%d logEventCode = %d, want %d (LE_GEN_SOFTWARE)", want.LogEventID, got.LogEventCode, want.LogEventCode)
		}
		if got.LogEventPEN != want.LogEventPEN {
			t.Errorf("step 7: LogEventID=%d logEventPEN = %d, want %d", want.LogEventID, got.LogEventPEN, want.LogEventPEN)
		}
		if got.ProfileID != want.ProfileID {
			t.Errorf("step 7: LogEventID=%d profileID = %d, want %d", want.LogEventID, got.ProfileID, want.ProfileID)
		}
		// Href is server-assigned. We do not assert its exact value
		// (timestamp-suffixed) but we do require it points under the
		// LogEventListLink we discovered, so a future store-key-format
		// change doesn't silently break clients walking the link.
		if len(got.Href) < len(wantHrefPrefix) || got.Href[:len(wantHrefPrefix)] != wantHrefPrefix {
			t.Errorf("step 7: LogEventID=%d href = %q, want prefix %q", want.LogEventID, got.Href, wantHrefPrefix)
		}
	}
}

// getAndUnmarshal issues a GET against url and unmarshals the response
// body as XML into dest, returning a wrapped error on any transport
// failure or non-200 status rather than calling t.Fatalf directly, so
// callers can attach their own step-numbered context. Used for both the
// step-2 link discovery (DeviceCapability, EndDevice) and the step-5
// LogEventList read: one GET-and-decode path for the whole file, so a
// future change to error wrapping or timeout handling lands in one place.
func getAndUnmarshal(ctx context.Context, t *testing.T, client *http.Client, url string, dest any) error {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build GET %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body for GET %s: %w", url, err)
	}
	if err := xml.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("unmarshal body for GET %s: %w", url, err)
	}
	return nil
}
