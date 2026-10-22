//go:build !unix

package devicelog

// noFollow is unavailable here; the directory check in PrepareDir is the
// only guard on these platforms.
const noFollow = 0

// openedSymlink is always false here: without noFollow the Lstat in
// openLogFile is what refuses a symlink.
func openedSymlink(error) bool { return false }
