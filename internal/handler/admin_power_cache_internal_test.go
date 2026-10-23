package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// countingMUPs counts List calls, the read that copies every mirror, and can
// hold each one open until gate is closed.
type countingMUPs struct {
	store.ResourceReader[sep2.MirrorUsagePoint]
	lists atomic.Int32
	gate  chan struct{}
}

func (c *countingMUPs) List(ctx context.Context, o store.ListOptions) (store.ListResult[sep2.MirrorUsagePoint], error) {
	c.lists.Add(1)
	if c.gate != nil {
		<-c.gate
	}
	return c.ResourceReader.List(ctx, o)
}

func (p *powerHarness) counted(gate chan struct{}) (http.HandlerFunc, *countingMUPs) {
	c := &countingMUPs{ResourceReader: p.mups, gate: gate}
	h := &AdminFleetHandler{Managers: p.mgr, MirrorUsagePoints: c, MirrorMeterReadings: p.mmrs}
	return HandlePower(h, 0), c
}

func serve(h http.HandlerFunc, query string) (int, string) {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?"+query, nil))
	return w.Code, w.Body.String()
}

// Identical polls arriving together share one listing of the mirror store,
// and a later poll inside the TTL reuses the answer.
func TestPower_ConcurrentIdenticalPollsListOnce(t *testing.T) {
	p := newPowerHarness(t)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 1000})
	gate := make(chan struct{})
	h, c := p.counted(gate)

	const polls = 32
	bodies := make([]string, polls)
	var started, done sync.WaitGroup
	started.Add(polls)
	done.Add(polls)
	for i := range polls {
		go func() {
			defer done.Done()
			started.Done()
			code, body := serve(h, "scope=all&window=900")
			if code != http.StatusOK {
				t.Errorf("poll %d status %d: %s", i, code, body)
			}
			bodies[i] = body
		}()
	}
	started.Wait()
	close(gate)
	done.Wait()

	for i, b := range bodies {
		if b != bodies[0] || !strings.Contains(b, powerD1) {
			t.Fatalf("poll %d body differs or lacks the device: %s", i, b)
		}
	}
	if code, body := serve(h, "scope=all&window=900"); code != http.StatusOK || body != bodies[0] {
		t.Fatalf("later poll = %d %s, want the shared answer", code, body)
	}
	if n := c.lists.Load(); n != 1 {
		t.Fatalf("mirror listings = %d, want 1 for %d identical polls", n, polls+1)
	}
}

// An answer is keyed on scope, aggregator and window: a poll never gets
// another key's answer, and each key lists once.
func TestPower_CacheNeverCrossesScopes(t *testing.T) {
	const agg2 = "AAAA000000000000000000000000000000000002"
	p := newPowerHarness(t)
	p.manage(powerD1)
	if err := p.mgr.Assign(context.Background(), agg2, powerD2); err != nil {
		t.Fatal(err)
	}
	rev := sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, [2]int64{powerNow - 100, 1000})
	p.post(powerD2, roleFlagIsDER, 50, rev, [2]int64{powerNow - 100, 2000})
	h, c := p.counted(nil)

	queries := []struct {
		query string
		want  []string
		not   []string
		unit  string
	}{
		{"aggregatorLFDI=" + powerAgg + "&window=900", []string{powerD1}, []string{powerD2}, `"bucketSeconds":5`},
		{"aggregatorLFDI=" + agg2 + "&window=900", []string{powerD2}, []string{powerD1}, `"bucketSeconds":5`},
		{"scope=all&window=900", []string{powerD1, powerD2}, nil, `"bucketSeconds":5`},
		{"scope=all&window=21600", []string{powerD1, powerD2}, nil, `"bucketSeconds":30`},
	}
	for round := range 3 {
		for _, q := range queries {
			code, body := serve(h, q.query)
			if code != http.StatusOK {
				t.Fatalf("%s: status %d: %s", q.query, code, body)
			}
			for _, w := range q.want {
				if !strings.Contains(body, `"name":"`+w+`"`) {
					t.Fatalf("round %d %s: lacks series %s", round, q.query, w)
				}
			}
			for _, n := range q.not {
				if strings.Contains(body, n) {
					t.Fatalf("round %d %s: carries %s from another scope", round, q.query, n)
				}
			}
			if !strings.Contains(body, q.unit) {
				t.Fatalf("round %d %s: lacks %s", round, q.query, q.unit)
			}
		}
	}
	if n := c.lists.Load(); n != int32(len(queries)) {
		t.Fatalf("mirror listings = %d, want %d (one per key)", n, len(queries))
	}
}

func TestPowerCache_ExpiryAndFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := newPowerCache(5 * time.Second)
	c.clock = func() time.Time { return now }
	key := powerKey{scope: "all", window: 900}
	calls := 0
	compute := func() (PowerResponse, error) { calls++; return PowerResponse{Scope: fmt.Sprint(calls)}, nil }
	get := func() PowerResponse {
		r, err := c.get(context.Background(), key, compute)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := get(); r.Scope != "1" {
		t.Fatalf("first = %q", r.Scope)
	}
	now = now.Add(4 * time.Second)
	if r := get(); r.Scope != "1" || calls != 1 {
		t.Fatalf("inside TTL = %q after %d computes, want the cached 1", r.Scope, calls)
	}
	now = now.Add(2 * time.Second)
	if r := get(); r.Scope != "2" || calls != 2 {
		t.Fatalf("past TTL = %q after %d computes, want a fresh 2", r.Scope, calls)
	}

	failing := powerKey{scope: "all", window: 3600}
	boom := errors.New("boom")
	n := 0
	for range 2 {
		if _, err := c.get(context.Background(), failing, func() (PowerResponse, error) { n++; return PowerResponse{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	}
	if n != 2 {
		t.Fatalf("failing computes = %d, want 2: a failure is never kept", n)
	}
}

// The cached answer is the same bytes a fresh read gives.
func TestPower_CachedBodyEqualsFreshBody(t *testing.T) {
	p := newPowerHarness(t)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 1000})
	h, _ := p.counted(nil)
	_, first := serve(h, "scope=all&window=900")
	_, second := serve(h, "scope=all&window=900")
	_, fresh := p.get("scope=all&window=900")
	if first != second || !bytes.Equal([]byte(first), fresh) {
		t.Fatalf("bodies differ:\n%s\n%s\n%s", first, second, fresh)
	}
}
