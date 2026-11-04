//go:build !unix

package devicelog

// noFollow is unavailable here; the Lstat in openLogFile refuses a symlinked
// log file. The directory permission check does not run on Windows, so the
// server turns the log on there only for an operator-set directory.
const noFollow = 0

// openedSymlink is always false here: without noFollow the Lstat in
// openLogFile is what refuses a symlink.
func openedSymlink(error) bool { return false }
