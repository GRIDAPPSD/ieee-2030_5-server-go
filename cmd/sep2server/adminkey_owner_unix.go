//go:build unix

package main

import (
	"io/fs"
	"os"
	"syscall"
)

// currentUID is a variable so tests can simulate a file owned by someone else.
var currentUID = os.Getuid

// fileOwnedByCurrentUser reports whether fi's owner is the running user.
func fileOwnedByCurrentUser(fi fs.FileInfo) (bool, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	return int(st.Uid) == currentUID(), nil
}
