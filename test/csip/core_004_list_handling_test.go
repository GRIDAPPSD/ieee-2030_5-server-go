// CSIP V1.2 §5.6 — List Handling.
//
// Procedure (paraphrased; full text in V1.2 PDF §5.6):
//
//	Step 1: Server has at least three EndDevice resources advertised
//	        at /edev (see three-edev.yaml fixture).
//	Step 2: GET /edev with various paging query parameters.
//	Step 3: Assert the server honors the s/l/a parameters per spec
//	        §4.6.2 and §5.6:
//	          - `s=<start>` — return items starting at ordinal Start.
//	          - `l=<limit>` — return at most Limit items.
//	          - `a=<after>` — return items strictly after the given key.
//	          - `l=0`       — return zero items (degenerate but valid).
//	          - Duplicate parameters (e.g. `?l=5&l=10`) — server chooses
//	            deterministically. Per Go's net/url.Values.Get this
//	            harness asserts the FIRST value wins; document this
//	            convention so a future implementor does not silently
//	            change it.
//	          - Unknown parameters (e.g. `?foo=bar`) — silently ignored,
//	            full list still returned.
//
// This test validates the paging surface used by every list endpoint in
// the server (the generic handler.ListHandler — see
// internal/handler/listhandler.go). Regressions in paging.ParseQuery
// surface here.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCORE_004_ListHandling exercises CSIP V1.2 §5.6 against a live
// spec server seeded with a 3-EndDevice fixture.
func TestCORE_004_ListHandling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Boot a fresh server and adapt its Stores into a csiptest.Target so
	// the fixture loader writes through the same store the booted server
	// reads from. The adapter is the canonical pattern for Phase 3 tests
	// per #52's loader docs.
	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	if err := csiptest.Load(ctx, target, "fixtures/three-edev.yaml"); err != nil {
		t.Fatalf("Load three-edev.yaml: %v", err)
	}

	srv := csiptest.BootServer(t, csiptest.WithStores(stores))

	// Sanity-check the seeded state via the chained-GET helper before
	// exercising paging. If /edev returns anything other than 3 items
	// at default paging, every subtest below is meaningless.
	baseline := walkEndDevices(ctx, t, srv, "/edev")
	if baseline.All != 3 {
		t.Fatalf("baseline /edev: All = %d, want 3", baseline.All)
	}
	if len(baseline.EndDevice) != 3 {
		t.Fatalf("baseline /edev: len(EndDevice) = %d, want 3", len(baseline.EndDevice))
	}

	t.Run("start_and_limit", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 step: GET /edev?s=0&l=10 → up to 10 items, starting
		// at ordinal 0. With 3 items total, expect all 3.
		list := walkEndDevices(ctx, t, srv, "/edev?s=0&l=10")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if list.Results != 3 {
			t.Errorf("Results = %d, want 3", list.Results)
		}
		if len(list.EndDevice) != 3 {
			t.Fatalf("len(EndDevice) = %d, want 3", len(list.EndDevice))
		}
		wantHrefs := []string{"/edev/0", "/edev/1", "/edev/2"}
		for i, want := range wantHrefs {
			if got := list.EndDevice[i].Href; got != want {
				t.Errorf("EndDevice[%d].Href = %q, want %q", i, got, want)
			}
		}
	})

	t.Run("start_offset", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 step: GET /edev?s=1 → skip the first item.
		// Default limit is 10, so expect EndDevices 1 and 2.
		list := walkEndDevices(ctx, t, srv, "/edev?s=1")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if list.Results != 2 {
			t.Errorf("Results = %d, want 2", list.Results)
		}
		if len(list.EndDevice) != 2 || list.EndDevice[0].Href != "/edev/1" || list.EndDevice[1].Href != "/edev/2" {
			t.Errorf("EndDevices = %+v, want [/edev/1, /edev/2]", list.EndDevice)
		}
	})

	t.Run("limit_zero_returns_empty", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 edge: GET /edev?l=0 → zero items, but All still
		// reflects the full count so paging-aware clients know there
		// is more behind the curtain.
		list := walkEndDevices(ctx, t, srv, "/edev?l=0")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if list.Results != 0 {
			t.Errorf("Results = %d, want 0", list.Results)
		}
		if len(list.EndDevice) != 0 {
			t.Errorf("len(EndDevice) = %d, want 0", len(list.EndDevice))
		}
	})

	t.Run("after_key_returns_strictly_greater", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 step: GET /edev?a=0 → return items whose store key
		// is strictly greater than "0" → IDs "1" and "2".
		list := walkEndDevices(ctx, t, srv, "/edev?a=0")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if len(list.EndDevice) != 2 || list.EndDevice[0].Href != "/edev/1" || list.EndDevice[1].Href != "/edev/2" {
			t.Errorf("EndDevices = %+v, want [/edev/1, /edev/2]", list.EndDevice)
		}
	})

	t.Run("duplicate_limit_first_value_wins", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 + Go net/url.Values.Get convention: when the same
		// parameter appears twice (?l=1&l=10), this server takes the
		// FIRST occurrence — matches stdlib Get(), no extra dedup
		// logic. Documented behavior, not bikeshed material.
		//
		// Asserting `Results == 1` proves the first value (l=1) won;
		// if the second value (l=10) had won we would see 3 items.
		list := walkEndDevices(ctx, t, srv, "/edev?l=1&l=10")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if list.Results != 1 {
			t.Errorf("Results = %d, want 1 (first value wins)", list.Results)
		}
		if len(list.EndDevice) != 1 || list.EndDevice[0].Href != "/edev/0" {
			t.Errorf("EndDevices = %+v, want [/edev/0]", list.EndDevice)
		}
	})

	t.Run("unknown_param_ignored", func(t *testing.T) {
		t.Parallel()
		// V1.2 §5.6 step: unknown query parameters are ignored.
		// GET /edev?foo=bar must behave identically to GET /edev.
		list := walkEndDevices(ctx, t, srv, "/edev?foo=bar")
		if list.All != 3 {
			t.Errorf("All = %d, want 3", list.All)
		}
		if list.Results != 3 {
			t.Errorf("Results = %d, want 3", list.Results)
		}
		if len(list.EndDevice) != 3 {
			t.Errorf("len(EndDevice) = %d, want 3", len(list.EndDevice))
		}
	})
}

// walkEndDevices issues GET <baseURL>+href via the booted server's
// csiptest.Client and parses the response as an EndDeviceList. The
// href argument carries the path AND the query string; WalkLink
// concatenates it onto baseURL untouched, so query parameters reach
// the server intact.
func walkEndDevices(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, href string) sep2.EndDeviceList {
	t.Helper()

	var list sep2.EndDeviceList
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: href}, &list); err != nil {
		t.Fatalf("WalkLink %s: %v", href, err)
	}
	return list
}
