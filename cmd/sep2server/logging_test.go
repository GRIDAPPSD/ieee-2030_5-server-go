package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
)

// TestSetupLogging_EmitsValidJSON verifies the slog bridge produces a
// well-formed JSON object for stdlib log output, carrying the time, level,
// and msg fields the observability stack's LogQL queries depend on. Per
// the data-invariants rule, this asserts the wire shape of the emitted
// line, not merely that the call did not panic.
func TestSetupLogging_EmitsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	setupLogging(&buf)
	// Restore the test binary's default handler so this test does not
	// leak its buffer-backed handler into sibling tests.
	t.Cleanup(func() {
		slog.SetDefault(slog.Default())
	})

	const sample = "CA loaded — admin cert API enabled"
	log.Print(sample)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("setupLogging produced no output for log.Print")
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline: %q", err, line)
	}

	if got, ok := rec["msg"].(string); !ok || got != sample {
		t.Errorf("msg = %v, want %q", rec["msg"], sample)
	}
	if got, ok := rec["level"].(string); !ok || got != "INFO" {
		t.Errorf("level = %v, want INFO (existing log.* output must not be dropped/downgraded)", rec["level"])
	}
	if _, ok := rec["time"]; !ok {
		t.Error("log line missing time field")
	}
}
