package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome expands a leading "~" in path to the running user's home
// directory, resolved via os.UserHomeDir. Only the leading position is
// special: a tilde anywhere else in path is an ordinary character and is
// returned unchanged, since it is a legal filename character.
//
// A shell expands "~" on a command line before a program ever sees it; an
// environment variable, a config file, a container spec, or a systemd unit
// passes the two characters through literally, and Go's filepath package
// never expands them either. Any SEP2_* path setting or CLI flag value that
// can come from one of those sources must be routed through this function
// (#598).
//
// The "~user" form (a tilde followed by a username) is rejected with an
// error rather than silently resolved to the current user's home: doing so
// would write to a location the operator did not name.
func ExpandHome(path string) (string, error) {
	if path == "~" {
		return os.UserHomeDir()
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rest), nil
	}
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("path %q uses the ~user form, which is not supported: use an absolute path, or ~/... for the current user's home", path)
	}
	return path, nil
}
