package sep2capture

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// closeStore is the shared test cleanup for Store.Close: cleanup itself is
// not the property under test in most tests here, so a bounded context
// stands in for the deadline a real caller would supply, and a failure is
// logged rather than failing the test, mirroring recording_test.go's
// closeRecorder.
func closeStore(t testing.TB, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Logf("Store.Close in cleanup: %v", err)
	}
}

// makeExchange builds a synthetic, well-formed exchange whose request and
// response bytes are each exactly reqLen and respLen bytes: a real request
// or status line followed by filler derived from id, so two different
// exchanges never collide byte for byte and TestExchangeBytesRoundTrip can
// tell them apart.
func makeExchange(id, connID uint64, client string, reqLen, respLen int) Exchange {
	req := padLine(fmt.Sprintf("GET /x HTTP/1.1\r\nX-ID: %d\r\n\r\n", id), reqLen, byte('a'+id%26))
	resp := padLine(fmt.Sprintf("HTTP/1.1 200 OK\r\nX-ID: %d\r\n\r\n", id), respLen, byte('A'+id%26))
	now := time.Now()
	return Exchange{
		ID:          id,
		ConnID:      connID,
		ClientLFDI:  client,
		ClientSFDI:  client + "-sfdi",
		Started:     now,
		Ended:       now.Add(time.Millisecond),
		Request:     Direction{Bytes: req, TrueLen: int64(len(req))},
		Response:    Direction{Bytes: resp, TrueLen: int64(len(resp))},
		Mark:        MarkHandled,
		HandlerRuns: 1,
	}
}

// padLine returns head, truncated or padded with fill to exactly n bytes,
// so callers get a precise, predictable payload size.
func padLine(head string, n int, fill byte) []byte {
	b := []byte(head)
	if len(b) >= n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	for i := len(b); i < n; i++ {
		out[i] = fill
	}
	return out
}

// waitQueueDrained polls until the store's intake queue has emptied (every
// Record call handed to the writer goroutine has been written, dropped, or
// abandoned), so a size or index assertion right after a batch of Record
// calls observes the writer's own settled state rather than a race with
// it.
func waitQueueDrained(t testing.TB, s *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.inFlightBytes.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("waitQueueDrained: still %d bytes in flight after 5s", s.inFlightBytes.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// dirSize is the consumer's own view of the capture directory: real file
// sizes read with os.ReadDir, exactly as the acceptance criterion (Q7
// item 3) specifies, never the store's own internal counter.
func dirSize(t testing.TB, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%s): %v", dir, err)
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("Info(%s): %v", e.Name(), err)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}
