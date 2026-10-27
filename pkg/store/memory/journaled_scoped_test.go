package memory_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

func TestJournaledScopedStoreSatisfiesScopedStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunScopedStoreSuite(t, func(t *testing.T) store.ScopedStore[storetest.Resource] {
		return openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	})
}

// journalOpenedAt is the floor a journal opened by these tests starts with.
const journalOpenedAt = 1000

func openJournal(t *testing.T, path string) *memory.JournaledScopedStore[storetest.Resource] {
	t.Helper()
	s, err := memory.NewJournaledScopedStore[storetest.Resource](path, "Resource", journalOpenedAt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// held is every record of s by parent, ids and values in list order.
func held(t *testing.T, s store.ScopedReader[storetest.Resource]) map[string][]storetest.Resource {
	t.Helper()
	ctx := context.Background()
	parents, err := s.Parents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]storetest.Resource{}
	for _, p := range parents {
		page, err := s.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 0 {
			out[p] = page.Items
		}
	}
	return out
}

func equalHeld(a, b map[string][]storetest.Resource) bool {
	if len(a) != len(b) {
		return false
	}
	for p, items := range a {
		if !slices.EqualFunc(items, b[p], func(x, y storetest.Resource) bool {
			return x.ID == y.ID && x.Body == y.Body && slices.Equal(x.Tags, y.Tags)
		}) {
			return false
		}
	}
	return true
}

// Every write survives a reopen: the same parents, ids and values.
func TestJournaledScopedStore_ReopenHoldsTheSameRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, w := range []struct{ parent, id, body string }{
		{"dev1/a", "1", "one"}, {"dev1/a", "2", "two"}, {"dev1/b", "1", "three"},
		{"dev2", "1", "four"}, {"dev2", "2", "five"}, {"dev3", "1", "six"},
	} {
		if err := s.Create(ctx, w.parent, w.id, storetest.Resource{ID: w.id, Body: w.body, Tags: []string{w.parent}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(ctx, "dev2", "1", storetest.Resource{ID: "1", Body: "four, updated"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "dev2", "2"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteParentsWithPrefix(ctx, "dev1/"); err != nil || n != 3 {
		t.Fatalf("DeleteParentsWithPrefix = %d, %v; want 3", n, err)
	}
	if n, err := s.DeleteParent(ctx, "dev3"); err != nil || n != 1 {
		t.Fatalf("DeleteParent = %d, %v; want 1", n, err)
	}
	want := map[string][]storetest.Resource{"dev2": {{ID: "1", Body: "four, updated"}}}
	if got := held(t, s); !equalHeld(got, want) {
		t.Fatalf("held = %+v, want %+v", got, want)
	}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after reopen = %+v, want %+v", got, want)
	}
	if err := s.Create(ctx, "dev2", "1", storetest.Resource{ID: "1"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("duplicate create = %v, want ErrAlreadyExists", err)
	}
}

// A torn last line, an altered line and a create of an id already held are
// dropped, and the journal is rewritten without them.
func TestJournaledScopedStore_DamageIsDroppedAndRewritten(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, id := range []string{"1", "2", "3"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id, Body: "body " + id}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	// After the floor line: record 2 altered, record 1 repeated, and the
	// last line torn.
	altered := strings.Replace(lines[2], "body 2", "body X", 1)
	damaged := lines[0] + lines[1] + altered + lines[1] + lines[3][:len(lines[3])/2]
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	r := openJournal(t, path)
	want := map[string][]storetest.Resource{"p": {{ID: "1", Body: "body 1"}}}
	if got := held(t, r); !equalHeld(got, want) {
		t.Fatalf("after damage = %+v, want %+v", got, want)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(rewritten) != lines[0]+lines[1] {
		t.Errorf("journal after open = %q, want the floor and record 1 %q", rewritten, lines[0]+lines[1])
	}
}

// A write whose append fails is not stored, and the journal is rewritten
// from memory before the next write, so the failure leaves nothing behind.
func TestJournaledScopedStore_FailedAppendIsUndone(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "j.jsonl")
	s := openJournal(t, path)
	if err := s.Create(ctx, "p", "1", storetest.Resource{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "q", "1", storetest.Resource{ID: "1"}); err == nil {
		t.Fatal("create with no journal directory succeeded")
	}
	if err := s.Delete(ctx, "p", "1"); err == nil {
		t.Fatal("delete with no journal directory succeeded")
	}
	if n, err := s.DeleteParent(ctx, "p"); err == nil || n != 0 {
		t.Fatalf("DeleteParent with no journal directory = %d, %v; want 0 and an error", n, err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "1"}}}
	if got := held(t, s); !equalHeld(got, want) {
		t.Fatalf("after failed writes = %+v, want %+v", got, want)
	}
	if ok, _ := s.HasParent(ctx, "q"); ok {
		t.Error("a failed create left its parent behind")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "p", "2", storetest.Resource{ID: "2"}); err != nil {
		t.Fatal(err)
	}
	want["p"] = append(want["p"], storetest.Resource{ID: "2"})
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after recovery and reopen = %+v, want %+v", got, want)
	}
}

// Compact keeps what keep picks on disk, drops nothing from memory, and
// leaves the journal unchanged when keep fails.
func TestJournaledScopedStore_Compact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, id := range []string{"1", "2", "3", "4"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	all := held(t, s)
	if err := s.Compact(ctx, 0, func(string, []storetest.Resource) ([]bool, error) { return nil, errors.New("no") }); err == nil {
		t.Fatal("a failing keep compacted")
	}
	if err := s.Compact(ctx, 0, func(string, []storetest.Resource) ([]bool, error) { return []bool{true}, nil }); err == nil {
		t.Fatal("a keep answering 1 of 4 records compacted")
	}
	if got := held(t, openJournal(t, path)); !equalHeld(got, all) {
		t.Fatalf("a failed compaction changed the journal: %+v", got)
	}
	var seen []string
	if err := s.Compact(ctx, 0, func(parent string, items []storetest.Resource) ([]bool, error) {
		keep := make([]bool, len(items))
		for i, r := range items {
			seen = append(seen, r.ID)
			keep[i] = r.ID >= "3"
		}
		return keep, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"1", "2", "3", "4"}) {
		t.Errorf("keep saw %v, want the ids ascending", seen)
	}
	if got := held(t, s); !equalHeld(got, all) {
		t.Errorf("compaction changed memory: %+v", got)
	}
	if err := s.Delete(ctx, "p", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "3"}, {ID: "4"}}}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after compaction, a delete of a dropped record and reopen = %+v, want %+v", got, want)
	}
}

// The floor: a new journal starts at its open time, a compaction moves it to
// keepFrom but never below the floor the replay started from, and a reopen
// reports the floor the journal carries.
func TestJournaledScopedStore_Floor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	if got := s.CompleteFrom(); got != journalOpenedAt {
		t.Fatalf("new journal floor = %d, want %d", got, journalOpenedAt)
	}
	keepAll := func(_ string, items []storetest.Resource) ([]bool, error) {
		keep := make([]bool, len(items))
		for i := range keep {
			keep[i] = true
		}
		return keep, nil
	}
	if err := s.Compact(ctx, journalOpenedAt-500, keepAll); err != nil {
		t.Fatal(err)
	}
	if got := openJournal(t, path).CompleteFrom(); got != journalOpenedAt {
		t.Errorf("floor after compacting below it = %d, want %d", got, journalOpenedAt)
	}
	if err := s.Compact(ctx, 5000, keepAll); err != nil {
		t.Fatal(err)
	}
	if s.CompleteFrom() != journalOpenedAt {
		t.Errorf("a compaction moved the floor of the store in memory to %d", s.CompleteFrom())
	}
	if got := openJournal(t, path).CompleteFrom(); got != 5000 {
		t.Errorf("floor after compacting to 5000 = %d, want 5000", got)
	}
}

// A batch is one line: a torn batch line drops every record of the batch.
func TestJournaledScopedStore_TornBatchDropsTheWholeBatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	if err := s.Create(ctx, "p", "0", storetest.Resource{ID: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAll(ctx, "p", []string{"1", "2", "3"}, []storetest.Resource{{ID: "1"}, {ID: "2"}, {ID: "3"}}); err != nil {
		t.Fatal(err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "0"}, {ID: "1"}, {ID: "2"}, {ID: "3"}}}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Fatalf("after a batch and reopen = %+v, want %+v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-20], 0o600); err != nil {
		t.Fatal(err)
	}
	want = map[string][]storetest.Resource{"p": {{ID: "0"}}}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after a torn batch = %+v, want %+v", got, want)
	}
	if err := s.CreateAll(ctx, "p", []string{"9", "0"}, []storetest.Resource{{ID: "9"}, {ID: "0"}}); err == nil {
		t.Error("a batch holding an id already stored succeeded")
	}
	if _, err := s.Get(ctx, "p", "9"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused batch left its first record: %v", err)
	}
}

// A journal whose record lines are all bad is kept aside, not deleted, and a
// new one starts empty.
func TestJournaledScopedStore_AllBadIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.jsonl")
	bad := []byte("{\"op\":\"c\",\"p\":\"x\",\"id\":\"1\",\"k\":1}\nnot json\n")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	s := openJournal(t, path)
	if got := held(t, s); len(got) != 0 {
		t.Fatalf("held = %+v, want nothing", got)
	}
	aside, err := os.ReadFile(path + ".damaged-1000")
	if err != nil || string(aside) != string(bad) {
		t.Fatalf("damaged journal kept as %q, %v; want the original bytes", aside, err)
	}
	if err := s.Create(context.Background(), "p", "1", storetest.Resource{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if got := held(t, openJournal(t, path)); len(got["p"]) != 1 {
		t.Errorf("new journal after reopen = %+v, want one record", got)
	}
}

// Writers run while a compaction is staged, and what they wrote meanwhile is
// in the compacted journal.
func TestJournaledScopedStore_CompactDoesNotHoldWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, id := range []string{"1", "2"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	inKeep, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Compact(ctx, 0, func(_ string, items []storetest.Resource) ([]bool, error) {
			close(inKeep)
			<-release
			return []bool{false, true}, nil
		})
	}()
	<-inKeep
	created := make(chan error, 1)
	go func() {
		err := s.Create(ctx, "p", "3", storetest.Resource{ID: "3"})
		if err == nil {
			err = s.Delete(ctx, "p", "2")
		}
		created <- err
	}()
	select {
	case err := <-created:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write waited for the compaction")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "3"}}}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after the compaction and reopen = %+v, want %+v (record 1 dropped, 2 deleted, 3 written meanwhile)", got, want)
	}
}

// Under -race: writers and repeated keep-all compactions together lose no
// write; the reopened journal equals memory.
func TestJournaledScopedStore_CompactAgainstWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	keepAll := func(_ string, items []storetest.Resource) ([]bool, error) {
		keep := make([]bool, len(items))
		for i := range keep {
			keep[i] = true
		}
		return keep, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				parent := fmt.Sprintf("p%d", w)
				id := fmt.Sprintf("%03d", i)
				if err := s.Create(ctx, parent, id, storetest.Resource{ID: id}); err != nil {
					errs <- err
					return
				}
				if i%3 == 0 {
					if err := s.Delete(ctx, parent, id); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			if err := s.Compact(ctx, 0, keepAll); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	mem := held(t, s)
	if len(mem) != 4 || len(mem["p0"]) != 33 {
		t.Fatalf("memory = %d parents, p0 %d records; want 4 and 33", len(mem), len(mem["p0"]))
	}
	if got := held(t, openJournal(t, path)); !equalHeld(got, mem) {
		t.Errorf("reopened journal differs from memory")
	}
}

// A failed sync marks the journal for a rewrite: the next write replaces it
// from memory before appending.
func TestJournaledScopedStore_FailedSyncRewrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	if err := s.Create(ctx, "p", "1", storetest.Resource{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	memory.SetJournalFault(s, func() error { return errors.New("disk gone") })
	if err := s.Sync(); err == nil {
		t.Fatal("a failing sync succeeded")
	}
	memory.SetJournalFault(s, nil)
	// A line the failed sync may have left torn.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"c","p":"p","id":"9"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := s.Create(ctx, "p", "2", storetest.Resource{ID: "2"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"id":"9"`) {
		t.Errorf("the journal was appended to without a rewrite: %s", data)
	}
}

const faultDevice = "DEVICE_A_LFDI"

func faultBatchBody() []byte {
	var b strings.Builder
	b.WriteString(`<MirrorMeterReadingList xmlns="urn:ieee:std:2030.5:ns" all="3" results="3">`)
	for i := range 3 {
		fmt.Fprintf(&b, `<MirrorMeterReading><mRID>M%d</mRID><Reading><value>%d</value></Reading></MirrorMeterReading>`, i, 100*i)
	}
	b.WriteString(`</MirrorMeterReadingList>`)
	return []byte(b.String())
}

// hideCreateAll is the store as a plain store.ScopedStore, so the POST
// handler takes its create-then-roll-back loop.
type hideCreateAll struct {
	store.ScopedStore[sep2.MirrorMeterReading]
}

// A disk fault that persists through a three-reading POST: the one-step batch
// answers 500 with nothing stored, in memory or after a restart. The control
// takes the handler's per-reading loop with the fault from the second write
// on, and shows the readings the rollback could not remove.
func TestJournaledScopedStore_BatchPostUnderAPersistingFault(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hide      bool
		failAfter int
		want      uint32
	}{
		{"one-step batch", false, 0, 0},
		{"control: per-reading loop", true, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "j.jsonl")
			js := openJournalOf[sep2.MirrorMeterReading](t, path)
			mups := memory.NewStore[sep2.MirrorUsagePoint]()
			if err := mups.Create(ctx, "m1", sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/m1"}, MRID: "MUP", DeviceLFDI: faultDevice}); err != nil {
				t.Fatal(err)
			}
			writes := 0
			memory.SetJournalFault(js, func() error {
				writes++
				if writes > tc.failAfter {
					return errors.New("disk gone")
				}
				return nil
			})
			var mmrs store.ScopedStore[sep2.MirrorMeterReading] = js
			if tc.hide {
				mmrs = hideCreateAll{js}
			}
			mux := http.NewServeMux()
			mux.HandleFunc("POST /mup/{id}/mr", metering.HandlePostMirrorMeterReading(mups, mmrs, nil, func(context.Context) (string, bool) { return faultDevice, true }))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup/m1/mr", bytes.NewReader(faultBatchBody())))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("POST = %d, want 500; body %s", w.Code, w.Body.String())
			}
			if n, err := js.Count(ctx, "m1"); err != nil || n != tc.want {
				t.Errorf("readings in memory = %d, %v; want %d", n, err, tc.want)
			}
			if n, _ := openJournalOf[sep2.MirrorMeterReading](t, path).Count(ctx, "m1"); n != tc.want {
				t.Errorf("readings after a restart = %d, want %d", n, tc.want)
			}
		})
	}
}

func openJournalOf[T store.Copier[T]](t *testing.T, path string) *memory.JournaledScopedStore[T] {
	t.Helper()
	s, err := memory.NewJournaledScopedStore[T](path, "J", journalOpenedAt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
