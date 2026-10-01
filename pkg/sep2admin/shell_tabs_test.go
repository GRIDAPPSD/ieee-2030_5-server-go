package sep2admin

import (
	"errors"
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

func TestCoreTabsMatchTheShell(t *testing.T) {
	src, err := os.ReadFile(shellRouteTable)
	if err != nil {
		t.Fatalf("read the shell's route table: %v", err)
	}
	var shell []string
	for _, m := range shellTabPath.FindAllStringSubmatch(string(src), -1) {
		shell = append(shell, m[1])
	}
	if len(shell) == 0 {
		t.Fatalf("found no /ui/ tab paths in %s; the pattern no longer matches the file", shellRouteTable)
	}

	if !slices.Equal(shell, coreTabs) {
		t.Fatalf("shell tabs %v, coreTabs %v: the same slugs in the same nav order", shell, coreTabs)
	}
}

func TestEveryCoreTabIsRefusedAtRegister(t *testing.T) {
	for _, id := range coreTabs {
		if err := NewRegistry().Register(graftPanel(id, 1)); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Register(ID=%q): err = %v, want ErrInvalidID", id, err)
		}
	}
}
