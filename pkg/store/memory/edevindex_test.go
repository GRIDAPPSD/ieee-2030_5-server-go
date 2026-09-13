package memory_test

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestEndDeviceIndexAllocateIsIdempotent asserts the same device key always
// yields the same index, which is the whole point of the type: a device's
// URL must not move because it was provisioned twice.
func TestEndDeviceIndexAllocateIsIdempotent(t *testing.T) {
	t.Parallel()

	x := memory.NewEndDeviceIndex()
	first, err := x.Allocate("device-a")
	if err != nil {
		t.Fatalf("Allocate(device-a): %v", err)
	}
	second, err := x.Allocate("device-a")
	if err != nil {
		t.Fatalf("Allocate(device-a) again: %v", err)
	}
	if first != second {
		t.Errorf("index moved on re-allocation: %q then %q", first, second)
	}
	if first != "1" {
		t.Errorf("first index = %q, want %q (numbering starts at 1)", first, "1")
	}
}

// TestEndDeviceIndexDistinctKeysDistinctIndices asserts two devices never
// collide onto one index. A collision would make one URL address two
// devices, which is the worst failure this type can have.
func TestEndDeviceIndexDistinctKeysDistinctIndices(t *testing.T) {
	t.Parallel()

	x := memory.NewEndDeviceIndex()
	a, _ := x.Allocate("device-a")
	b, _ := x.Allocate("device-b")
	c, _ := x.Allocate("device-c")

	if a != "1" || b != "2" || c != "3" {
		t.Errorf("indices = %q/%q/%q, want 1/2/3", a, b, c)
	}
	if a == b || b == c || a == c {
		t.Errorf("index collision: %q %q %q", a, b, c)
	}
}

// TestEndDeviceIndexRejectsBlankKey asserts a blank device key fails closed
// rather than being defaulted. Synthesizing an index for an unidentified
// device would let two devices share one URL.
func TestEndDeviceIndexRejectsBlankKey(t *testing.T) {
	t.Parallel()

	x := memory.NewEndDeviceIndex()
	got, err := x.Allocate("")
	if err == nil {
		t.Fatalf("Allocate(\"\") = %q, nil; want an error", got)
	}
	if got != "" {
		t.Errorf("Allocate(\"\") returned index %q alongside its error; want empty", got)
	}
	if len(x.Assignments()) != 0 {
		t.Errorf("rejected allocation left state behind: %v", x.Assignments())
	}
}

// TestEndDeviceIndexReverseLookup asserts an index resolves back to its
// device key, which is how a request path is mapped onto a device record.
func TestEndDeviceIndexReverseLookup(t *testing.T) {
	t.Parallel()

	x := memory.NewEndDeviceIndex()
	idx, _ := x.Allocate("device-a")

	key, ok := x.DeviceKey(idx)
	if !ok {
		t.Fatalf("DeviceKey(%q) reported absent", idx)
	}
	if key != "device-a" {
		t.Errorf("DeviceKey(%q) = %q, want %q", idx, key, "device-a")
	}
	if _, ok := x.DeviceKey("99"); ok {
		t.Error("DeviceKey on an unassigned index reported present")
	}
	if _, ok := x.IndexFor("nobody"); ok {
		t.Error("IndexFor on an unknown device key reported present")
	}
}

// TestEndDeviceIndexStableAcrossRestart is the stability guarantee stated on
// EndDeviceIndex: with a persistence path, a device keeps its index across a
// process restart. The second allocator is a genuinely fresh instance loaded
// from disk, so this exercises the real reload path rather than shared
// memory.
func TestEndDeviceIndexStableAcrossRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "edevindex.json")

	before, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	want := map[string]string{}
	for _, key := range []string{"mrid-alpha", "mrid-bravo", "mrid-charlie"} {
		idx, err := before.Allocate(key)
		if err != nil {
			t.Fatalf("Allocate(%q): %v", key, err)
		}
		want[key] = idx
	}

	// Restart: a brand-new allocator reading the same file.
	after, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for key, wantIdx := range want {
		gotIdx, err := after.Allocate(key)
		if err != nil {
			t.Fatalf("post-restart Allocate(%q): %v", key, err)
		}
		if gotIdx != wantIdx {
			t.Errorf("device %q moved across restart: was index %q, now %q", key, wantIdx, gotIdx)
		}
	}
}

// TestEndDeviceIndexInsertionDoesNotShiftExisting asserts that adding a
// device after a restart leaves every existing device's index untouched. This
// is the property a sorted-order derivation would NOT have, and it is why
// assignments are persisted rather than recomputed.
func TestEndDeviceIndexInsertionDoesNotShiftExisting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "edevindex.json")

	before, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	alpha, _ := before.Allocate("mrid-alpha")
	charlie, _ := before.Allocate("mrid-charlie")

	after, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// "mrid-bravo" sorts between alpha and charlie: a sorted derivation
	// would give it charlie's old number and push charlie along.
	bravo, err := after.Allocate("mrid-bravo")
	if err != nil {
		t.Fatalf("Allocate(mrid-bravo): %v", err)
	}

	if got, _ := after.IndexFor("mrid-alpha"); got != alpha {
		t.Errorf("alpha shifted from %q to %q on insertion", alpha, got)
	}
	if got, _ := after.IndexFor("mrid-charlie"); got != charlie {
		t.Errorf("charlie shifted from %q to %q on insertion", charlie, got)
	}
	if bravo == alpha || bravo == charlie {
		t.Errorf("inserted device reused an existing index: %q", bravo)
	}
	if bravo != "3" {
		t.Errorf("inserted device index = %q, want %q (max+1, never a reused gap)", bravo, "3")
	}
}

// TestEndDeviceIndexNeverReusesAfterRestart asserts a fresh allocator does
// not hand out an index already recorded on disk. Reuse would point a stored
// link at a different device than the one it was captured against, which is
// silent cross-device addressing rather than a visible 404.
func TestEndDeviceIndexNeverReusesAfterRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "edevindex.json")
	before, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	for _, key := range []string{"a", "b", "c", "d"} {
		if _, err := before.Allocate(key); err != nil {
			t.Fatalf("Allocate(%q): %v", key, err)
		}
	}

	after, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fresh, err := after.Allocate("e")
	if err != nil {
		t.Fatalf("Allocate(e): %v", err)
	}
	if fresh != "5" {
		t.Errorf("post-restart allocation = %q, want %q (max on disk was 4)", fresh, "5")
	}
	for _, taken := range []string{"1", "2", "3", "4"} {
		if fresh == taken {
			t.Fatalf("post-restart allocation reused in-use index %q", taken)
		}
	}
}

// TestEndDeviceIndexPersistedShape asserts the on-disk record carries the
// device key and the index as written, so the file is a usable operator
// artifact and a reader can round-trip it. Asserts the bytes, per
// data-invariants Rule 1, not merely that a file appeared.
func TestEndDeviceIndexPersistedShape(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "edevindex.json")
	x, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := x.Allocate("mrid-alpha"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"version":1`,
		`"device_key":"mrid-alpha"`,
		`"index":"1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot missing %s\ngot: %s", want, got)
		}
	}
}

// TestEndDeviceIndexRejectsCorruptSnapshot asserts a snapshot that would
// make an index ambiguous is refused outright rather than partially loaded.
// A partially-loaded allocator could hand out an index already in use.
func TestEndDeviceIndexRejectsCorruptSnapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "duplicate index",
			body: `{"version":1,"records":[{"device_key":"a","index":"1"},{"device_key":"b","index":"1"}]}`,
		},
		{
			name: "duplicate device key",
			body: `{"version":1,"records":[{"device_key":"a","index":"1"},{"device_key":"a","index":"2"}]}`,
		},
		{
			name: "non numeric index",
			body: `{"version":1,"records":[{"device_key":"a","index":"AABB"}]}`,
		},
		{
			name: "zero index",
			body: `{"version":1,"records":[{"device_key":"a","index":"0"}]}`,
		},
		{
			name: "leading zero index",
			body: `{"version":1,"records":[{"device_key":"a","index":"01"}]}`,
		},
		{
			name: "leading zero multi digit index",
			body: `{"version":1,"records":[{"device_key":"a","index":"007"}]}`,
		},
		{
			name: "negative index",
			body: `{"version":1,"records":[{"device_key":"a","index":"-1"}]}`,
		},
		{
			name: "blank device key",
			body: `{"version":1,"records":[{"device_key":"","index":"1"}]}`,
		},
		{
			name: "unknown version",
			body: `{"version":99,"records":[{"device_key":"a","index":"1"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "edevindex.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			x, err := memory.NewEndDeviceIndexWithPersistence(path)
			if err == nil {
				t.Fatalf("want an error for %s, got allocator with %v", tc.name, x.Assignments())
			}
			if x != nil {
				t.Errorf("want a nil allocator alongside the error, got %v", x.Assignments())
			}
		})
	}
}

// TestEndDeviceIndexColdBoot asserts a missing snapshot file is a cold boot,
// not an error, and that numbering starts from 1.
func TestEndDeviceIndexColdBoot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	x, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("cold boot returned an error: %v", err)
	}
	idx, err := x.Allocate("device-a")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if idx != "1" {
		t.Errorf("cold-boot first index = %q, want %q", idx, "1")
	}
}

// TestEndDeviceIndexAssignmentsIsACopy asserts the exported map cannot be
// used to mutate allocator state.
func TestEndDeviceIndexAssignmentsIsACopy(t *testing.T) {
	t.Parallel()

	x := memory.NewEndDeviceIndex()
	idx, _ := x.Allocate("device-a")

	snapshot := x.Assignments()
	snapshot["device-a"] = "999"
	delete(snapshot, "device-a")

	if got, ok := x.IndexFor("device-a"); !ok || got != idx {
		t.Errorf("mutating the Assignments copy changed the allocator: got %q ok=%v, want %q", got, ok, idx)
	}
}

// TestEndDeviceIndexFromStoreSkipsOccupiedIndexesAfterRestart is acceptance
// criterion 2 for GRIDAPPSD/ieee-2030_5-server-go#443. EndDeviceStore
// persists its records across a restart while a plain NewEndDeviceIndex()
// does not, so a freshly booted, unseeded index handing out "1", "2", ...
// reissues an id a persisted device already occupies. Seeding the new index
// from the reloaded store's own contents closes that gap without adding a
// second on-disk file for the index to fall out of sync with.
func TestEndDeviceIndexFromStoreSkipsOccupiedIndexesAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "enddevices.json")

	first, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("NewEndDeviceStoreWithPersistence: %v", err)
	}
	if err := first.Create(ctx, "1", sep2.EndDevice{SFDI: "1111111111111111", LFDI: "lfdi-1"}); err != nil {
		t.Fatalf("create device 1: %v", err)
	}
	if err := first.Create(ctx, "2", sep2.EndDevice{SFDI: "2222222222222222", LFDI: "lfdi-2"}); err != nil {
		t.Fatalf("create device 2: %v", err)
	}

	// Restart: a new process reloads the persisted store from disk and
	// builds a fresh index seeded from it, the way internal/server/server.go
	// wires the two together.
	restarted, err := memory.NewEndDeviceStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload after restart: %v", err)
	}
	idx := memory.NewEndDeviceIndexFromStore(restarted)

	got, err := idx.Allocate("lfdi-3")
	if err != nil {
		t.Fatalf("Allocate after restart: %v", err)
	}
	if got == "1" || got == "2" {
		t.Fatalf("Allocate after restart returned %q, which is already occupied by a persisted device", got)
	}
	if got != "3" {
		t.Errorf("Allocate after restart = %q, want %q (first free index)", got, "3")
	}
}

// TestEndDeviceIndexFromStoreSeedsOneEntryPerDevice pins the ordinary case:
// every canonical-id device with a non-blank LFDI gets its own byKey entry,
// addressed by its stored id.
func TestEndDeviceIndexFromStoreSeedsOneEntryPerDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	if err := s.Create(ctx, "1", sep2.EndDevice{SFDI: "1111111111111111", LFDI: "lfdi-1"}); err != nil {
		t.Fatalf("create device 1: %v", err)
	}
	if err := s.Create(ctx, "2", sep2.EndDevice{SFDI: "2222222222222222", LFDI: "lfdi-2"}); err != nil {
		t.Fatalf("create device 2: %v", err)
	}

	idx := memory.NewEndDeviceIndexFromStore(s)

	if got, ok := idx.IndexFor("lfdi-1"); !ok || got != "1" {
		t.Errorf("IndexFor(lfdi-1) = (%q, %v), want (\"1\", true)", got, ok)
	}
	if got, ok := idx.IndexFor("lfdi-2"); !ok || got != "2" {
		t.Errorf("IndexFor(lfdi-2) = (%q, %v), want (\"2\", true)", got, ok)
	}
	if got, err := idx.Allocate("lfdi-3"); err != nil || got != "3" {
		t.Errorf("Allocate(lfdi-3) = (%q, %v), want (\"3\", nil)", got, err)
	}
}

// TestEndDeviceIndexFromStoreSkipsBlankLFDI pins that a record with a blank
// LFDI raises the counter floor (its numeric id is never reissued) but gets
// no byKey entry, since Allocate never assigns a blank key to look it up by.
func TestEndDeviceIndexFromStoreSkipsBlankLFDI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	if err := s.Create(ctx, "1", sep2.EndDevice{SFDI: "1111111111111111", LFDI: ""}); err != nil {
		t.Fatalf("create device 1: %v", err)
	}

	idx := memory.NewEndDeviceIndexFromStore(s)

	if _, ok := idx.IndexFor(""); ok {
		t.Error("IndexFor(\"\") reported present; a blank LFDI must never be a lookup key")
	}
	if got, err := idx.Allocate("lfdi-new"); err != nil || got != "2" {
		t.Errorf("Allocate(lfdi-new) = (%q, %v), want (\"2\", nil): id 1 must still raise the counter floor", got, err)
	}
}

// TestEndDeviceIndexFromStoreSkipsMalformedAndNonCanonicalIDs pins that
// non-numeric, non-canonical (leading zero), and negative-range ids neither
// raise the counter floor nor get a byKey entry.
func TestEndDeviceIndexFromStoreSkipsMalformedAndNonCanonicalIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	for id, lfdi := range map[string]string{
		"abc": "lfdi-abc",
		"007": "lfdi-007",
		"0":   "lfdi-0",
	} {
		if err := s.Create(ctx, id, sep2.EndDevice{SFDI: "1111111111111111", LFDI: lfdi}); err != nil {
			t.Fatalf("create device %q: %v", id, err)
		}
	}
	if err := s.Create(ctx, "3", sep2.EndDevice{SFDI: "3333333333333333", LFDI: "lfdi-3"}); err != nil {
		t.Fatalf("create device 3: %v", err)
	}

	idx := memory.NewEndDeviceIndexFromStore(s)

	for _, lfdi := range []string{"lfdi-abc", "lfdi-007", "lfdi-0"} {
		if _, ok := idx.IndexFor(lfdi); ok {
			t.Errorf("IndexFor(%q) reported present; a malformed/non-canonical id must not seed byKey", lfdi)
		}
	}
	if got, ok := idx.IndexFor("lfdi-3"); !ok || got != "3" {
		t.Errorf("IndexFor(lfdi-3) = (%q, %v), want (\"3\", true)", got, ok)
	}
	// Only "3" ever raised the floor: "007" is non-canonical and "0" is
	// below firstIndex, so neither counts.
	if got, err := idx.Allocate("lfdi-new"); err != nil || got != "4" {
		t.Errorf("Allocate(lfdi-new) = (%q, %v), want (\"4\", nil)", got, err)
	}
}

// TestEndDeviceIndexFromStoreDuplicateLFDIKeepsSnapshotOrderNotCreationOrder
// pins the #446 duplicate-LFDI rule stated on NewEndDeviceIndexFromStore's
// doc comment: the record entered into byKey is the first one reached in
// the store's own snapshot order (sorted lexicographically on the id
// string), not the one created first. "10" sorts before "9", so creating
// "9" first and "10" second still resolves the shared LFDI to "10".
func TestEndDeviceIndexFromStoreDuplicateLFDIKeepsSnapshotOrderNotCreationOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	const sharedLFDI = "lfdi-shared"
	if err := s.Create(ctx, "9", sep2.EndDevice{SFDI: "9999999999999999", LFDI: sharedLFDI}); err != nil {
		t.Fatalf("create device 9 (created first): %v", err)
	}
	if err := s.Create(ctx, "10", sep2.EndDevice{SFDI: "1010101010101010", LFDI: sharedLFDI}); err != nil {
		t.Fatalf("create device 10 (created second): %v", err)
	}

	idx := memory.NewEndDeviceIndexFromStore(s)

	got, ok := idx.IndexFor(sharedLFDI)
	if !ok {
		t.Fatal("IndexFor(sharedLFDI) reported absent; one of the two records must win")
	}
	if got != "10" {
		t.Errorf("IndexFor(sharedLFDI) = %q, want %q: snapshot order is lexicographic on the id string (\"10\" < \"9\"), not creation order", got, "10")
	}
	// Both ids raise the floor regardless of which one won byKey.
	if next, err := idx.Allocate("lfdi-new"); err != nil || next != "11" {
		t.Errorf("Allocate(lfdi-new) = (%q, %v), want (\"11\", nil)", next, err)
	}
}

// TestEndDeviceIndexFromStoreLogsSkippedRecordCount cannot run in parallel:
// it swaps the process-wide log output. It pins the operator-visible side of
// seeding: a skipped record is not silent, and the log carries only a count,
// never a device id or LFDI.
func TestEndDeviceIndexFromStoreLogsSkippedRecordCount(t *testing.T) {
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	if err := s.Create(ctx, "abc", sep2.EndDevice{SFDI: "1111111111111111", LFDI: "lfdi-abc"}); err != nil {
		t.Fatalf("create malformed-id device: %v", err)
	}
	if err := s.Create(ctx, "1", sep2.EndDevice{SFDI: "2222222222222222", LFDI: ""}); err != nil {
		t.Fatalf("create blank-LFDI device: %v", err)
	}

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	memory.NewEndDeviceIndexFromStore(s)

	logged := buf.String()
	if !strings.Contains(logged, "skipped 2 record") {
		t.Errorf("log = %q, want it to report skipping 2 records", logged)
	}
	if strings.Contains(logged, "abc") || strings.Contains(logged, "lfdi-abc") {
		t.Errorf("log = %q, leaked a skipped record's id or LFDI", logged)
	}
}

// TestEndDeviceIndexFromStoreLogsNothingWhenNothingSkipped is the control:
// a seed with no skips must not log a skip line at all.
func TestEndDeviceIndexFromStoreLogsNothingWhenNothingSkipped(t *testing.T) {
	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	if err := s.Create(ctx, "1", sep2.EndDevice{SFDI: "1111111111111111", LFDI: "lfdi-1"}); err != nil {
		t.Fatalf("create device 1: %v", err)
	}

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	memory.NewEndDeviceIndexFromStore(s)

	if buf.Len() != 0 {
		t.Errorf("log = %q, want empty: nothing was skipped", buf.String())
	}
}
