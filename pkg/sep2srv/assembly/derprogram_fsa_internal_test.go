package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func seedViewPrograms(t *testing.T, hrefs map[string]string) store.ScopedStore[sep2.DERProgram] {
	t.Helper()
	s := memory.NewScopedStore[sep2.DERProgram]()
	for id, href := range hrefs {
		p := sep2.DERProgram{MRID: "M-" + id}
		p.Href = href
		if err := s.Create(context.Background(), "e1", id, p); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	return s
}

func TestProgramsInFSA_RefusesAnyReadButUnboundedFromStart(t *testing.T) {
	t.Parallel()
	v := programsInFSA{ScopedStore: seedViewPrograms(t, map[string]string{"p1": "/edev/e1/fsa/f1/derp/p1"}), fsaID: "f1",
		warn: newUnscopedProgramWarnings(t.Logf)}
	for _, opts := range []store.ListOptions{
		{Limit: 2},
		{Start: 1, Limit: 2},
		{Unbounded: true, Start: 1},
		{Unbounded: true, After: "p0"},
	} {
		if _, err := v.List(context.Background(), "e1", opts); !errors.Is(err, store.ErrInvalidListOptions) {
			t.Errorf("List(%+v) err = %v, want ErrInvalidListOptions", opts, err)
		}
	}
	res, err := v.List(context.Background(), "e1", store.ListOptions{Unbounded: true})
	if err != nil || res.All != 1 || res.Results != 1 || len(res.Items) != 1 {
		t.Errorf("unbounded List = %+v, %v; want the one f1 program", res, err)
	}
}

// partialStore answers an unbounded List with fewer items than its All.
type partialStore struct {
	store.ScopedStore[sep2.DERProgram]
}

func (partialStore) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	p := sep2.DERProgram{}
	p.Href = "/edev/e1/fsa/f1/derp/p1"
	return store.ListResult[sep2.DERProgram]{All: 3, Results: 1, Items: []sep2.DERProgram{p}}, nil
}

func TestProgramsInFSA_RefusesAPartialCollection(t *testing.T) {
	t.Parallel()
	v := programsInFSA{ScopedStore: partialStore{memory.NewScopedStore[sep2.DERProgram]()}, fsaID: "f1",
		warn: newUnscopedProgramWarnings(t.Logf)}
	if _, err := v.List(context.Background(), "e1", store.ListOptions{Unbounded: true}); err == nil || !strings.Contains(err.Error(), "partial collection") {
		t.Errorf("List over a partial read: err = %v, want the partial collection refusal", err)
	}
	if _, err := v.Count(context.Background(), "e1"); err == nil {
		t.Error("Count over a partial read: err = nil, want the refusal")
	}
}

func TestProgramsInFSA_CountIsTheFSAsOwnPrograms(t *testing.T) {
	t.Parallel()
	v := programsInFSA{ScopedStore: seedViewPrograms(t, map[string]string{
		"p1": "/edev/e1/fsa/f1/derp/p1", "p2": "/edev/e1/fsa/f1/derp/p2",
		"p3": "/edev/e1/fsa/f2/derp/p3", "p4": "/edev/e1/derp/p4",
	}), fsaID: "f1", warn: newUnscopedProgramWarnings(t.Logf)}
	if n, err := v.Count(context.Background(), "e1"); err != nil || n != 2 {
		t.Errorf("Count = %d, %v; want 2", n, err)
	}
}

// A program whose href names no FSA (the shape written before #743) is in
// no FSA's list and 404s on its route; it is logged once per href, not per
// request.
func TestProgramsInFSA_UnscopedHrefIsServedNowhereAndLoggedOnce(t *testing.T) {
	t.Parallel()
	logs := &logLines{}
	warn := newUnscopedProgramWarnings(logs.logf)
	programs := seedViewPrograms(t, map[string]string{
		"p1": "/edev/e1/fsa/f1/derp/p1", "old": "/edev/e1/derp/old", "older": "",
	})
	ctx := context.Background()
	for _, fsa := range []string{"f1", "f2"} {
		v := programsInFSA{ScopedStore: programs, fsaID: fsa, warn: warn}
		for range 2 {
			res, err := v.List(ctx, "e1", store.ListOptions{Unbounded: true})
			if err != nil {
				t.Fatalf("List %s: %v", fsa, err)
			}
			for _, p := range res.Items {
				if p.MRID != "M-p1" {
					t.Errorf("FSA %s lists %q, want only M-p1", fsa, p.MRID)
				}
			}
			if _, err := v.Get(ctx, "e1", "old"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Get old under %s: err = %v, want ErrNotFound", fsa, err)
			}
		}
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.lines) != 2 {
		t.Fatalf("log lines = %d %q, want one per unscoped href (2)", len(logs.lines), logs.lines)
	}
	joined := strings.Join(logs.lines, "\n")
	for _, want := range []string{`"/edev/e1/derp/old"`, `href ""`, `"e1"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs %q do not name %s", logs.lines, want)
		}
	}
}
