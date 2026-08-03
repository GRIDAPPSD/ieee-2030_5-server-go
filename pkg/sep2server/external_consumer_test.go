package sep2server_test

// The external-consumer proof for IEEESRV-025.
//
// Every package inside this module can import internal/, so no test that lives
// here can demonstrate that the embeddable surface is genuinely reachable from
// outside. A surface that named an internal type anywhere in an exported
// signature would compile perfectly in-tree and fail for the one consumer it
// was built for, with "use of internal package not allowed", which is the exact
// failure this card exists to retire.
//
// So the proof is a real second module: built in a temp directory, requiring
// this one through a replace directive, importing ONLY pkg/sep2server and
// core's public packages. If any exported symbol leaks an internal type, that
// module does not compile and this test fails.
//
// It exercises rather than only compiles: it assembles the protocol handler,
// serves a request through it, reads the body, and reports the route count back
// so this test can cross-check it against the in-tree count. A pure compile
// check would prove importability without proving the surface does anything.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
)

// externalConsumerSource is the second module's program. It is a Go source
// file held as a string on purpose: committing it as a real package would put
// it back inside this module, where the internal/ visibility rule it is
// testing does not apply.
const externalConsumerSource = `package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
)

// Compile-time proof that every exported member of the surface is nameable
// from outside the module: a signature mentioning an internal type would not
// resolve here.
var (
	_ func(sep2server.Config) (*sep2server.Server, error)                          = sep2server.New
	_ func(sep2server.Config, sep2srv.Identity) (http.Handler, []string)           = sep2server.BuildHandler
	_ func() *assembly.Stores                                                      = sep2server.NewStores
	_ func() assembly.AuthPolicy                                                   = sep2server.DefaultAuthPolicy
	_ func(*sep2server.Server) http.Handler                                        = (*sep2server.Server).Handler
	_ func(*sep2server.Server) []string                                            = (*sep2server.Server).Patterns
	_ func(*sep2server.Server) *assembly.Stores                                    = (*sep2server.Server).Stores
	_ func(*sep2server.Server) sep2srv.Identity                                    = (*sep2server.Server).Identity
	_ func(*sep2server.Server) string                                              = (*sep2server.Server).Addr
	_ func(*sep2server.Server, context.Context) error                              = (*sep2server.Server).Run
)

// identitySource is the bridge admin UI's own consumer-side interface, copied
// verbatim from gridappsd-ieee-2030_5-go internal/adminui/server.go. It is the
// second-consumer check made executable: the surface built for the bridge has
// to serve the admin UI unchanged, and the admin UI asks for exactly these two
// methods. A rename or a signature change on either breaks HERE rather than in
// the downstream repository.
type identitySource interface {
	Identity() sep2srv.Identity
	Addr() string
}

var _ identitySource = (*sep2server.Server)(nil)

// The admin UI's other need is read access to the stores, which it takes
// through its own snapshot projections: List and Get over EndDevices, DERs,
// DERPrograms, DefaultDERControls and DERControls. Every one of those is a
// pure read, so all five narrow to core's reader interfaces. Asserting the
// narrowing from OUTSIDE the module is what shows IEEESRV-026's privilege
// split needs nothing from this surface that it does not already have.
func assertAdminUIReadNeedsNarrow(stores *assembly.Stores) {
	var (
		_ store.ResourceReader[sep2.EndDevice]         = stores.EndDevices
		_ store.ScopedReader[sep2.DER]                 = stores.DERs
		_ store.ScopedReader[sep2.DERProgram]          = stores.DERPrograms
		_ store.ScopedReader[sep2.DefaultDERControl]   = stores.DefaultDERControls
		_ store.ScopedReader[sep2.DERControl]          = stores.DERControls
	)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	// 1. Construction: an embedder supplies its own stores and its own auth
	//    policy, exactly as the bridge does.
	stores := sep2server.NewStores()

	var reachedRouter bool
	passThrough := assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reachedRouter = true
				next.ServeHTTP(w, r)
			})
		},
	}

	cfg := sep2server.Config{
		Addr:     "127.0.0.1:0",
		CertFile: "cert.pem",
		KeyFile:  "key.pem",
		CAFile:   "ca.pem",
		Stores:   stores,
		Auth:     passThrough,
	}

	// 2. Store handles: the accessor's type is nameable out here, which is
	//    what lets an embedder seed and inject control through it.
	var handle *assembly.Stores = cfg.Stores
	if handle == nil {
		die("Config.Stores round-trip is nil")
	}
	assertAdminUIReadNeedsNarrow(handle)

	// 3. The router as an http.Handler, serving a real request.
	handler, patterns := sep2server.BuildHandler(cfg, sep2srv.Identity{SFDI: "123456789012", LFDI: "aabbccddeeff"})
	if handler == nil {
		die("BuildHandler returned a nil handler")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil))
	if rec.Code != http.StatusOK {
		die("GET /dcap: status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "DeviceCapability") {
		die("GET /dcap did not return a DeviceCapability document: %q", rec.Body.String())
	}
	if !reachedRouter {
		die("the supplied auth policy was never invoked")
	}

	// 4. Lifecycle, and the fail-closed construction contract: an embedder
	//    that forgets an auth policy must be refused rather than served.
	openCfg := cfg
	openCfg.Auth = assembly.AuthPolicy{}
	if _, err := sep2server.New(openCfg); err == nil {
		die("New accepted a config with no auth policy")
	}

	// DefaultAuthPolicy is the door onto this server's own enforcement, and
	// all three touch points have to be wired or an embedder silently gets a
	// weaker policy than the standalone binary runs.
	def := sep2server.DefaultAuthPolicy()
	if def.Wrap == nil || def.Identity == nil || def.SFDIPrefix == nil {
		die("DefaultAuthPolicy is incompletely wired: Wrap=%v Identity=%v SFDIPrefix=%v",
			def.Wrap != nil, def.Identity != nil, def.SFDIPrefix != nil)
	}

	fmt.Printf("patterns=%d\n", len(patterns))
	fmt.Println("EXTERNAL SURFACE OK")
}
`

func TestSurfaceIsReachableFromOutsideTheModule(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	dir := t.TempDir()

	goMod := "module externalconsumer\n\n" +
		"go 1.26.3\n\n" +
		"require github.com/GRIDAPPSD/ieee-2030_5-server-go v0.0.0\n\n" +
		"replace github.com/GRIDAPPSD/ieee-2030_5-server-go => " + moduleRoot + "\n"
	writeFile(t, filepath.Join(dir, "go.mod"), goMod)
	writeFile(t, filepath.Join(dir, "main.go"), externalConsumerSource)

	// Reuse this module's go.sum so the transitive hashes are already present
	// and current. Copying rather than pinning a second copy means a core bump
	// never leaves a stale checksum file behind to go red for the wrong reason.
	sum, err := os.ReadFile(filepath.Join(moduleRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	writeFile(t, filepath.Join(dir, "go.sum"), string(sum))

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		// -mod=mod lets the throwaway module settle its own requirements.
		"GOFLAGS=-mod=mod",
		// Everything needed is already in the module cache, because this
		// module built. Refusing the network keeps the test hermetic and
		// keeps a proxy outage from looking like a surface regression.
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the external consumer module failed to build or run: %v\n%s\n"+
			"An import error naming an internal package means the exported surface leaks an internal type.",
			err, out)
	}

	text := string(out)
	if !strings.Contains(text, "EXTERNAL SURFACE OK") {
		t.Fatalf("the external consumer did not complete:\n%s", text)
	}

	// Cross-check the route surface the outside consumer sees against the one
	// this module sees. A surface that compiled externally but mounted a
	// different route set would be a different server, not the same one.
	var externalPatterns int
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "patterns="); ok {
			externalPatterns, err = strconv.Atoi(rest)
			if err != nil {
				t.Fatalf("parse reported pattern count %q: %v", rest, err)
			}
		}
	}
	if externalPatterns == 0 {
		t.Fatalf("the external consumer did not report a pattern count:\n%s", text)
	}

	_, inTree := sep2server.BuildHandler(sep2server.Config{}, sep2srv.Identity{})
	if externalPatterns != len(inTree) {
		t.Errorf("route surface differs by vantage point: external consumer sees %d patterns, this module sees %d",
			externalPatterns, len(inTree))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
