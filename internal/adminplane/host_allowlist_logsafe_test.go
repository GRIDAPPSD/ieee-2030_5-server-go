package adminplane

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A CR or LF in a refused request's Host, method or path must not start a
// second log line (#1236).
func TestHostAllowlistRefusalLogsOneEscapedLine(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := HostAllowlistMiddleware([]string{"localhost"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a disallowed host reached the next handler")
	}))
	req := httptest.NewRequest(http.MethodGet, "/forged", nil)
	req.Host = "evil.example\r\nINFO forged line"
	req.URL.Path = "/a\nINFO forged path"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMisdirectedRequest)
	}
	out := buf.String()
	if n := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; n != 1 {
		t.Fatalf("log held %d lines, want exactly 1:\n%s", n, out)
	}
	// The text handler quotes the message, so an escape logsafe wrote as \n
	// shows as \\n; an unescaped line break would show as a single \n.
	for _, want := range []string{`/a\\nINFO forged path`} {
		if !strings.Contains(out, want) {
			t.Errorf("log line lacks the visible escape %q:\n%s", want, out)
		}
	}
}
