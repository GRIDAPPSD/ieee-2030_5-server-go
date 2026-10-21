// Package idminttest holds the shared check that a handler minting its own
// ids gives every one of many simultaneous creates a distinct id.
package idminttest

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/idmint"
)

// AssertParallelCreatesDistinct holds the id clock still, then serves n
// requests from newReq concurrently through h. Every response must be 201
// with a Location no other response carries, which is the collision #1010
// saw when two creates read one clock value. The caller must not be
// parallel, because the clock is process-wide, and newReq must build a
// fresh request on each call.
func AssertParallelCreatesDistinct(t *testing.T, h http.Handler, n int, newReq func() *http.Request) []string {
	t.Helper()
	t.Cleanup(idmint.SwapClock(func() time.Time { return time.Unix(1700000000, 42) }))

	codes := make([]int, n)
	locs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newReq())
			codes[i], locs[i] = rec.Code, rec.Header().Get("Location")
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i := range n {
		if codes[i] != http.StatusCreated {
			t.Errorf("request %d status = %d, want 201", i, codes[i])
			continue
		}
		if locs[i] == "" || seen[locs[i]] {
			t.Errorf("request %d Location = %q, empty or repeated", i, locs[i])
		}
		seen[locs[i]] = true
	}
	return locs
}
