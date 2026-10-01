package sep2admin

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// shellRouteTable is the shell's tab list, read from source rather than
// copied so that adding a tab there without reserving it here fails.
var shellRouteTable = filepath.Join("..", "adminui", "web", "frontend", "src", "routes", "index.ts")

// shellTabPath matches one routeList entry's path, e.g. path: '/ui/fsas'.
var shellTabPath = regexp.MustCompile(`path:\s*'/ui/([a-z][a-z0-9-]*)'`)

func TestCoreTabIDsMatchTheShell(t *testing.T) {
	src, err := os.ReadFile(shellRouteTable)
	if err != nil {
		t.Fatalf("read the shell's route table: %v", err)
	}
	var shell []string
	for _, m := range shellTabPath.FindAllStringSubmatch(string(src), -1) {
		shell = append(shell, m[1])
	}
	slices.Sort(shell)
	shell = slices.Compact(shell)
	if len(shell) == 0 {
		t.Fatalf("found no /ui/ tab paths in %s; the pattern no longer matches the file", shellRouteTable)
	}

	reserved := slices.Sorted(maps.Keys(coreTabIDs))
	if !slices.Equal(shell, reserved) {
		t.Fatalf("shell tabs %v, coreTabIDs %v: every shell tab must be a reserved panel ID and nothing else", shell, reserved)
	}
}

func TestEveryCoreTabIDIsRefusedAtRegister(t *testing.T) {
	for id := range coreTabIDs {
		if err := NewRegistry().Register(graftPanel(id, 1)); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Register(ID=%q): err = %v, want ErrInvalidID", id, err)
		}
	}
}
