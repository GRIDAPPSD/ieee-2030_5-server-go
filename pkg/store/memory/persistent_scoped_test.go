package memory_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

var _ store.ScopedStore[storetest.Resource] = (*memory.PersistentScopedStore[storetest.Resource])(nil)

func newPersistentResources(t *testing.T) (*memory.PersistentScopedStore[storetest.Resource], string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("NewPersistentScopedStore: %v", err)
	}
	return s, path
}

func res(id, body string, tags ...string) storetest.Resource {
	return storetest.Resource{ID: id, Body: body, Tags: tags}
}

func TestPersistentScopedStore_ConformanceSuite(t *testing.T) {
	storetest.RunScopedStoreSuite(t, func(t *testing.T) store.ScopedStore[storetest.Resource] {
		s, _ := newPersistentResources(t)
		return s
	})
}

func TestPersistentScopedStore_EmptyPathIsInMemory(t *testing.T) {
	s, err := memory.NewPersistentScopedStore[storetest.Resource]("", "resources")
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if s.Persists() {
		t.Fatal("Persists() = true for an empty path")
	}
	if !s.RollsBackOnFailure() {
		t.Fatal("RollsBackOnFailure() = false, want true")
	}
	if err := s.Create(context.Background(), "p", "a", res("a", "x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestPersistentScopedStore_PersistsOnceWired(t *testing.T) {
	s, _ := newPersistentResources(t)
	if !s.Persists() {
		t.Fatal("Persists() = false with a path configured")
	}
}

func TestPersistentScopedStore_ReloadRoundTripsTwoParents(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	for _, w := range []struct{ parent, id, body string }{
		{"edev-1", "a", "one"},
		{"edev-1", "b", "two"},
		{"edev-2", "a", "three"},
	} {
		if err := s.Create(ctx, w.parent, w.id, res(w.id, w.body, "t-"+w.body)); err != nil {
			t.Fatalf("Create %s/%s: %v", w.parent, w.id, err)
		}
	}
	if err := s.Update(ctx, "edev-1", "b", res("b", "two-updated", "u")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Delete(ctx, "edev-1", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	parents, err := re.Parents(ctx)
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if !slices.Equal(parents, []string{"edev-1", "edev-2"}) {
		t.Fatalf("Parents = %v, want [edev-1 edev-2]", parents)
	}
	got, err := re.Get(ctx, "edev-1", "b")
	if err != nil {
		t.Fatalf("Get edev-1/b: %v", err)
	}
	if got.Body != "two-updated" || !slices.Equal(got.Tags, []string{"u"}) {
		t.Fatalf("edev-1/b = %+v, want body two-updated tags [u]", got)
	}
	got, err = re.Get(ctx, "edev-2", "a")
	if err != nil {
		t.Fatalf("Get edev-2/a: %v", err)
	}
	if got.Body != "three" || !slices.Equal(got.Tags, []string{"t-three"}) {
		t.Fatalf("edev-2/a = %+v, want body three tags [t-three]", got)
	}
	if _, err := re.Get(ctx, "edev-1", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted edev-1/a after reload: err = %v, want ErrNotFound", err)
	}
	if n, _ := re.Count(ctx, "edev-1"); n != 1 {
		t.Fatalf("Count(edev-1) = %d, want 1", n)
	}
}

func TestPersistentScopedStore_CorruptSnapshotRefusesToLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources"); err == nil {
		t.Fatal("constructor accepted an unknown snapshot version")
	}
}

func TestPersistentScopedStore_FailedCreateIsNotReadable(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	blockPersist(t, path)

	if err := s.Create(ctx, "p", "a", res("a", "x")); err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if _, err := s.Get(ctx, "p", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after failed Create: err = %v, want ErrNotFound", err)
	}
	if n, _ := s.Count(ctx, "p"); n != 0 {
		t.Fatalf("Count = %d, want 0", n)
	}
	if ok, _ := s.HasParent(ctx, "p"); ok {
		t.Fatal("a failed Create left its new parent behind")
	}
	if parents, _ := s.Parents(ctx); len(parents) != 0 {
		t.Fatalf("Parents = %v, want none", parents)
	}
}

func TestPersistentScopedStore_FailedCreateKeepsExistingParentAndSiblings(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "keep")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Create(ctx, "p", "b", res("b", "lost")); err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if _, err := s.Get(ctx, "p", "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get b: err = %v, want ErrNotFound", err)
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil || got.Body != "keep" {
		t.Fatalf("sibling a = %+v, %v; want body keep", got, err)
	}
}

func TestPersistentScopedStore_FailedUpdateRestoresPriorValue(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "before", "t1")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Update(ctx, "p", "a", res("a", "after", "t2")); err == nil {
		t.Fatal("Update succeeded on an unwritable path")
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "before" || !slices.Equal(got.Tags, []string{"t1"}) {
		t.Fatalf("after failed Update = %+v, want body before tags [t1]", got)
	}
}

func TestPersistentScopedStore_FailedDeleteRestoresRecord(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "keep", "t")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Delete(ctx, "p", "a"); err == nil {
		t.Fatal("Delete succeeded on an unwritable path")
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil {
		t.Fatalf("Get after failed Delete: %v", err)
	}
	if got.Body != "keep" || !slices.Equal(got.Tags, []string{"t"}) {
		t.Fatalf("restored record = %+v, want body keep tags [t]", got)
	}

	// The key index must have been restored too, or the next successful
	// snapshot would drop the record from disk.
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "p", "b", res("b", "next")); err != nil {
		t.Fatalf("Create after unblocking: %v", err)
	}
	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := re.Get(ctx, "p", "a"); err != nil {
		t.Fatalf("restored record missing after next snapshot: %v", err)
	}
}

func TestPersistentScopedStore_DuplicateCreateKeepsOriginal(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "first")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Create(ctx, "p", "a", res("a", "second")); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate Create: err = %v, want ErrAlreadyExists", err)
	}
	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := re.Get(ctx, "p", "a")
	if err != nil || got.Body != "first" {
		t.Fatalf("reloaded = %+v, %v; want body first", got, err)
	}
}

func reloadResources(t *testing.T, path string) *memory.PersistentScopedStore[storetest.Resource] {
	t.Helper()
	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return re
}

func mustHave(t *testing.T, s *memory.PersistentScopedStore[storetest.Resource], parent, id, body string) {
	t.Helper()
	got, err := s.Get(context.Background(), parent, id)
	if err != nil {
		t.Fatalf("Get %s/%s: %v", parent, id, err)
	}
	if got.Body != body {
		t.Fatalf("%s/%s body = %q, want %q", parent, id, got.Body, body)
	}
}

// A store reloaded from disk must keep persisting everything it loaded: the
// key index is rebuilt by the load, or the first write after a restart
// snapshots only itself.
func TestPersistentScopedStore_WriteAfterReloadKeepsEarlierRecords(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p1", "a", res("a", "first")); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	s2 := reloadResources(t, path)
	if err := s2.Create(ctx, "p2", "b", res("b", "second")); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	s3 := reloadResources(t, path)
	mustHave(t, s3, "p1", "a", "first")
	mustHave(t, s3, "p2", "b", "second")
}

// A failed Create must leave no stale key behind, or every later snapshot
// would fail reading it and persistence would stay wedged.
func TestPersistentScopedStore_WriteAfterFailedCreateStillPersists(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "kept", res("kept", "seed")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Create(ctx, "p", "failed", res("failed", "x")); err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "p", "next", res("next", "after")); err != nil {
		t.Fatalf("Create after unblocking: %v", err)
	}
	re := reloadResources(t, path)
	mustHave(t, re, "p", "kept", "seed")
	mustHave(t, re, "p", "next", "after")
	if _, err := re.Get(ctx, "p", "failed"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed record on disk: err = %v, want ErrNotFound", err)
	}
}

// Loading reads the snapshot and never writes it: a hand-formatted file must
// be byte-identical after a load, and a load that fails part way must not
// have truncated it.
func TestPersistentScopedStore_LoadNeverWritesTheSnapshot(t *testing.T) {
	good := `{ "version": 1, "records": [
 {"parent":"p","id":"a","value":{"ID":"a","Body":"one"}},
 {"parent":"p","id":"b","value":{"ID":"b","Body":"two"}} ] }`
	dup := `{ "version": 1, "records": [
 {"parent":"p","id":"a","value":{"ID":"a","Body":"one"}},
 {"parent":"p","id":"a","value":{"ID":"a","Body":"dup"}} ] }`
	for name, tc := range map[string]struct {
		content string
		wantErr bool
	}{"good": {good, false}, "duplicate": {dup, true}} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "r.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
			if (err != nil) != tc.wantErr {
				t.Fatalf("constructor err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				mustHave(t, s, "p", "b", "two")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, []byte(tc.content)) {
				t.Fatalf("snapshot rewritten during load:\n%s", after)
			}
		})
	}
}

func TestPersistentScopedStore_LoadRefusesUndecodableRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"records":"not-a-list"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources"); err == nil {
		t.Fatal("constructor accepted records that are not a list")
	}
}

// An emptied parent lingers in memory (store.ScopedReader leaves that
// implementation-defined) and is not written to the snapshot.
func TestPersistentScopedStore_EmptiedParentLingersUntilReload(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p", "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.HasParent(ctx, "p"); !ok {
		t.Fatal("HasParent = false for an emptied parent before reload, want true")
	}
	if n, _ := s.Count(ctx, "p"); n != 0 {
		t.Fatalf("Count = %d, want 0", n)
	}
	re := reloadResources(t, path)
	if ok, _ := re.HasParent(ctx, "p"); ok {
		t.Fatal("HasParent = true after reload, want false")
	}
}

func newPersistentFlowStores(t *testing.T) (reqs *memory.PersistentScopedStore[sep2.FlowReservationRequest], resps *memory.PersistentScopedStore[sep2.FlowReservationResponse], reqPath, respPath string) {
	t.Helper()
	dir := t.TempDir()
	reqPath = filepath.Join(dir, "frq.json")
	respPath = filepath.Join(dir, "frp.json")
	var err error
	if reqs, err = memory.NewPersistentScopedStore[sep2.FlowReservationRequest](reqPath, "frq"); err != nil {
		t.Fatal(err)
	}
	if resps, err = memory.NewPersistentScopedStore[sep2.FlowReservationResponse](respPath, "frp"); err != nil {
		t.Fatal(err)
	}
	return reqs, resps, reqPath, respPath
}

func seedFlowRecords(t *testing.T, reqs store.ScopedStore[sep2.FlowReservationRequest], resps store.ScopedStore[sep2.FlowReservationResponse]) {
	t.Helper()
	ctx := context.Background()
	for _, e := range []string{"1", "2"} {
		if err := reqs.Create(ctx, e, "req-"+e, sep2.FlowReservationRequest{MRID: "req-" + e}); err != nil {
			t.Fatal(err)
		}
		if err := resps.Create(ctx, e, "resp-"+e, sep2.FlowReservationResponse{Subject: "req-" + e}); err != nil {
			t.Fatal(err)
		}
	}
}

// The EndDevice delete cascades through the persistent stores' own
// DeleteParent, so the records stay gone after a restart.
func TestPersistentScopedStore_EndDeviceDeleteCascadePersists(t *testing.T) {
	ctx := context.Background()
	reqs, resps, reqPath, respPath := newPersistentFlowStores(t)
	devs := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore(), reqs, resps)
	dev := sep2.EndDevice{SFDI: "1111111111"}
	dev.Href = "/edev/1"
	if err := devs.Create(ctx, "1", dev); err != nil {
		t.Fatal(err)
	}
	seedFlowRecords(t, reqs, resps)

	if err := devs.Delete(ctx, "1"); err != nil {
		t.Fatalf("EndDevice Delete: %v", err)
	}
	if _, err := devs.Get(ctx, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device after delete: err = %v, want ErrNotFound", err)
	}

	reqs2, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](reqPath, "frq")
	if err != nil {
		t.Fatal(err)
	}
	resps2, err := memory.NewPersistentScopedStore[sep2.FlowReservationResponse](respPath, "frp")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := reqs2.Count(ctx, "1"); n != 0 {
		t.Fatalf("requests under deleted device after reload = %d, want 0", n)
	}
	if n, _ := resps2.Count(ctx, "1"); n != 0 {
		t.Fatalf("responses under deleted device after reload = %d, want 0", n)
	}
	got, err := reqs2.Get(ctx, "2", "req-2")
	if err != nil || got.MRID != "req-2" {
		t.Fatalf("other device's request = %+v, %v; want req-2", got, err)
	}
	if _, err := resps2.Get(ctx, "2", "resp-2"); err != nil {
		t.Fatalf("other device's response: %v", err)
	}
}

// A failed snapshot write refuses the delete and loses nothing: the device,
// both collections and the on-disk state all survive.
func TestPersistentScopedStore_EndDeviceDeleteRefusedWhenSnapshotFails(t *testing.T) {
	ctx := context.Background()
	reqs, resps, reqPath, respPath := newPersistentFlowStores(t)
	devs := memory.NewFlowReservationLinkedEndDeviceStore(memory.NewEndDeviceStore(), reqs, resps)
	for _, id := range []string{"1", "2"} {
		dev := sep2.EndDevice{SFDI: id + id + id + id + id + id + id + id + id + id}
		dev.Href = "/edev/" + id
		if err := devs.Create(ctx, id, dev); err != nil {
			t.Fatal(err)
		}
	}
	seedFlowRecords(t, reqs, resps)
	blockPersist(t, reqPath)

	if err := devs.Delete(ctx, "1"); err == nil {
		t.Fatal("EndDevice Delete succeeded though the snapshot write failed")
	}
	if _, err := devs.Get(ctx, "1"); err != nil {
		t.Fatalf("device lost by a refused delete: %v", err)
	}
	got, err := reqs.Get(ctx, "1", "req-1")
	if err != nil || got.MRID != "req-1" {
		t.Fatalf("request after refused delete = %+v, %v; want req-1", got, err)
	}
	if _, err := resps.Get(ctx, "1", "resp-1"); err != nil {
		t.Fatalf("response after refused delete: %v", err)
	}

	// Rolled-back key index: the next successful write keeps the records.
	if err := os.RemoveAll(reqPath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := reqs.Create(ctx, "2", "req-2b", sep2.FlowReservationRequest{MRID: "req-2b"}); err != nil {
		t.Fatalf("Create after unblocking: %v", err)
	}
	reqs2, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](reqPath, "frq")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reqs2.Get(ctx, "1", "req-1"); err != nil || got.MRID != "req-1" {
		t.Fatalf("restored request missing from the next snapshot: %+v, %v", got, err)
	}
	_ = respPath
}

func TestPersistentScopedStore_DeleteParentOfUnknownParentIsNoOp(t *testing.T) {
	s, _ := newPersistentResources(t)
	n, err := s.DeleteParent(context.Background(), "nope")
	if err != nil || n != 0 {
		t.Fatalf("DeleteParent(unknown) = %d, %v; want 0, nil", n, err)
	}
}

func TestPersistentScopedStore_DeleteParentCountOnSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	for _, id := range []string{"a", "b"} {
		if err := s.Create(ctx, "p", id, res(id, "x")); err != nil {
			t.Fatal(err)
		}
	}
	blockPersist(t, path)
	if n, err := s.DeleteParent(ctx, "p"); err == nil || n != 0 {
		t.Fatalf("failed DeleteParent = %d, %v; want 0 and an error", n, err)
	}
	if n, _ := s.Count(ctx, "p"); n != 2 {
		t.Fatalf("Count after failed DeleteParent = %d, want 2", n)
	}
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteParent(ctx, "p"); err != nil || n != 2 {
		t.Fatalf("DeleteParent = %d, %v; want 2, nil", n, err)
	}
}

// A refused DeleteParent of a parent that holds no records still leaves the
// parent as it was.
func TestPersistentScopedStore_FailedDeleteParentKeepsEmptiedParent(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p", "a"); err != nil {
		t.Fatal(err)
	}
	blockPersist(t, path)
	if _, err := s.DeleteParent(ctx, "p"); err == nil {
		t.Fatal("DeleteParent succeeded on an unwritable path")
	}
	if ok, _ := s.HasParent(ctx, "p"); !ok {
		t.Fatal("HasParent = false after a refused DeleteParent of an emptied parent, want true")
	}
}

func TestPersistentScopedStore_LoadValidatesRecords(t *testing.T) {
	withID := memory.WithRecordID(func(r storetest.Resource) string { return r.ID })
	for name, tc := range map[string]struct {
		content string
		opts    []memory.PersistentScopedOption[storetest.Resource]
		wantErr bool
	}{
		"empty parent":    {`{"version":1,"records":[{"parent":"","id":"a","value":{"ID":"a"}}]}`, nil, true},
		"empty id":        {`{"version":1,"records":[{"parent":"p","id":"","value":{"ID":"a"}}]}`, nil, true},
		"id disagrees":    {`{"version":1,"records":[{"parent":"p","id":"a","value":{"ID":"other"}}]}`, []memory.PersistentScopedOption[storetest.Resource]{withID}, true},
		"id agrees":       {`{"version":1,"records":[{"parent":"p","id":"a","value":{"ID":"a"}}]}`, []memory.PersistentScopedOption[storetest.Resource]{withID}, false},
		"no id check set": {`{"version":1,"records":[{"parent":"p","id":"a","value":{"ID":"other"}}]}`, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "r.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources", tc.opts...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
