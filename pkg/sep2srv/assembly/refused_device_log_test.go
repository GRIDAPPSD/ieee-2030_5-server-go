package assembly_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// sendRefusedProbe sends GET path as lfdi with a query and secret headers,
// the text a refused line must never carry.
func sendRefusedProbe(t *testing.T, base, path, lfdi string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path+"?token=query-secret-value", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateIdentityHeader, lfdi)
	req.Header.Set("Authorization", "Bearer header-secret-value")
	req.Header.Set("Cookie", "session=cookie-secret-value")
	status, _ := sendGateRequest(t, req)
	return status
}

func TestDeviceLogRecordsOwnershipRefusalAsRefusedLine(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, unmanagedLFDI, "/edev/"+unmanagedID)

	// unmanagedLFDI is registered and owns device 3, so a read of device 1 is
	// the ownership gate's 403.
	if status := sendRefusedProbe(t, srv.URL, "/edev/"+victimID, unmanagedLFDI); status != http.StatusForbidden {
		t.Fatalf("control: GET /edev/%s as the other device = %d, want 403", victimID, status)
	}
	closeLog()

	raw, lines := linesFor(t, filepath.Join(dir, unmanagedLFDI+".jsonl"), "/edev/"+victimID)
	if len(lines) != 1 {
		t.Fatalf("got %d lines for the refused path, want 1: %s", len(lines), raw)
	}
	l := lines[0]
	want := map[string]any{
		"kind":   devicelog.KindRefused,
		"actor":  unmanagedLFDI,
		"method": http.MethodGet,
		"path":   "/edev/" + victimID,
		"status": float64(http.StatusForbidden),
		"reason": "not-owner-or-manager",
		"edev":   victimID,
	}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("field %s = %v, want %v", k, l[k], v)
		}
	}
	for _, leaked := range []string{"query-secret-value", "token", "header-secret-value", "cookie-secret-value", "Authorization", "Cookie"} {
		if strings.Contains(raw, leaked) {
			t.Errorf("line carries %q: %s", leaked, raw)
		}
	}
	if _, present := l["req_bytes"]; present {
		t.Errorf("refused line carries request-size fields: %v", l)
	}
}

func TestDeviceLogWritesNothingForRequestsWithoutAVerifiedCertificate(t *testing.T) {
	t.Parallel()
	// A policy whose Wrap answers 401 when no certificate identity is
	// present, the way the real identity layer does.
	policy := gateTestPolicy()
	inner := policy.Wrap
	policy.Wrap = func(next http.Handler) http.Handler {
		wrapped := inner(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(gateIdentityHeader) == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
	srv, dir, closeLog := logServer(t, policy)

	if status, _ := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("control: no-certificate request = %d, want 401", status)
	}
	closeLog()

	if names := logDirNames(t, dir); len(names) != 0 {
		t.Errorf("a 401 wrote %v, want no file", names)
	}
}

// A request that reaches the observer with no identity is refused by the
// ownership gate with a 403, and still has no certificate to key a line on.
func TestDeviceLogWritesNothingForAnIdentitylessRefusal(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())

	if status, _ := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, "", ""); status != http.StatusForbidden {
		t.Fatalf("control: request with no identity = %d, want 403", status)
	}
	closeLog()

	if names := logDirNames(t, dir); len(names) != 0 {
		t.Errorf("an identityless 403 wrote %v, want no file", names)
	}
}

func TestDeviceLogRefusedLoopStaysBoundedByRotation(t *testing.T) {
	t.Parallel()
	const maxBytes = 4096
	srv, dir, closeLog, _ := logServerSized(t, maxBytes, gateTestPolicy(), nil, nil)
	warmDeviceLog(t, srv, dir, unmanagedLFDI, "/edev/"+unmanagedID)

	// Each line is well over 100 bytes, so 200 of them pass the limit many
	// times over.
	for range 200 {
		if status, _ := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, unmanagedLFDI, ""); status != http.StatusForbidden {
			t.Fatalf("refused request status = %d, want 403", status)
		}
	}
	closeLog()

	cur := filepath.Join(dir, unmanagedLFDI+".jsonl")
	old := filepath.Join(dir, unmanagedLFDI+".1.jsonl")
	for _, p := range []string{cur, old} {
		raw, _ := readLogLines(t, p)
		if len(raw) > maxBytes {
			t.Errorf("%s holds %d bytes, want at most %d", filepath.Base(p), len(raw), maxBytes)
		}
	}
	if names := logDirNames(t, dir); len(names) != 2 {
		t.Errorf("directory holds %v, want the current and one rotated file", names)
	}
	_, lines := linesFor(t, cur, "/edev/"+victimID)
	if len(lines) == 0 || lines[len(lines)-1]["kind"] != devicelog.KindRefused {
		t.Errorf("current file refused lines = %v, want the newest to be kind refused", lines)
	}
}
