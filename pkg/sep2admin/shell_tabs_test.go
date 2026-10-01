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

// shellTabPath matches one routeList entry's path in either quote style,
// e.g. path: '/ui/fsas' or path: "/ui/fsas".
var shellTabPath = regexp.MustCompile(`path:\s*['"]/ui/([a-z][a-z0-9-]*)['"]`)

// commentedOut matches a line that is only a // comment and a /* */ block, so a tab
// that is commented out of routeList is not read as a live tab.
var commentedOut = regexp.MustCompile(`(?s)/\*.*?\*/|(?m)^[ \t]*//[^\n]*`)

// shellTabSlugs returns the live /ui/ tab slugs in src, in order.
func shellTabSlugs(src string) []string {
	var slugs []string
	for _, m := range shellTabPath.FindAllStringSubmatch(commentedOut.ReplaceAllString(src, ""), -1) {
		slugs = append(slugs, m[1])
	}
	return slugs
}

func TestShellTabSlugsSkipsCommentsAndReadsBothQuotes(t *testing.T) {
	src := `export const routeList = [
  { path: '/ui/overview', label: 'Overview' },
  // { path: '/ui/retired', label: 'Retired' },
  /* { path: '/ui/draft', label: 'Draft' }, */
  { path: "/ui/derms", label: "DERMS" },
]`
	if got, want := shellTabSlugs(src), []string{"overview", "derms"}; !slices.Equal(got, want) {
		t.Fatalf("shellTabSlugs = %v, want %v", got, want)
	}
}

func TestCoreTabsMatchTheShell(t *testing.T) {
	src, err := os.ReadFile(shellRouteTable)
	if err != nil {
		t.Fatalf("read the shell's route table: %v", err)
	}
	shell := shellTabSlugs(string(src))
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
