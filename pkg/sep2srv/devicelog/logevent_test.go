package devicelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func fixedIdentity(lfdi string, ok bool) identity.Func {
	return func(context.Context) (string, string, bool) { return lfdi, "", ok }
}

func sampleEvent() sep2.LogEvent {
	ext := uint32(9001)
	ev := sep2.LogEvent{
		CreatedDateTime: 1604963588,
		Details:         "gen software alarm",
		ExtendedData:    &ext,
		FunctionSet:     sep2.FunctionSetLogEvent,
		LogEventCode:    7,
		LogEventID:      1234,
		LogEventPEN:     54465,
		ProfileID:       2,
	}
	ev.Href = "/edev/dev1/lel/abc"
	return ev
}

// fileLines closes w and returns the raw non-empty lines of lfdi's file, or
// nil when the file does not exist.
func fileLines(t *testing.T, w *Writer, dir, lfdi string) [][]byte {
	t.Helper()
	closeWriter(t, w)
	raw, err := os.ReadFile(filepath.Join(dir, lfdi+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out [][]byte
	for _, l := range bytes.Split(raw, []byte{'\n'}) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func TestLogEventStore_CreateWritesOneLineWithTheDocumentsFields(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	inner := memory.NewScopedStore[sep2.LogEvent]()
	s := NewLogEventStore(inner, w, fixedIdentity(strings.ToLower(lfdiA), true))

	ev := sampleEvent()
	if err := s.Create(context.Background(), "dev1", "abc", ev); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := inner.Get(context.Background(), "dev1", "abc"); err != nil {
		t.Fatalf("event not stored in the inner store: %v", err)
	}

	lines := fileLines(t, w, dir, lfdiA)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal(lines[0], &got); err != nil {
		t.Fatalf("line is not one JSON object: %v: %s", err, lines[0])
	}
	want := map[string]any{
		"seq": 1.0, "kind": "logevent", "actor": lfdiA, "edev": "dev1",
		"href": "/edev/dev1/lel/abc", "functionSet": float64(ev.FunctionSet),
		"logEventCode": 7.0, "logEventID": 1234.0, "logEventPEN": 54465.0,
		"profileID": 2.0, "createdDateTime": 1604963588.0, "extendedData": 9001.0,
		"details": "gen software alarm",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s = %v (%T), want %v", k, got[k], got[k], v)
		}
	}
	if _, ok := got["t"]; !ok {
		t.Error("line has no t field")
	}
	if len(got) != len(want)+1 {
		t.Errorf("line has %d fields, want %d: %s", len(got), len(want)+1, lines[0])
	}
}

func TestLogEventStore_DetailsWithLineBreakStaysOneObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	s := NewLogEventStore(memory.NewScopedStore[sep2.LogEvent](), w, fixedIdentity(lfdiA, true))

	ev := sampleEvent()
	ev.Details = "first\nsecond\r{\"seq\":99,\"kind\":\"forged\"}"
	if err := s.Create(context.Background(), "dev1", "abc", ev); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ev2 := sampleEvent()
	if err := s.Create(context.Background(), "dev1", "def", ev2); err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	lines := fileLines(t, w, dir, lfdiA)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (one per event)", len(lines))
	}
	var first struct {
		Seq     int    `json:"seq"`
		Kind    string `json:"kind"`
		Details string `json:"details"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("first line: %v", err)
	}
	if first.Kind != "logevent" || first.Seq != 1 {
		t.Errorf("first line kind/seq = %q/%d, want logevent/1", first.Kind, first.Seq)
	}
	if want := `first\nsecond\r{"seq":99,"kind":"forged"}`; first.Details != want {
		t.Errorf("details = %q, want the escaped %q", first.Details, want)
	}
}

func TestLogEventStore_RefusedCreateWritesNothing(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	s := NewLogEventStore(memory.NewScopedStore[sep2.LogEvent](), w, fixedIdentity(lfdiA, true))
	ev := sampleEvent()
	if err := s.Create(context.Background(), "dev1", "abc", ev); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := s.Create(context.Background(), "dev1", "abc", ev)
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate Create err = %v, want ErrAlreadyExists", err)
	}
	if lines := fileLines(t, w, dir, lfdiA); len(lines) != 1 {
		t.Fatalf("lines = %d, want 1 (the refused create wrote none)", len(lines))
	}
}

func TestLogEventStore_NoUsableIdentityWritesNothing(t *testing.T) {
	cases := map[string]identity.Func{
		"not authenticated": fixedIdentity(lfdiA, false),
		"malformed lfdi":    fixedIdentity("../../etc/passwd", true),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			w := newTestWriter(t, dir, Config{})
			inner := memory.NewScopedStore[sep2.LogEvent]()
			s := NewLogEventStore(inner, w, id)
			if err := s.Create(context.Background(), "dev1", "abc", sampleEvent()); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got := w.Dropped(); got != 1 {
				t.Errorf("Dropped() = %d, want 1: the lost line must be counted", got)
			}
			closeWriter(t, w)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("log dir holds %d files, want 0", len(entries))
			}
			if _, err := inner.Get(context.Background(), "dev1", "abc"); err != nil {
				t.Errorf("the create must still reach the inner store: %v", err)
			}
		})
	}
}

func TestLogEventStore_FileIsKeyedByIdentityNotByPath(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	s := NewLogEventStore(memory.NewScopedStore[sep2.LogEvent](), w, fixedIdentity(lfdiA, true))
	// The path id names a different device; the file stays the caller's.
	if err := s.Create(context.Background(), lfdiB, "abc", sampleEvent()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if lines := fileLines(t, w, dir, lfdiA); len(lines) != 1 {
		t.Fatalf("caller's file lines = %d, want 1", len(lines))
	}
	if _, err := os.Stat(filepath.Join(dir, lfdiB+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("path-named device got a file: %v", err)
	}
}

func TestNewLogEventStore_NilWriterReturnsInner(t *testing.T) {
	inner := memory.NewScopedStore[sep2.LogEvent]()
	if got := NewLogEventStore(inner, nil, fixedIdentity(lfdiA, true)); got != store.ScopedStore[sep2.LogEvent](inner) {
		t.Errorf("nil writer must return the inner store unchanged, got %T", got)
	}
}

func TestLogEventStore_FullQueueNeverFailsTheCreate(t *testing.T) {
	dir := t.TempDir()
	stall := make(chan struct{})
	w := newTestWriter(t, dir, Config{QueueSize: 1, onDequeue: func() { <-stall }})
	s := NewLogEventStore(memory.NewScopedStore[sep2.LogEvent](), w, fixedIdentity(lfdiA, true))
	for i := range 5 {
		id := string(rune('a' + i))
		if err := s.Create(context.Background(), "dev1", id, sampleEvent()); err != nil {
			t.Fatalf("Create %s with a stalled writer: %v", id, err)
		}
	}
	if w.Dropped() == 0 {
		t.Error("Dropped() = 0, want the overflow counted")
	}
	close(stall)
	closeWriter(t, w)
}

func TestLogEventStore_CreateAfterCloseIsCounted(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	w := newTestWriter(t, dir, Config{Logger: log.New(&logs, "", 0)})
	s := NewLogEventStore(memory.NewScopedStore[sep2.LogEvent](), w, fixedIdentity(lfdiA, true))
	closeWriter(t, w)
	if err := s.Create(context.Background(), "dev1", "abc", sampleEvent()); err != nil {
		t.Fatalf("Create after Close must still succeed: %v", err)
	}
	if got := w.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1", got)
	}
	if !strings.Contains(logs.String(), "writer closed") {
		t.Errorf("first drop not logged: %q", logs.String())
	}
}
