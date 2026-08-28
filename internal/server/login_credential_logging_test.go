package server_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// #413: internal/auth's own tests prove LogFailedAdminCredential and
// LogSuccessfulAdminCredential are correct in isolation. These tests prove
// HandleLoginSubmit actually calls them, on the right branch.
//
// captureSlogForLogin duplicates internal/auth's captureSlog helper: the two
// packages' test binaries cannot share an unexported test helper, and each
// capture is small enough that duplicating it costs less than a third
// package would. slog.SetDefault is process-global, so none of these tests
// call t.Parallel().
func captureSlogForLogin(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestLoginSubmitWrongKeyLogsFailureViaAuth(t *testing.T) {
	buf := captureSlogForLogin(t)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	h := server.HandleLoginSubmit("the-secret", sessions)

	// The submitted key avoids the literal "wrong": the log's own outcome
	// field reads "wrong_credential", and a substring check against that
	// word would report a leak that is really the test's own vocabulary.
	const submittedKey = "totally-off-base-guess"
	form := url.Values{"key": []string{submittedKey}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	if !strings.Contains(buf.String(), `"msg":"admin: credential presented and rejected"`) {
		t.Errorf("wrong-key submission did not log a failure line: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"admission_path":"form"`) {
		t.Errorf("failure line missing admission_path=form: %s", buf.String())
	}
	if strings.Contains(buf.String(), submittedKey) {
		t.Errorf("failure line contains the submitted key verbatim: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"key"`) {
		t.Errorf("failure line appears to carry a key field: %s", buf.String())
	}
}

func TestLoginSubmitBlankKeyDoesNotLogFailure(t *testing.T) {
	buf := captureSlogForLogin(t)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	h := server.HandleLoginSubmit("the-secret", sessions)

	form := url.Values{"key": []string{"   "}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	if buf.Len() != 0 {
		t.Errorf("blank-key submission (credential-free on this path) logged something: %s", buf.String())
	}
}

func TestLoginSubmitCorrectKeyLogsSuccessViaAuth(t *testing.T) {
	buf := captureSlogForLogin(t)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	h := server.HandleLoginSubmit("the-secret", sessions)

	form := url.Values{"key": []string{"the-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	if !strings.Contains(buf.String(), `"msg":"admin: credential presented and accepted"`) {
		t.Errorf("correct-key submission did not log a success line: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"admission_path":"form"`) {
		t.Errorf("success line missing admission_path=form: %s", buf.String())
	}
}
