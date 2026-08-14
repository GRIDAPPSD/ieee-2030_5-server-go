package assembly_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// The router under GETs for parents that do not exist.
//
// The scoped store used to create a per-parent bucket on read, and every parent
// id on the scoped surface arrives as a path segment the client chose. So an
// authenticated client could grow the server's memory without bound by issuing
// GETs against parent ids it invented, storing nothing retrievable: the buckets
// themselves were the leak, with no eviction and no ceiling.
//
// The store-level statement of this is in pkg/store/memory. This file is the
// REACHABILITY half: it proves the growth was drivable over HTTP, and that it
// no longer is, on every route the router actually mounts. That matters because
// the reachable surface roughly doubled in one session, as the DER, LogEvent,
// FlowReservation, TextMessage and MirrorUsagePoint instance routes each added
// a fresh path along which an arbitrary parent key reaches the store.
//
// The route list is DERIVED from BuildProtocolRouter's own pattern enumeration,
// never hand-written, for the reason storefault_route_test.go states: a
// hand-written list keeps passing while the surface it claims to cover grows
// underneath it.

// noallocProbeRepeats is how many times each route is probed, each time with a
// DIFFERENT path value. One probe per route would not distinguish a store that
// allocates per distinct key from one that does not allocate at all, which is
// the whole question.
const noallocProbeRepeats = 50

// parentEnumerator is the read-only slice of store.ScopedReader this test
// needs. It is declared here, at the consumer, so the test does not have to
// name each scoped store's element type to count its parents.
type parentEnumerator interface {
	Parents(context.Context) ([]string, error)
}

// scopedStoresIn returns every field of Stores that can enumerate parents,
// keyed by field name.
//
// It reflects over the struct rather than listing the fields, so a scoped store
// added tomorrow is counted the day it is wired rather than the day somebody
// remembers to extend a list. A hand-written list here would be the same
// vacuity hazard the fault table's partition guard exists to refuse.
func scopedStoresIn(s *assembly.Stores) map[string]parentEnumerator {
	found := make(map[string]parentEnumerator)

	v := reflect.ValueOf(s).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanInterface() {
			continue
		}
		switch f.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func:
			if f.IsNil() {
				continue
			}
		}
		if pe, ok := f.Interface().(parentEnumerator); ok {
			found[v.Type().Field(i).Name] = pe
		}
	}
	return found
}

// parentCensus is the per-store parent count, taken across every scoped store
// at once so that a probe cannot leak into a store the test forgot to watch.
func parentCensus(t *testing.T, stores map[string]parentEnumerator) map[string]int {
	t.Helper()

	ctx := context.Background()
	census := make(map[string]int, len(stores))
	for name, s := range stores {
		parents, err := s.Parents(ctx)
		if err != nil {
			t.Fatalf("%s.Parents: %v", name, err)
		}
		census[name] = len(parents)
	}
	return census
}

// noallocProbeRequestFor turns a mounted pattern into a GET whose every
// wildcard carries a value nothing was ever stored under.
//
// The value embeds the iteration so each probe of a route names a different
// parent. It is prefixed rather than bare because several handlers treat an
// empty or malformed segment as a 400 before any store is read, and a probe
// that never reaches the store would report coverage it did not have.
func noallocProbeRequestFor(base, pattern string, iteration int) (*http.Request, bool, error) {
	method, shape, ok := strings.Cut(pattern, " ")
	if !ok {
		return nil, false, fmt.Errorf("pattern %q has no method", pattern)
	}
	if method != http.MethodGet {
		return nil, false, nil
	}

	segments := strings.Split(shape, "/")
	wildcards := 0
	for i, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			wildcards++
			segments[i] = "noalloc" + strconv.Itoa(iteration) + "x" + strconv.Itoa(wildcards)
		}
	}
	if wildcards == 0 {
		// A route with no wildcard carries no client-chosen key, so there is
		// nothing here for a client to grow the store with.
		return nil, false, nil
	}

	req, err := http.NewRequest(method, base+strings.Join(segments, "/"), nil)
	if err != nil {
		return nil, false, err
	}
	return req, true, nil
}

// TestGetsForUnknownParentsCreateNoStoreState is the route-level statement of
// the defect: reads must not be a write primitive a client can drive.
//
// The assertion is the parent census before and after, per store. Status codes
// are deliberately NOT asserted: a 404, a 403 and an empty 200 are all fine
// answers to a GET for something that is not there, and pinning them here would
// duplicate the route tests that own each shape while making this test fail for
// reasons that have nothing to do with allocation.
func TestGetsForUnknownParentsCreateNoStoreState(t *testing.T) {
	t.Parallel()

	stores := testStores()
	handler, patterns := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), testSFDI, testLFDI, nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	if len(patterns) == 0 {
		t.Fatal("the router reported no mounted patterns; this test would cover nothing and pass")
	}

	scoped := scopedStoresIn(stores)
	// The count is a floor, not an equality: it exists so that a Stores whose
	// scoped fields stopped being reachable through reflection cannot leave a
	// green test that watched nothing.
	if len(scoped) < 15 {
		t.Fatalf("found %d scoped store(s) on assembly.Stores, want at least 15; "+
			"a census over almost nothing would pass while the leak was wide open", len(scoped))
	}

	before := parentCensus(t, scoped)

	probed, routes := 0, 0
	for _, pattern := range patterns {
		routeProbed := false
		for i := 0; i < noallocProbeRepeats; i++ {
			req, wanted, err := noallocProbeRequestFor(srv.URL, pattern, i)
			if err != nil {
				t.Errorf("%s: %v", pattern, err)
				break
			}
			if !wanted {
				break
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("%s: %v", pattern, err)
				break
			}
			_ = resp.Body.Close()
			probed++
			routeProbed = true
		}
		if routeProbed {
			routes++
		}
	}

	if routes == 0 {
		t.Fatal("no wildcard GET route was probed; the enumeration produced nothing to test")
	}
	t.Logf("drove %d GET(s) across %d wildcard route(s) of %d mounted, over %d scoped store(s)",
		probed, routes, len(patterns), len(scoped))

	after := parentCensus(t, scoped)

	grown := make([]string, 0, len(after))
	for name, n := range after {
		if n != before[name] {
			grown = append(grown, fmt.Sprintf("%s: %d -> %d", name, before[name], n))
		}
	}
	if len(grown) > 0 {
		sort.Strings(grown)
		t.Errorf("%d GET(s) for parents that do not exist grew %d store(s): %s. "+
			"The parent key is a client-chosen path segment, so this is unbounded client-driven growth",
			probed, len(grown), strings.Join(grown, ", "))
	}
}

// TestListOfAnExistingButEmptyParentStillServesAnEmptyList guards the invariant
// the fix could most easily trade away at the route level: a collection that
// exists and holds nothing answers 200 with an empty list, not 404.
//
// It uses the LogEvent list, which is scoped by a client-chosen {id} and is one
// of the routes mounted this session. The parent is made real by posting a
// LogEvent and deleting it, so the emptiness is genuine rather than an absence.
func TestListOfAnExistingButEmptyParentStillServesAnEmptyList(t *testing.T) {
	t.Parallel()

	srv, _ := lelServer(t)

	loc := postLogEvent(t, srv, "edev-with-no-events-left", sampleLogEvent(1, 3))

	req, err := http.NewRequest(http.MethodDelete, srv.URL+loc, nil)
	if err != nil {
		t.Fatalf("build DELETE %s: %v", loc, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", loc, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE %s status = %d, want 204 or 200", loc, resp.StatusCode)
	}

	status, body := getBytes(t, srv, "/edev/edev-with-no-events-left/lel")
	if status != http.StatusOK {
		t.Fatalf("GET the emptied LogEventList status = %d, want 200: an existing collection that holds "+
			"nothing is empty, not absent", status)
	}
	if !strings.Contains(string(body), `all="0"`) {
		t.Errorf("emptied LogEventList body = %s, want all=\"0\"", body)
	}
}
