package assembly_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// CSIP V1.2 section 8.27, Alarms (LogEvent), as a LINK WALK.
//
// This is the assertion the routing and the advertisement halves exist to
// make possible, and it is deliberately written the way a conforming client
// behaves rather than the way a test with prior knowledge of the URL space
// would: every address after /dcap is READ OFF THE PREVIOUS RESPONSE. No
// step hardcodes /lel. That is what makes the walk evidence rather than
// decoration, because a server that served the list at the right address
// while advertising nothing would pass a hardcoded test and fail this one at
// step 2, which is the exact state this once found.
//
// BASIC-027 step 2 is "Using the EndDevice instance, find the LogEventListLink",
// with pass criteria "Client was able to successfully find the LogEventListLink
// in its EndDevice". Before this, no production path assigned that link, so
// step 2 could not pass against this server at any address.
//
// Scope, stated so this file is not mistaken for the whole procedure: the
// server-go repository carries the full CSIP BASIC-027 run
// (test/csip/basic_027_alarms_test.go) over a real TLS listener with device
// certificates. This one covers the half core owns: that the router serves the
// declared addresses and that the EndDevice advertises them.
//
// leGenSoftware is the LogEventCode V1.2 section 8.27 Table 8-27 assigns to the
// "General Software" classification. It is a test-local constant because
// pkg/sep2 does not model the LogEventCode enum; adding it is out of scope
// here.
const leGenSoftware uint8 = 27

// TestBASIC_027_Alarms walks a client from /dcap to a LogEvent instance.
func TestBASIC_027_Alarms(t *testing.T) {
	t.Parallel()

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		testStores(),
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Step 1: the entry point. Every subsequent address comes from a document
	// the server served, never from a literal in this test.
	var dcap sep2.DeviceCapability
	walk(t, srv, "/dcap", &dcap)
	if dcap.EndDeviceListLink == nil {
		t.Fatal("DeviceCapability carries no EndDeviceListLink; the walk cannot start")
	}

	// Step 2a: register, and take the EndDevice's own self href from the
	// Location the server returned.
	resp, err := http.Post(srv.URL+dcap.EndDeviceListLink.Href, "application/sep+xml", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST %s: %v", dcap.EndDeviceListLink.Href, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s status = %d, want 201", dcap.EndDeviceListLink.Href, resp.StatusCode)
	}
	edevHref := resp.Header.Get("Location")
	if edevHref == "" {
		t.Fatal("POST /edev returned no Location")
	}

	// Step 2b, the pass criterion: re-read the EndDevice and FIND the link.
	// Re-read rather than use the 201 body, because a client that restarts
	// arrives here with nothing but its own href.
	var edev sep2.EndDevice
	walk(t, srv, edevHref, &edev)
	if edev.LogEventListLink == nil {
		t.Fatal("BASIC-027 step 2 FAILS: the EndDevice carries no LogEventListLink, " +
			"so a client has no way to discover where to report an alarm")
	}
	logListHref := edev.LogEventListLink.Href
	if logListHref != edevHref+"/lel" {
		t.Errorf("LogEventListLink href = %q, want %q (sep_wadl.xml:1358, 2018 A.3.5.1)", logListHref, edevHref+"/lel")
	}

	// Step 3: report three alarms to the advertised list, each at a distinct
	// createdDateTime, and keep the Location each POST returned.
	const alarmCount = 3
	posted := make([]sep2.LogEvent, 0, alarmCount)
	locations := make([]string, 0, alarmCount)
	for i := 0; i < alarmCount; i++ {
		evt := sampleLogEvent(uint16(100+i), leGenSoftware)
		body, err := xml.Marshal(&evt)
		if err != nil {
			t.Fatalf("marshal LogEvent[%d]: %v", i, err)
		}
		post, err := http.Post(srv.URL+logListHref, "application/sep+xml", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("POST %s [%d]: %v", logListHref, i, err)
		}
		_ = post.Body.Close()
		if post.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s [%d] status = %d, want 201 (sep_wadl.xml:1385 declares POST mode M)",
				logListHref, i, post.StatusCode)
		}
		loc := post.Header.Get("Location")
		if loc == "" {
			t.Fatalf("POST %s [%d] returned no Location", logListHref, i)
		}
		posted = append(posted, evt)
		locations = append(locations, loc)
	}

	// Step 4: dereference the link and read the list.
	var list sep2.LogEventList
	walk(t, srv, logListHref, &list)
	if list.All != alarmCount || list.Results != alarmCount {
		t.Errorf("LogEventList all=%d results=%d, want %d and %d", list.All, list.Results, alarmCount, alarmCount)
	}
	if len(list.LogEvent) != alarmCount {
		t.Fatalf("LogEventList holds %d events, want %d", len(list.LogEvent), alarmCount)
	}

	// Step 5: the list is in time order. V1.2 section 8.27 requires time order
	// and does not pin the direction; this server keys events by a
	// nanosecond-formatted id and the memory store iterates by string key, so
	// the emitted order is ascending. Asserting the observable direction means
	// a change to either the key format or the iteration order fails loudly
	// rather than silently accepting whatever comes out.
	ascending := sort.SliceIsSorted(list.LogEvent, func(a, b int) bool {
		return list.LogEvent[a].CreatedDateTime < list.LogEvent[b].CreatedDateTime
	})
	if !ascending {
		times := make([]int64, len(list.LogEvent))
		for i, le := range list.LogEvent {
			times[i] = le.CreatedDateTime
		}
		t.Errorf("LogEventList is not ascending by createdDateTime; got %v", times)
	}

	// Step 6: every member's identifying fields survive the round trip, and
	// every member's own href RESOLVES. The second half is what the card means
	// by "the Location it returns resolves, verified by following it": the list
	// could be perfect while each member href pointed at nothing.
	byID := make(map[uint16]sep2.LogEvent, alarmCount)
	for _, le := range list.LogEvent {
		byID[le.LogEventID] = le
	}
	for i, want := range posted {
		got, ok := byID[want.LogEventID]
		if !ok {
			t.Errorf("logEventID %d is absent from the served list", want.LogEventID)
			continue
		}
		if got.Href != locations[i] {
			t.Errorf("logEventID %d list href = %q, want the Location the POST returned, %q",
				want.LogEventID, got.Href, locations[i])
		}
		if got.CreatedDateTime != want.CreatedDateTime {
			t.Errorf("logEventID %d createdDateTime = %d, want %d", want.LogEventID, got.CreatedDateTime, want.CreatedDateTime)
		}
		if got.FunctionSet != want.FunctionSet {
			t.Errorf("logEventID %d functionSet = %d, want %d", want.LogEventID, got.FunctionSet, want.FunctionSet)
		}
		if got.LogEventCode != leGenSoftware {
			t.Errorf("logEventID %d logEventCode = %d, want %d (LE_GEN_SOFTWARE)", want.LogEventID, got.LogEventCode, leGenSoftware)
		}
		if got.LogEventPEN != want.LogEventPEN {
			t.Errorf("logEventID %d logEventPEN = %d, want %d", want.LogEventID, got.LogEventPEN, want.LogEventPEN)
		}
		if got.ProfileID != want.ProfileID {
			t.Errorf("logEventID %d profileID = %d, want %d", want.LogEventID, got.ProfileID, want.ProfileID)
		}

		var instance sep2.LogEvent
		walk(t, srv, got.Href, &instance)
		if instance.LogEventID != want.LogEventID {
			t.Errorf("GET %s served logEventID %d, want %d", got.Href, instance.LogEventID, want.LogEventID)
		}
		if instance.Href != got.Href {
			t.Errorf("GET %s served href %q, want %q", got.Href, instance.Href, got.Href)
		}
	}
}

// TestBASIC_027_ListPUTIsOutOfScopeAndRefused records the one method on the
// LogEventList deliberately not implemented here.
//
// PUT is mode E at sep_wadl.xml:1379: the server is required to answer 400 or
// 405, explicitly, per section 4.3 c) 4). Mounting the full WADL-declared
// method set is a separate sweep and is NOT done here.
//
// It is recorded rather than left silent because the two ways to not implement a
// method are not equivalent. A 404 means "there is nothing at this address at
// all" and is indistinguishable from the function set being unmounted, which is
// how an unrouted path passes for a conformant refusal. What this server
// actually returns is asserted below, so the record is the observed status and
// not a claim: a regression to 404, which is exactly what removing the list
// routes would produce, fails here.
func TestBASIC_027_ListPUTIsOutOfScopeAndRefused(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)
	postLogEvent(t, srv, "d1", sampleLogEvent(200, leGenSoftware))

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/edev/d1/lel", strings.NewReader("<LogEvent/>"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /edev/d1/lel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("PUT /edev/d1/lel returned 404: an unmounted path is not a conformant mode E refusal")
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT /edev/d1/lel status = %d, want 405 (mode E, sep_wadl.xml:1379)", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET, HEAD, POST" {
		t.Errorf("PUT /edev/d1/lel Allow = %q, want %q: the Allow header is the client's only "+
			"statement of what this list does serve", got, "GET, HEAD, POST")
	}
}

// walk fetches href and decodes it, failing the test on any non-200. It is the
// one place this file issues a GET, so no step can accidentally tolerate a
// missing link by falling back to a literal path.
func walk(t *testing.T, srv *httptest.Server, href string, dst any) {
	t.Helper()

	status, body := getBytes(t, srv, href)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: the server advertised this href itself", href, status)
	}
	if err := xml.Unmarshal(body, dst); err != nil {
		t.Fatalf("decode %s: %v\nbody: %s", href, err, body)
	}
}
