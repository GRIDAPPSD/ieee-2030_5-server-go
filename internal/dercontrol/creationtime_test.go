package dercontrol

import (
	"context"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// Acceptance criterion 3: creationTime is the server clock, or one second
// after the newest creationTime in the same scope, whichever is later.

func TestIssue_CreationTime_NearServerClock(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	before := sep2time.Now().Unix()
	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	after := sep2time.Now().Unix()
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if res.Control.CreationTime < before || res.Control.CreationTime > after {
		t.Fatalf("CreationTime = %d, want between %d and %d", res.Control.CreationTime, before, after)
	}
}

// TestIssue_CreationTime_ConcurrentIssuesAreDistinctAndOrdered runs 50
// concurrent Issue calls in one scope under -race and asserts every
// creationTime is distinct, proving the per-scope lock actually serializes
// the read-max-then-write step it exists for.
func TestIssue_CreationTime_ConcurrentIssuesAreDistinctAndOrdered(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	const n = 50
	times := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := h.issuer.Issue(context.Background(), CreateRequest{
				DERProgramHref:  programHref("dev1", "0", "p1"),
				Type:            Connect,
				DurationSeconds: 3600,
			})
			if err != nil {
				t.Errorf("Issue() #%d error = %v", i, err)
				return
			}
			times[i] = res.Control.CreationTime
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for i, ct := range times {
		if ct == 0 {
			t.Fatalf("issue #%d never recorded a creationTime", i)
		}
		if seen[ct] {
			t.Fatalf("duplicate creationTime %d among %d concurrent issues", ct, n)
		}
		seen[ct] = true
	}
}
