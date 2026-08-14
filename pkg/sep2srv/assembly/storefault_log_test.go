package assembly_test

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// Every 500 on a store failure carries a log line naming its route.
//
// The status assertion in storefault_route_test.go proves the CLIENT is told
// the truth. This proves the OPERATOR is. They are different failures with
// different costs: a wrong status misleads one client about one resource,
// whereas an unlogged 500 leaves nobody able to say what broke at all.
//
// The shape of the outage this is written for: with a durable backend a
// store that stops answering produces 500s across the whole
// surface at once, and every one of them carries the same three-word body. The
// only thing that separates "the backend is down" from "one client is
// provoking errors on one route" is the server-side log, and only if every
// 500 is in it. Before this suite the log lines were uneven: some paths logged
// and some did not, with no rule, so their presence recorded which afternoon a
// handler was written rather than anything about the failure.
//
// The route list is the SAME mechanically derived partition the status
// assertion uses, so a route mounted tomorrow is required to log from the day
// it is mounted, and a route cannot be dropped from log coverage without
// failing the partition guard that both tests share.

// logProbeSafeBuffer is an io.Writer this test can read while the server's
// handler goroutines are still writing to it.
//
// The standard logger serialises its own writes but nothing serialises them
// against this test's read, so a plain bytes.Buffer would be a data race under
// -race rather than a flake that shows up once a month.
type logProbeSafeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *logProbeSafeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logProbeSafeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *logProbeSafeBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// clientSuppliedText extracts the character data from a probe document.
//
// It is deliberately mechanical rather than a hand-kept list of secrets: a
// probe body added to faultProbeBodies tomorrow has its values checked for
// leakage without anybody remembering to extend a list, which is the same
// reason the route table is derived rather than written out.
//
// The 8-character floor drops structural noise ("true", "0", "60") that would
// match a log line by coincidence and say nothing. What it keeps is the class
// that matters: mRIDs, LFDIs, and anything else long enough to identify a
// device or a caller.
var xmlTextNode = regexp.MustCompile(`>([^<>]{8,})<`)

func clientSuppliedText(body string) []string {
	var out []string
	for _, m := range xmlTextNode.FindAllStringSubmatch(body, -1) {
		if text := strings.TrimSpace(m[1]); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// TestEveryStoreFailureFiveHundredIsLoggedWithItsRoute drives every probed
// route through the fault injector and requires each 500 to leave exactly the
// record an operator needs, and nothing more.
//
// It does NOT call t.Parallel, and that is load-bearing rather than an
// oversight. Redirecting the standard logger is process-wide, and Go runs
// every sequential top-level test to completion before releasing the parallel
// ones, so a sequential test is the only place a process-wide capture can be
// read without the sibling suites in this package writing into it.
//
// The per-route token is the second guard on the same problem. Each route is
// probed with its OWN error value, and the assertion requires the line to
// carry that route's token, so a line left behind by some other request cannot
// be mistaken for this route's. Without it a test could pass on log lines it
// did not cause, which is the "green because it measured nothing" failure the
// vacuity guard exists to refuse elsewhere.
func TestEveryStoreFailureFiveHundredIsLoggedWithItsRoute(t *testing.T) {
	stores, fault := faultyStores()

	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	part, err := partitionMountedRoutes(patterns, storeFreeRoutes)
	if err != nil {
		t.Fatalf("route coverage: %v", err)
	}

	buf := &logProbeSafeBuffer{}
	prevFlags, prevOut := log.Flags(), log.Writer()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	logged := 0
	for i, pattern := range part.Probed {
		// Unique per route, so a line this request did not produce can
		// never satisfy this route's assertion.
		token := fmt.Sprintf("faultprobe-%03d", i)
		fault.Arm(fmt.Errorf("%w [%s]", storetest.ErrBackendUnavailable, token))

		req, err := probeRequestFor(srv.URL, pattern)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}

		buf.Reset()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		captured := buf.String()

		if status != http.StatusInternalServerError {
			// The 5xx contract itself is asserted next door. Here a
			// non-500 only means the log obligation does not apply to
			// this response, so it is reported and skipped rather than
			// failed twice.
			t.Logf("%s answered %d rather than 500; the log assertion does not apply", pattern, status)
			continue
		}
		logged++

		wantPrefix := srverr.LogLinePrefix(pattern)
		if !containsLineWith(captured, wantPrefix, token) {
			t.Errorf("%s answered 500 and logged no line naming it.\n"+
				"want a line beginning %q and carrying %q\ngot:\n%s\n"+
				"A 500 with no log line tells an operator that something failed and nothing about what, "+
				"and under a store outage it is indistinguishable from a client provoking errors",
				pattern, wantPrefix, token, indentOrNone(captured))
		}

		// The other half of the obligation: the line names the route and
		// the error, and nothing the client sent. A log line is a place
		// secrets leak, and this codebase handles a registration pIN.
		leaks := clientSuppliedText(faultProbeBodies[pattern])
		if concrete := concretePath(pattern); concrete != "" {
			leaks = append(leaks, concrete)
		}
		for _, leaked := range leaks {
			if strings.Contains(captured, leaked) {
				t.Errorf("%s logged client-supplied %q.\ngot:\n%s\n"+
					"Only the route pattern and the error may be logged",
					pattern, leaked, indentOrNone(captured))
			}
		}
	}

	// The counter is the vacuity guard for this test specifically: the
	// partition proves the table is complete and the loop above proves each
	// entry logs, but neither notices a run where every route answered
	// something other than 500 and every assertion was skipped.
	if logged == 0 {
		t.Errorf("not one of the %d probed routes answered 500 with the store failing; "+
			"this test asserted nothing", len(part.Probed))
	}
	t.Logf("asserted a route-naming log line on %d of %d probed routes", logged, len(part.Probed))
}

// containsLineWith reports whether any captured line begins with prefix and
// carries token.
//
// Line by line rather than over the whole buffer, because a prefix on one line
// and a token on another is two unrelated log lines, not the one line this
// test is asserting the existence of.
func containsLineWith(captured, prefix, token string) bool {
	for _, line := range strings.Split(captured, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, token) {
			return true
		}
	}
	return false
}

// concretePath returns the request path a wildcard pattern is probed with, or
// "" for a pattern with no wildcards.
//
// A pattern with no wildcard has a path equal to its own shape, so asserting
// the log does not contain it would forbid logging the route pattern, which is
// the one thing this package is required to log.
func concretePath(pattern string) string {
	_, shape, ok := strings.Cut(pattern, " ")
	if !ok || !strings.Contains(shape, "{") {
		return ""
	}
	segments := strings.Split(shape, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			segments[i] = faultProbePathValue
		}
	}
	return strings.Join(segments, "/")
}

// indentOrNone renders captured output for a failure message, naming an empty
// capture rather than printing nothing, since "logged nothing at all" and
// "logged the wrong thing" are different diagnoses.
func indentOrNone(captured string) string {
	if strings.TrimSpace(captured) == "" {
		return "  (nothing was logged)"
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(captured, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}
