package sep2adminplane_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/extmodtest"
)

// externalPlaneSource is a program in a second module that builds the plane
// from public packages only and serves the admin UI and a registered panel
// through it. It is a string so it stays outside this module, where the
// internal/ rule it tests does not apply.
const externalPlaneSource = `package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
)

var (
	_ func(sep2adminplane.Config) (*sep2adminplane.Plane, error) = sep2adminplane.New
	_ func(*sep2adminplane.Plane) http.Handler                   = (*sep2adminplane.Plane).Handler
	_ func(*sep2adminplane.Plane) []string                       = (*sep2adminplane.Plane).Patterns
)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func get(h http.Handler, path string, withKey bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "localhost"
	if withKey {
		req.Header.Set("Authorization", "Bearer outside-key-0123456789")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func main() {
	plane, err := sep2adminplane.New(sep2adminplane.Config{
		Stores:       sep2server.NewStores(),
		AdminKey:     "outside-key-0123456789",
		AllowedHosts: []string{"localhost"},
		ReadOnly:     true,
		Panels: []sep2admin.Panel{{
			ID:                "outside-status",
			Label:             "Outside",
			Placement:         sep2admin.ExtensionSlot(1),
			DescriptorVersion: sep2admin.CurrentDescriptorVersion,
			View: func(context.Context) (sep2admin.Descriptor, error) {
				return sep2admin.Descriptor{
					Version: sep2admin.CurrentDescriptorVersion,
					Sections: []sep2admin.Section{{
						Heading: "Outside heading",
						Body:    sep2admin.NewTableBody(sep2admin.TableBody{Columns: []string{"name"}}),
					}},
				}, nil
			},
		}},
	})
	if err != nil {
		die("New: %v", err)
	}
	h := plane.Handler()

	if rec := get(h, "/ui/", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<div id=\"app\">") {
		die("GET /ui/ with the key: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(h, "/ui/", false); rec.Code != http.StatusUnauthorized {
		die("GET /ui/ from loopback with no credential: %d, want 401", rec.Code)
	}
	if rec := get(h, "/api/ui/panels/outside-status", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Outside heading") {
		die("GET the registered panel: %d %q", rec.Code, rec.Body.String())
	}
	// ReadOnly: a write gets 404 or 405 even with the key, a GET panel 200.
	for _, path := range []string{"/api/fsas", "/api/devices"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.RemoteAddr = "127.0.0.1:40000"
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer outside-key-0123456789")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			die("ReadOnly: POST %s = %d, want 404 or 405", path, rec.Code)
		}
	}
	if rec := get(h, "/api/fsas", true); rec.Code != http.StatusOK {
		die("ReadOnly: GET /api/fsas = %d, want 200", rec.Code)
	}
	fmt.Println("EXTERNAL PLANE OK")
}
`

func TestPlaneIsUsableFromOutsideTheModule(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	if out := extmodtest.Run(t, moduleRoot, externalPlaneSource); !strings.Contains(out, "EXTERNAL PLANE OK") {
		t.Fatalf("the external consumer did not complete:\n%s", out)
	}
}
