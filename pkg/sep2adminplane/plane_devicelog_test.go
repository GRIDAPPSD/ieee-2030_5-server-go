package sep2adminplane_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

const planeLogLFDI = "0123456789ABCDEF0123456789ABCDEF01234567"

// An embedder passes the Reader for the directory its router writes; the
// plane serves it behind the credential, and answers 503 when it passes none.
func TestPlaneServesDeviceLogsBehindACredential(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	line := `{"seq":1,"t":"2026-10-21T12:00:00Z","kind":"request","actor":"` + planeLogLFDI + `","path":"/dcap"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, planeLogLFDI+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := devicelog.NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}

	cfg := baseConfig()
	cfg.LoopbackBypass = true
	cfg.DeviceLogs = reader
	on := newPlane(t, cfg)
	path := "/api/device-logs/" + planeLogLFDI

	if w := send(on, http.MethodGet, path, "", false); w.Code != http.StatusUnauthorized {
		t.Errorf("no credential = %d %s, want 401", w.Code, w.Body)
	}
	w := send(on, http.MethodGet, path, "", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"path":"/dcap"`) {
		t.Errorf("credentialed = %d %s, want 200 with the stored line", w.Code, w.Body)
	}

	off := newPlane(t, baseConfig())
	if w := send(off, http.MethodGet, path, "", true); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no reader = %d %s, want 503", w.Code, w.Body)
	}
}
